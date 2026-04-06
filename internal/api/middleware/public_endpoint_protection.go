package middleware

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	publicEndpointCleanupInterval = 5 * time.Minute
	publicEndpointEntryTTL        = 15 * time.Minute
)

type PublicEndpointPolicy struct {
	Name     string
	Window   time.Duration
	KeyLimit int
	IPLimit  int
}

type PublicEndpointDecision struct {
	Allowed    bool
	PolicyName string
	Scope      string
	RetryAfter time.Duration
}

type publicEndpointCounter struct {
	WindowStart time.Time
	Count       int
	LastSeen    time.Time
}

// PublicEndpointProtector applies per-API-key and per-IP fixed-window rate limits
// to selected internet-facing routes.
type PublicEndpointProtector struct {
	mu          sync.Mutex
	now         func() time.Time
	counters    map[string]*publicEndpointCounter
	lastCleanup time.Time
}

// NewPublicEndpointProtector constructs the default protector used by public API routes.
func NewPublicEndpointProtector() *PublicEndpointProtector {
	return newPublicEndpointProtector(time.Now)
}

func newPublicEndpointProtector(now func() time.Time) *PublicEndpointProtector {
	if now == nil {
		now = time.Now
	}
	return &PublicEndpointProtector{
		now:      now,
		counters: make(map[string]*publicEndpointCounter),
	}
}

// PublicEndpointProtectionMiddleware enforces route-specific rate limits after authentication.
func PublicEndpointProtectionMiddleware(protector *PublicEndpointProtector) gin.HandlerFunc {
	return func(c *gin.Context) {
		if protector == nil || c.Request == nil || c.Request.Method == http.MethodOptions {
			c.Next()
			return
		}

		apiKeyValue, _ := c.Get("apiKey")
		apiKey, _ := apiKeyValue.(string)
		decision := protector.Evaluate(c.Request, apiKey, c.ClientIP())
		if decision.Allowed {
			c.Next()
			return
		}

		retryAfterSeconds := retryAfterSeconds(decision.RetryAfter)
		if retryAfterSeconds > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfterSeconds))
		}
		if decision.PolicyName != "" {
			c.Header("X-RateLimit-Policy", decision.PolicyName)
		}
		c.Header("Cache-Control", "no-store")
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
			"error":               true,
			"message":             "rate limit exceeded",
			"policy":              decision.PolicyName,
			"scope":               decision.Scope,
			"retry_after_seconds": retryAfterSeconds,
		})
	}
}

// Evaluate returns the rate-limit decision for the request.
func (p *PublicEndpointProtector) Evaluate(req *http.Request, apiKey, clientIP string) PublicEndpointDecision {
	if p == nil || req == nil {
		return PublicEndpointDecision{Allowed: true}
	}
	policy, ok := classifyPublicEndpointPolicy(req)
	if !ok {
		return PublicEndpointDecision{Allowed: true}
	}
	if isLoopbackIP(clientIP) {
		return PublicEndpointDecision{Allowed: true, PolicyName: policy.Name}
	}

	now := p.now().UTC()
	apiKey = strings.TrimSpace(apiKey)
	clientIP = strings.TrimSpace(clientIP)

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.lastCleanup.IsZero() || now.Sub(p.lastCleanup) >= publicEndpointCleanupInterval {
		p.cleanupLocked(now)
		p.lastCleanup = now
	}

	if policy.KeyLimit > 0 && apiKey != "" {
		keyBucket := "key:" + policy.Name + ":" + apiKey
		if retryAfter, exceeded := p.wouldExceedLocked(keyBucket, policy.Window, policy.KeyLimit, now); exceeded {
			return PublicEndpointDecision{
				Allowed:    false,
				PolicyName: policy.Name,
				Scope:      "api_key",
				RetryAfter: retryAfter,
			}
		}
	}
	if policy.IPLimit > 0 && clientIP != "" {
		ipBucket := "ip:" + policy.Name + ":" + clientIP
		if retryAfter, exceeded := p.wouldExceedLocked(ipBucket, policy.Window, policy.IPLimit, now); exceeded {
			return PublicEndpointDecision{
				Allowed:    false,
				PolicyName: policy.Name,
				Scope:      "ip",
				RetryAfter: retryAfter,
			}
		}
	}

	if policy.KeyLimit > 0 && apiKey != "" {
		p.commitLocked("key:"+policy.Name+":"+apiKey, policy.Window, now)
	}
	if policy.IPLimit > 0 && clientIP != "" {
		p.commitLocked("ip:"+policy.Name+":"+clientIP, policy.Window, now)
	}

	return PublicEndpointDecision{Allowed: true, PolicyName: policy.Name}
}

