package management

import (
	"context"
	"encoding/base64"
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

type codexSubscriptionRefreshTestExecutor struct {
	refreshedAuth *coreauth.Auth
}

func (e *codexSubscriptionRefreshTestExecutor) Identifier() string { return "codex" }

func (e *codexSubscriptionRefreshTestExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *codexSubscriptionRefreshTestExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *codexSubscriptionRefreshTestExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if e.refreshedAuth == nil {
		return auth, nil
	}
	return e.refreshedAuth.Clone(), nil
}

func (e *codexSubscriptionRefreshTestExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *codexSubscriptionRefreshTestExecutor) HttpRequest(_ context.Context, _ *coreauth.Auth, _ *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(stringsNewReader(`{"ok":true}`)),
		Header:     make(http.Header),
	}, nil
}

func TestGetAuthenticatedCodexSubscriptionStatusRequiresAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/codex-subscription-status", nil)

	h := &Handler{}
	h.GetAuthenticatedCodexSubscriptionStatus(c)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestGetAuthenticatedCodexSubscriptionStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	_, _ = manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-test-plus.json",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"email":        "test@example.com",
			"id_token":     buildCodexTestJWT(t, "test@example.com", "plus", "acct-1", "2026-04-01T02:39:29.735608+00:00", "2026-03-31T09:27:29+00:00", "2026-04-30T09:27:28+00:00"),
			"access_token": "token-1",
			"expired":      "2026-04-16T10:48:06+08:00",
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
	c.Request = httptest.NewRequest(http.MethodGet, "/api/codex-subscription-status", nil)
	c.Set("apiKey", "sk-team-alice-1234567890abcdef")

	h.GetAuthenticatedCodexSubscriptionStatus(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var payload codexSubscriptionStatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got := len(payload.Accounts); got != 1 {
		t.Fatalf("accounts len = %d, want 1", got)
	}

	row := payload.Accounts[0]
	if row.Account != "test@example.com" {
		t.Fatalf("account = %q, want %q", row.Account, "test@example.com")
	}
	if row.PlanType != "plus" {
		t.Fatalf("plan_type = %q, want %q", row.PlanType, "plus")
	}
	if row.ChatGPTAccountID != "acct-1" {
		t.Fatalf("chatgpt_account_id = %q, want %q", row.ChatGPTAccountID, "acct-1")
	}
	if row.SubscriptionActiveUntil == nil {
		t.Fatal("subscription_active_until = nil, want value")
	}
	if got := row.SubscriptionActiveUntil.Format(time.RFC3339); got != "2026-04-30T09:27:28Z" {
		t.Fatalf("subscription_active_until = %q, want %q", got, "2026-04-30T09:27:28Z")
	}
	if row.SubscriptionLastCheckedAt == nil {
		t.Fatal("subscription_last_checked_at = nil, want value")
	}
	if got := row.SubscriptionLastCheckedAt.Format(time.RFC3339Nano); got != "2026-04-01T02:39:29.735608Z" {
		t.Fatalf("subscription_last_checked_at = %q, want %q", got, "2026-04-01T02:39:29.735608Z")
	}
	if row.TokenExpiresAt == nil {
		t.Fatal("token_expires_at = nil, want value")
	}
	if got := row.TokenExpiresAt.Format(time.RFC3339); got != "2026-04-16T02:48:06Z" {
		t.Fatalf("token_expires_at = %q, want %q", got, "2026-04-16T02:48:06Z")
	}
}

func TestGetAuthenticatedCodexSubscriptionStatusSortsBySubscriptionActiveUntil(t *testing.T) {
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	_, _ = manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-later.json",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"email":    "later@example.com",
			"id_token": buildCodexTestJWT(t, "later@example.com", "plus", "acct-later", "2026-04-01T00:00:00+00:00", "2026-04-01T00:00:00+00:00", "2026-04-30T09:27:28+00:00"),
		},
	})
	_, _ = manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-earlier.json",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"email":    "earlier@example.com",
			"id_token": buildCodexTestJWT(t, "earlier@example.com", "plus", "acct-earlier", "2026-04-01T00:00:00+00:00", "2026-03-01T00:00:00+00:00", "2026-04-11T14:54:35+00:00"),
		},
	})

	h := &Handler{authManager: manager}
	rows := h.collectCodexSubscriptionStatusRows(context.Background(), false)
	if got := len(rows); got != 2 {
		t.Fatalf("rows len = %d, want 2", got)
	}
	if rows[0].Account != "earlier@example.com" {
		t.Fatalf("rows[0].account = %q, want %q", rows[0].Account, "earlier@example.com")
	}
	if rows[1].Account != "later@example.com" {
		t.Fatalf("rows[1].account = %q, want %q", rows[1].Account, "later@example.com")
	}
}

