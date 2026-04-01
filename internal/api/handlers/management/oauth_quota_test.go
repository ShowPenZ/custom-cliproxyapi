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
	statusCode int
	headers    http.Header
}

func (e *oauthQuotaTestExecutor) Identifier() string { return "codex" }

func (e *oauthQuotaTestExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *oauthQuotaTestExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *oauthQuotaTestExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
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
			"id_token":     `{"https://api.openai.com/auth":{"chatgpt_plan_type":"plus"}}`,
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
	if row.PrimaryUsedPercent == nil || *row.PrimaryUsedPercent != 61 {
		t.Fatalf("primary_used_percent = %v, want 61", row.PrimaryUsedPercent)
	}
	if row.PrimaryRemainingPercent == nil || *row.PrimaryRemainingPercent != 39 {
		t.Fatalf("primary_remaining_percent = %v, want 39", row.PrimaryRemainingPercent)
	}
	if row.PrimaryResetAt == nil {
		t.Fatal("primary_reset_at = nil, want value")
	}
	if got := row.PrimaryResetAt.Format(time.RFC3339); got != "2026-03-28T09:26:02+08:00" {
		t.Fatalf("primary_reset_at = %q, want %q", got, "2026-03-28T09:26:02+08:00")
	}
	if row.SecondaryResetAt == nil {
		t.Fatal("secondary_reset_at = nil, want value")
	}
	if got := row.SecondaryResetAt.Format(time.RFC3339); got != "2026-04-01T20:04:13+08:00" {
		t.Fatalf("secondary_reset_at = %q, want %q", got, "2026-04-01T20:04:13+08:00")
	}
	if row.ProbeStatusCode == nil || *row.ProbeStatusCode != http.StatusOK {
		t.Fatalf("probe_status_code = %v, want 200", row.ProbeStatusCode)
	}
}