func (p *PublicEndpointProtector) wouldExceedLocked(bucket string, window time.Duration, limit int, now time.Time) (time.Duration, bool) {
	counter := p.counterForLocked(bucket, window, now)
	if counter.Count < limit {
		return 0, false
	}
	retryAfter := counter.WindowStart.Add(window).Sub(now)
	if retryAfter < time.Second {
		retryAfter = time.Second
	}
	return retryAfter, true
}

func (p *PublicEndpointProtector) commitLocked(bucket string, window time.Duration, now time.Time) {
	counter := p.counterForLocked(bucket, window, now)
	counter.Count++
	counter.LastSeen = now
}

func (p *PublicEndpointProtector) counterForLocked(bucket string, window time.Duration, now time.Time) *publicEndpointCounter {
	counter, ok := p.counters[bucket]
	if !ok || now.Sub(counter.WindowStart) >= window {
		counter = &publicEndpointCounter{
			WindowStart: now,
			LastSeen:    now,
		}
		p.counters[bucket] = counter
		return counter
	}
	counter.LastSeen = now
	return counter
}

func (p *PublicEndpointProtector) cleanupLocked(now time.Time) {
	for bucket, counter := range p.counters {
		if counter == nil || now.Sub(counter.LastSeen) >= publicEndpointEntryTTL {
			delete(p.counters, bucket)
		}
	}
}

func classifyPublicEndpointPolicy(req *http.Request) (PublicEndpointPolicy, bool) {
	path := strings.TrimSpace(req.URL.Path)
	if path == "" {
		return PublicEndpointPolicy{}, false
	}
	if len(path) > 1 {
		path = strings.TrimRight(path, "/")
	}

	switch {
	case path == "/v1/api/oauth-quota":
		if queryTruthy(req.URL.Query().Get("probe")) {
			return PublicEndpointPolicy{
				Name:     "oauth-quota-probe",
				Window:   30 * time.Second,
				KeyLimit: 2,
				IPLimit:  4,
			}, true
		}
		return PublicEndpointPolicy{
			Name:     "oauth-quota-read",
			Window:   time.Minute,
			KeyLimit: 12,
			IPLimit:  24,
		}, true
	case path == "/v1/api/antigravity-quota":
		return PublicEndpointPolicy{
			Name:     "antigravity-quota-read",
			Window:   time.Minute,
			KeyLimit: 8,
			IPLimit:  16,
		}, true
	case path == "/v1/api/usage" || path == "/api/usage":
		return PublicEndpointPolicy{
			Name:     "usage-read",
			Window:   time.Minute,
			KeyLimit: 30,
			IPLimit:  60,
		}, true
	case path == "/v1/models" || path == "/v1beta/models":
		return PublicEndpointPolicy{
			Name:     "models-read",
			Window:   time.Minute,
			KeyLimit: 60,
			IPLimit:  120,
		}, true
	case path == "/v1/chat/completions",
		path == "/v1/completions",
		path == "/v1/messages",
		path == "/v1/messages/count_tokens",
		path == "/v1/responses",
		path == "/v1/responses/compact",
		strings.HasPrefix(path, "/v1beta/models/"):
		return PublicEndpointPolicy{
			Name:     "inference",
			Window:   time.Minute,
			KeyLimit: 240,
			IPLimit:  480,
		}, true
	default:
		return PublicEndpointPolicy{}, false
	}
}

func isLoopbackIP(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	ip := net.ParseIP(raw)
	return ip != nil && ip.IsLoopback()
}

func queryTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func retryAfterSeconds(delay time.Duration) int {
	if delay <= 0 {
		return 0
	}
	seconds := int((delay + time.Second - 1) / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}
