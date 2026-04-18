package management

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type oauthQuotaTestExecutor struct {
	statusCode  int
	headers     http.Header
	refreshAuth *coreauth.Auth
}

func (e *oauthQuotaTestExecutor) Identifier() string { return "codex" }

func (e *oauthQuotaTestExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *oauthQuotaTestExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *oauthQuotaTestExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if e.refreshAuth != nil {
		return e.refreshAuth.Clone(), nil
	}
	return auth, nil
}

func (e *oauthQuotaTestExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *oauthQuotaTestExecutor) HttpRequest(_ context.Context, _ *coreauth.Auth, _ *http.Request) (*http.Response, error) {
	status := e.statusCode
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header:     e.headers.Clone(),
		Body:       io.NopCloser(stringsNewReader(`{"ok":true}`)),
	}, nil
}

type stringReader struct{ s string }

func (r *stringReader) Read(p []byte) (int, error) {
	if len(r.s) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.s)
	r.s = r.s[n:]
	return n, nil
}

func stringsNewReader(s string) io.Reader { return &stringReader{s: s} }

func TestGetAuthenticatedOAuthQuotaRequiresAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/oauth-quota", nil)

	h := &Handler{}
	h.GetAuthenticatedOAuthQuota(c)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestGetAuthenticatedOAuthQuotaProbe(t *testing.T) {
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&oauthQuotaTestExecutor{
		headers: http.Header{
			"X-Codex-Plan-Type":                     []string{"plus"},
			"X-Codex-Primary-Used-Percent":          []string{"61"},
			"X-Codex-Primary-Reset-After-Seconds":   []string{"9000"},
			"X-Codex-Primary-Reset-At":              []string{"1774771562"},
			"X-Codex-Secondary-Used-Percent":        []string{"55"},
			"X-Codex-Secondary-Reset-After-Seconds": []string{"421700"},
			"X-Codex-Secondary-Reset-At":            []string{"1775184253"},
		},
	})
	_, _ = manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-test-plus.json",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"email":        "test@example.com",
			"account_id":   "acct-1",
			"access_token": "token-1",
			"id_token":     buildCodexTestJWT(t, "test@example.com", "plus", "acct-1", "2026-04-01T02:39:29.735608+00:00", "2026-03-31T09:27:29+00:00", "2026-04-30T09:27:28+00:00"),
		},
	})
	_, _ = manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-api-key",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"api_key": "sk-upstream-1",
		},
	})

	h := &Handler{authManager: manager}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/oauth-quota?probe=1", nil)
	c.Set("apiKey", "sk-team-alice-1234567890abcdef")

	h.GetAuthenticatedOAuthQuota(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var payload oauthQuotaResponse
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !payload.Probe {
		t.Fatal("expected probe=true")
	}
	if got := len(payload.Accounts); got != 1 {
		t.Fatalf("accounts len = %d, want 1", got)
	}
	row := payload.Accounts[0]
	if row.Account != "test@example.com" {
		t.Fatalf("account = %q, want %q", row.Account, "test@example.com")
	}
	if row.SubscriptionActiveUntil == nil {
		t.Fatal("subscription_active_until = nil, want value")
	}
	if got := row.SubscriptionActiveUntil.Format(time.RFC3339); got != "2026-04-30T09:27:28Z" {
		t.Fatalf("subscription_active_until = %q, want %q", got, "2026-04-30T09:27:28Z")
	}
	if row.PrimaryUsedPercent == nil || *row.PrimaryUsedPercent != 61 {
		t.Fatalf("primary_used_percent = %v, want 61", row.PrimaryUsedPercent)
	}
	if row.PrimaryRemainingPercent == nil || *row.PrimaryRemainingPercent != 39 {
		t.Fatalf("primary_remaining_percent = %v, want 39", row.PrimaryRemainingPercent)
	}
	if row.PrimaryResetAt == nil {
		t.Fatal("primary_reset_at = nil, want value")
	}
	if got := row.PrimaryResetAt.Format(time.RFC3339); got != "2026-03-29T16:06:02+08:00" {
		t.Fatalf("primary_reset_at = %q, want %q", got, "2026-03-29T16:06:02+08:00")
	}
	if row.SecondaryResetAt == nil {
		t.Fatal("secondary_reset_at = nil, want value")
	}
	if got := row.SecondaryResetAt.Format(time.RFC3339); got != "2026-04-03T10:44:13+08:00" {
		t.Fatalf("secondary_reset_at = %q, want %q", got, "2026-04-03T10:44:13+08:00")
	}
	if row.ProbeStatusCode == nil || *row.ProbeStatusCode != http.StatusOK {
		t.Fatalf("probe_status_code = %v, want 200", row.ProbeStatusCode)
	}
}

