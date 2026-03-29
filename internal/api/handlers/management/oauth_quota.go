package management

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

const (
	oauthQuotaProbeEndpoint       = "https://chatgpt.com/backend-api/codex/responses/compact"
	oauthQuotaProbeModel          = "gpt-5.4-mini"
	oauthQuotaProbeText           = "Reply with OK only."
	oauthQuotaProbeUserAgent      = "codex_cli_rs/0.116.0 (Mac OS 26.0.1; arm64) Apple_Terminal/464"
	oauthQuotaProbeOriginator     = "codex_cli_rs"
	oauthQuotaProbeTimeout        = 30 * time.Second
	oauthQuotaProbeRefreshTimeout = 45 * time.Second
)

type oauthQuotaResponse struct {
	Error       bool                     `json:"error"`
	User        string                   `json:"user,omitempty"`
	APIKey      string                   `json:"api_key"`
	Probe       bool                     `json:"probe"`
	RequestedAt time.Time                `json:"requested_at"`
	Accounts    []oauthQuotaAccountEntry `json:"accounts"`
}

type oauthQuotaAccountEntry struct {
	Account                 string     `json:"account"`
	State                   string     `json:"state"`
	PlanType                string     `json:"plan_type,omitempty"`
	AuthID                  string     `json:"auth_id,omitempty"`
	PrimaryUsedPercent      *int       `json:"primary_used_percent,omitempty"`
	PrimaryRemainingPercent *int       `json:"primary_remaining_percent,omitempty"`
	PrimaryResetAt          *time.Time `json:"primary_reset_at,omitempty"`
	PrimaryResetAfterSecs   *int       `json:"primary_reset_after_seconds,omitempty"`
	SecondaryUsedPercent    *int       `json:"secondary_used_percent,omitempty"`
	SecondaryRemainingPct   *int       `json:"secondary_remaining_percent,omitempty"`
	SecondaryResetAt        *time.Time `json:"secondary_reset_at,omitempty"`
	SecondaryResetAfterSecs *int       `json:"secondary_reset_after_seconds,omitempty"`
	LastSeen                *time.Time `json:"last_seen,omitempty"`
	Source                  string     `json:"source,omitempty"`
	ProbeModel              string     `json:"probe_model,omitempty"`
	ProbeStatusCode         *int       `json:"probe_status_code,omitempty"`
	ProbeError              string     `json:"probe_error,omitempty"`
}

