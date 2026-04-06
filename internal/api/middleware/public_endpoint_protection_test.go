package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPublicEndpointProtectorLimitsOAuthProbeByAPIKey(t *testing.T) {
	current := time.Unix(1_700_000_000, 0)
	protector := newPublicEndpointProtector(func() time.Time { return current })
	req := httptest.NewRequest(http.MethodGet, "/v1/api/oauth-quota?probe=1", nil)

	for i := 0; i < 2; i++ {
		decision := protector.Evaluate(req, "sk-test", "203.0.113.10")
		if !decision.Allowed {
			t.Fatalf("request %d unexpectedly denied: %#v", i+1, decision)
		}
	}

	decision := protector.Evaluate(req, "sk-test", "203.0.113.10")
	if decision.Allowed {
		t.Fatal("third probe request should be rate-limited")
	}
	if decision.PolicyName != "oauth-quota-probe" {
		t.Fatalf("policy = %q, want %q", decision.PolicyName, "oauth-quota-probe")
	}
	if decision.Scope != "api_key" {
		t.Fatalf("scope = %q, want %q", decision.Scope, "api_key")
	}
	if decision.RetryAfter <= 0 {
		t.Fatalf("retry_after = %v, want > 0", decision.RetryAfter)
	}
}

func TestPublicEndpointProtectorAllowsAfterWindowReset(t *testing.T) {
	current := time.Unix(1_700_000_000, 0)
	protector := newPublicEndpointProtector(func() time.Time { return current })
	req := httptest.NewRequest(http.MethodGet, "/v1/api/oauth-quota?probe=1", nil)

	_ = protector.Evaluate(req, "sk-test", "203.0.113.10")
	_ = protector.Evaluate(req, "sk-test", "203.0.113.10")
	if decision := protector.Evaluate(req, "sk-test", "203.0.113.10"); decision.Allowed {
		t.Fatal("expected limiter to deny request before window reset")
	}

	current = current.Add(31 * time.Second)
	decision := protector.Evaluate(req, "sk-test", "203.0.113.10")
	if !decision.Allowed {
		t.Fatalf("request after window reset should be allowed: %#v", decision)
	}
}

func TestPublicEndpointProtectorBypassesLoopback(t *testing.T) {
	protector := newPublicEndpointProtector(time.Now)
	req := httptest.NewRequest(http.MethodGet, "/v1/api/oauth-quota?probe=1", nil)

	for i := 0; i < 6; i++ {
		decision := protector.Evaluate(req, "sk-test", "127.0.0.1")
		if !decision.Allowed {
			t.Fatalf("loopback request %d unexpectedly denied: %#v", i+1, decision)
		}
	}
}

func TestPublicEndpointProtectorLimitsCodexSubscriptionStatusByAPIKey(t *testing.T) {
	current := time.Unix(1_700_000_000, 0)
	protector := newPublicEndpointProtector(func() time.Time { return current })
	req := httptest.NewRequest(http.MethodGet, "/v1/api/codex-subscription-status", nil)

	for i := 0; i < 12; i++ {
		decision := protector.Evaluate(req, "sk-test", "203.0.113.10")
		if !decision.Allowed {
			t.Fatalf("request %d unexpectedly denied: %#v", i+1, decision)
		}
	}

	decision := protector.Evaluate(req, "sk-test", "203.0.113.10")
	if decision.Allowed {
		t.Fatal("thirteenth request should be rate-limited")
	}
	if decision.PolicyName != "codex-subscription-status-read" {
		t.Fatalf("policy = %q, want %q", decision.PolicyName, "codex-subscription-status-read")
	}
	if decision.Scope != "api_key" {
		t.Fatalf("scope = %q, want %q", decision.Scope, "api_key")
	}
}