func TestGetAuthenticatedOAuthQuotaProbeRefreshesSubscriptionClaim(t *testing.T) {
	gin.SetMode(gin.TestMode)

	now := time.Now().UTC()
	newUntil := now.Add(30 * 24 * time.Hour).Format(time.RFC3339)

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&oauthQuotaTestExecutor{
		headers: http.Header{
			"X-Codex-Plan-Type":            []string{"plus"},
			"X-Codex-Primary-Used-Percent": []string{"12"},
		},
		refreshAuth: &coreauth.Auth{
			ID:       "codex-stale-plus.json",
			Provider: "codex",
			Status:   coreauth.StatusActive,
			Metadata: map[string]any{
				"email":         "refresh@example.com",
				"account_id":    "acct-new",
				"access_token":  "token-2",
				"refresh_token": "refresh-1",
				"expired":       now.Add(7 * 24 * time.Hour).Format(time.RFC3339),
				"id_token": buildCodexTestJWT(
					t,
					"refresh@example.com",
					"plus",
					"acct-new",
					now.Format(time.RFC3339),
					now.Add(-15*24*time.Hour).Format(time.RFC3339),
					newUntil,
				),
			},
		},
	})
	_, _ = manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-stale-plus.json",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"email":         "refresh@example.com",
			"account_id":    "acct-old",
			"access_token":  "token-1",
			"refresh_token": "refresh-1",
			"expired":       now.Add(2 * time.Hour).Format(time.RFC3339),
			"id_token": buildCodexTestJWT(
				t,
				"refresh@example.com",
				"plus",
				"acct-old",
				now.Add(-48*time.Hour).Format(time.RFC3339),
				now.Add(-60*24*time.Hour).Format(time.RFC3339),
				now.Add(-72*time.Hour).Format(time.RFC3339),
			),
		},
	})

	h := &Handler{authManager: manager}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/oauth-quota?probe=1", nil)
	c.Set("apiKey", "sk-team-alice-1234567890abcdef")

	h.GetAuthenticatedOAuthQuota(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var payload oauthQuotaResponse
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got := len(payload.Accounts); got != 1 {
		t.Fatalf("accounts len = %d, want 1", got)
	}
	row := payload.Accounts[0]
	if row.SubscriptionActiveUntil == nil {
		t.Fatal("subscription_active_until = nil, want value")
	}
	if got := row.SubscriptionActiveUntil.Format(time.RFC3339); got != now.Add(30*24*time.Hour).UTC().Format(time.RFC3339) {
		t.Fatalf("subscription_active_until = %q, want %q", got, now.Add(30*24*time.Hour).UTC().Format(time.RFC3339))
	}
	if row.PrimaryUsedPercent == nil || *row.PrimaryUsedPercent != 12 {
		t.Fatalf("primary_used_percent = %v, want 12", row.PrimaryUsedPercent)
	}
}

func TestGetManagementOAuthQuotaDoesNotRequireAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	_, _ = manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-management-plus.json",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"email":        "manage@example.com",
			"account_id":   "acct-mgmt",
			"access_token": "token-mgmt",
			"id_token":     buildCodexTestJWT(t, "manage@example.com", "plus", "acct-mgmt", "2026-04-01T02:39:29.735608+00:00", "2026-03-31T09:27:29+00:00", "2026-04-30T09:27:28+00:00"),
		},
	})

	h := &Handler{authManager: manager}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/oauth-quota", nil)

	h.GetManagementOAuthQuota(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var payload oauthQuotaResponse
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if payload.User != "management" {
		t.Fatalf("user = %q, want %q", payload.User, "management")
	}
	if got := len(payload.Accounts); got != 1 {
		t.Fatalf("accounts len = %d, want 1", got)
	}
	if payload.Accounts[0].Account != "manage@example.com" {
		t.Fatalf("account = %q, want %q", payload.Accounts[0].Account, "manage@example.com")
	}
}