func TestGetAuthenticatedCodexSubscriptionStatusRefreshesExpiredSubscriptionClaim(t *testing.T) {
	gin.SetMode(gin.TestMode)

	now := time.Now().UTC()
	oldUntil := now.Add(-72 * time.Hour).Format(time.RFC3339)
	newUntil := now.Add(29 * 24 * time.Hour).Format(time.RFC3339)
	lastChecked := now.Add(-48 * time.Hour).Format(time.RFC3339)
	newLastChecked := now.Format(time.RFC3339)

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&codexSubscriptionRefreshTestExecutor{
		refreshedAuth: &coreauth.Auth{
			ID:       "codex-stale.json",
			Provider: "codex",
			Status:   coreauth.StatusActive,
			Metadata: map[string]any{
				"email":         "stale@example.com",
				"refresh_token": "refresh-1",
				"access_token":  "token-2",
				"expired":       now.Add(7 * 24 * time.Hour).Format(time.RFC3339),
				"id_token": buildCodexTestJWT(
					t,
					"stale@example.com",
					"plus",
					"acct-refresh",
					newLastChecked,
					now.Add(-30*24*time.Hour).Format(time.RFC3339),
					newUntil,
				),
			},
		},
	})
	_, _ = manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-stale.json",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"email":         "stale@example.com",
			"refresh_token": "refresh-1",
			"access_token":  "token-1",
			"expired":       now.Add(2 * time.Hour).Format(time.RFC3339),
			"id_token": buildCodexTestJWT(
				t,
				"stale@example.com",
				"plus",
				"acct-old",
				lastChecked,
				now.Add(-60*24*time.Hour).Format(time.RFC3339),
				oldUntil,
			),
		},
	})

	h := &Handler{authManager: manager}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/codex-subscription-status", nil)
	c.Set("apiKey", "sk-team-alice-1234567890abcdef")

	h.GetAuthenticatedCodexSubscriptionStatus(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var payload codexSubscriptionStatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got := len(payload.Accounts); got != 1 {
		t.Fatalf("accounts len = %d, want 1", got)
	}
	row := payload.Accounts[0]
	if row.ChatGPTAccountID != "acct-refresh" {
		t.Fatalf("chatgpt_account_id = %q, want %q", row.ChatGPTAccountID, "acct-refresh")
	}
	if row.SubscriptionActiveUntil == nil {
		t.Fatal("subscription_active_until = nil, want value")
	}
	if got := row.SubscriptionActiveUntil.Format(time.RFC3339); got != now.Add(29*24*time.Hour).UTC().Format(time.RFC3339) {
		t.Fatalf("subscription_active_until = %q, want %q", got, now.Add(29*24*time.Hour).UTC().Format(time.RFC3339))
	}
	if row.SubscriptionLastCheckedAt == nil {
		t.Fatal("subscription_last_checked_at = nil, want value")
	}
	if got := row.SubscriptionLastCheckedAt.Format(time.RFC3339); got != now.Format(time.RFC3339) {
		t.Fatalf("subscription_last_checked_at = %q, want %q", got, now.Format(time.RFC3339))
	}
}

func buildCodexTestJWT(t *testing.T, email, planType, accountID, lastChecked, activeStart, activeUntil string) string {
	t.Helper()

	headerJSON, err := json.Marshal(map[string]any{
		"alg": "RS256",
		"typ": "JWT",
	})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	payloadJSON, err := json.Marshal(map[string]any{
		"email": email,
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id":                accountID,
			"chatgpt_plan_type":                 planType,
			"chatgpt_subscription_last_checked": lastChecked,
			"chatgpt_subscription_active_start": activeStart,
			"chatgpt_subscription_active_until": activeUntil,
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	encode := func(raw []byte) string {
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	return encode(headerJSON) + "." + encode(payloadJSON) + ".signature"
}