type oauthQuotaProbePayload struct {
	Model        string `json:"model"`
	Instructions string `json:"instructions"`
	Input        []struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"input"`
}

// GetAuthenticatedOAuthQuota returns upstream OAuth quota information for authenticated callers only.
// Probe mode sends one small live request to each Codex OAuth account and reads the X-Codex-* headers.
func (h *Handler) GetAuthenticatedOAuthQuota(c *gin.Context) {
	apiKey, _ := c.Get("apiKey")
	clientAPIKey, _ := apiKey.(string)
	clientAPIKey = strings.TrimSpace(clientAPIKey)
	if clientAPIKey == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": true, "message": "missing api key"})
		return
	}
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": true, "message": "core auth manager unavailable"})
		return
	}

	probe := queryTruthy(c.Query("probe"))
	model := strings.TrimSpace(c.Query("model"))
	if model == "" {
		model = oauthQuotaProbeModel
	}
	timeout := oauthQuotaProbeTimeout
	if raw := strings.TrimSpace(c.Query("timeout_seconds")); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			timeout = time.Duration(seconds) * time.Second
		}
	}

	rows := h.collectOAuthQuotaRows(c.Request.Context(), probe, model, timeout)

	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.Header("X-Content-Type-Options", "nosniff")
	c.JSON(http.StatusOK, oauthQuotaResponse{
		Error:       false,
		User:        ownerFromKey(clientAPIKey),
		APIKey:      maskAPIKey(clientAPIKey),
		Probe:       probe,
		RequestedAt: time.Now().UTC(),
		Accounts:    rows,
	})
}

func (h *Handler) collectOAuthQuotaRows(ctx context.Context, probe bool, model string, timeout time.Duration) []oauthQuotaAccountEntry {
	now := time.Now()
	auths := h.authManager.List()
	rows := make([]oauthQuotaAccountEntry, 0, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			continue
		}
		accountType, account := auth.AccountInfo()
		if !strings.EqualFold(accountType, "oauth") {
			continue
		}

		row := oauthQuotaAccountEntry{
			Account: strings.TrimSpace(account),
			State:   deriveRuntimeAuthState(auth, now),
			AuthID:  strings.TrimSpace(auth.ID),
			Source:  "runtime",
		}
		if claims := extractCodexIDTokenClaims(auth); claims != nil {
			if planType, ok := claims["plan_type"].(string); ok {
				row.PlanType = strings.TrimSpace(planType)
			}
		}

		if probe {
			h.applyOAuthProbe(ctx, auth, &row, model, timeout)
		}
		rows = append(rows, row)
	}

	sort.Slice(rows, func(i, j int) bool {
		return strings.ToLower(rows[i].Account) < strings.ToLower(rows[j].Account)
	})
	return rows
}

func (h *Handler) applyOAuthProbe(ctx context.Context, auth *coreauth.Auth, row *oauthQuotaAccountEntry, model string, timeout time.Duration) {
	if h == nil || h.authManager == nil || auth == nil || row == nil {
		return
	}
	result := h.doOAuthProbe(ctx, auth, model, timeout)
	if result.retryRefresh {
		refreshed, err := h.refreshOAuthProbeAuth(ctx, auth)
		if err == nil && refreshed != nil {
			result = h.doOAuthProbe(ctx, refreshed, model, timeout)
		}
	}

	row.ProbeModel = model
	if result.statusCode != 0 {
		row.ProbeStatusCode = intPtr(result.statusCode)
	}
	if result.errText != "" {
		row.ProbeError = result.errText
	}
	if result.planType != "" {
		row.PlanType = result.planType
	}
	if result.primaryUsedPercent != nil {
		row.PrimaryUsedPercent = result.primaryUsedPercent
		row.PrimaryRemainingPercent = intPtr(max(0, 100-*result.primaryUsedPercent))
	}
	if result.primaryResetAt != nil {
		row.PrimaryResetAt = result.primaryResetAt
	}
	if result.primaryResetAfter != nil {
		row.PrimaryResetAfterSecs = result.primaryResetAfter
	}
	if result.secondaryUsedPercent != nil {
		row.SecondaryUsedPercent = result.secondaryUsedPercent
		row.SecondaryRemainingPct = intPtr(max(0, 100-*result.secondaryUsedPercent))
	}
	if result.secondaryResetAt != nil {
		row.SecondaryResetAt = result.secondaryResetAt
	}
	if result.secondaryResetAfter != nil {
		row.SecondaryResetAfterSecs = result.secondaryResetAfter
	}
	if result.primaryUsedPercent != nil || result.secondaryUsedPercent != nil {
		now := time.Now().UTC()
		row.LastSeen = &now
		row.Source = "live-probe"
		row.ProbeError = ""
	}
}

type oauthProbeResult struct {
	statusCode         int
	errText            string
	retryRefresh       bool
	planType           string
	primaryUsedPercent *int
	primaryResetAt     *time.Time
	primaryResetAfter  *int
	secondaryUsedPercent *int
	secondaryResetAt     *time.Time
	secondaryResetAfter  *int
}

func (h *Handler) doOAuthProbe(ctx context.Context, auth *coreauth.Auth, model string, timeout time.Duration) oauthProbeResult {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, err := buildOAuthProbeBody(model)
	if err != nil {
		return oauthProbeResult{errText: err.Error()}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oauthQuotaProbeEndpoint, bytes.NewReader(body))
	if err != nil {
		return oauthProbeResult{errText: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Connection", "Keep-Alive")
	req.Header.Set("Originator", oauthQuotaProbeOriginator)
	req.Header.Set("User-Agent", oauthQuotaProbeUserAgent)
	sessionID := "probe-" + uuid.NewString()
	req.Header.Set("Session_id", sessionID)
	req.Header.Set("X-Client-Request-Id", sessionID)
	if accountID := authAccountID(auth); accountID != "" {
		req.Header.Set("Chatgpt-Account-Id", accountID)
	}

	resp, err := h.authManager.HttpRequest(ctx, auth, req)
	if err != nil {
		return oauthProbeResult{errText: err.Error()}
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	_, _ = io.Copy(io.Discard, resp.Body)

	result := oauthProbeResult{statusCode: resp.StatusCode}
	if resp.StatusCode == http.StatusUnauthorized {
		result.retryRefresh = true
	}
	if planType := strings.TrimSpace(resp.Header.Get("X-Codex-Plan-Type")); planType != "" {
		result.planType = planType
	}
	result.primaryUsedPercent = parseHeaderInt(resp.Header, "X-Codex-Primary-Used-Percent")
	result.primaryResetAfter = parseHeaderInt(resp.Header, "X-Codex-Primary-Reset-After-Seconds")
	result.primaryResetAt = parseHeaderEpoch(resp.Header, "X-Codex-Primary-Reset-At")
	result.secondaryUsedPercent = parseHeaderInt(resp.Header, "X-Codex-Secondary-Used-Percent")
	result.secondaryResetAfter = parseHeaderInt(resp.Header, "X-Codex-Secondary-Reset-After-Seconds")
	result.secondaryResetAt = parseHeaderEpoch(resp.Header, "X-Codex-Secondary-Reset-At")
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.errText = http.StatusText(resp.StatusCode)
	}
	return result
}

func (h *Handler) refreshOAuthProbeAuth(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if h == nil || h.authManager == nil || auth == nil {
		return nil, nil
	}
	exec, ok := h.authManager.Executor(auth.Provider)
	if !ok || exec == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, oauthQuotaProbeRefreshTimeout)
	defer cancel()

	refreshed, err := exec.Refresh(ctx, auth.Clone())
	if err != nil || refreshed == nil {
		return nil, err
	}
	updated, err := h.authManager.Update(ctx, refreshed)
	if err != nil {
		return refreshed, err
	}
	return updated, nil
}

func buildOAuthProbeBody(model string) ([]byte, error) {
	payload := oauthQuotaProbePayload{
		Model:        model,
		Instructions: "",
	}
	payload.Input = append(payload.Input, struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}{
		Role: "user",
		Content: []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{
			{Type: "input_text", Text: oauthQuotaProbeText},
		},
	})
	return json.Marshal(payload)
}

func authAccountID(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if accountID, ok := auth.Metadata["account_id"].(string); ok {
			return strings.TrimSpace(accountID)
		}
	}
	if claims := extractCodexIDTokenClaims(auth); claims != nil {
		if accountID, ok := claims["chatgpt_account_id"].(string); ok {
			return strings.TrimSpace(accountID)
		}
	}
	return ""
}

func parseHeaderInt(header http.Header, key string) *int {
	raw := strings.TrimSpace(header.Get(key))
	if raw == "" {
		return nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return nil
	}
	return &value
}

func parseHeaderEpoch(header http.Header, key string) *time.Time {
	raw := strings.TrimSpace(header.Get(key))
	if raw == "" {
		return nil
	}
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || seconds <= 0 {
		return nil
	}
	ts := time.Unix(seconds, 0).UTC()
	return &ts
}

func queryTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func intPtr(value int) *int {
	return &value
}

