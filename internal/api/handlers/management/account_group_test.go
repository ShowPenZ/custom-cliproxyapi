package management

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func buildCodexAccountGroupTestJWT(t *testing.T, email, planType, accountID string) string {
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
			"chatgpt_account_id": accountID,
			"chatgpt_plan_type":  planType,
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

func TestBuildAuthFileEntry_DerivesCodexAccountGroup(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "codex-pro.json",
		Provider: "codex",
		FileName: "codex-pro.json",
		Metadata: map[string]any{
			"email":    "pro@example.com",
			"id_token": buildCodexAccountGroupTestJWT(t, "pro@example.com", "pro", "acct_pro"),
		},
		Attributes: map[string]string{
			"path": "/tmp/codex-pro.json",
		},
		Status: coreauth.StatusActive,
	}

	entry := (&Handler{}).buildAuthFileEntry(auth)
	if got := entry["plan_type"]; got != "pro" {
		t.Fatalf("plan_type = %v, want pro", got)
	}
	if got := entry["account_group"]; got != "pro" {
		t.Fatalf("account_group = %v, want pro", got)
	}
	if got := entry["account_group_label"]; got != "Codex Pro" {
		t.Fatalf("account_group_label = %v, want Codex Pro", got)
	}
}

func TestBuildAuthFileEntry_DerivesCodexAccountGroupFromProlite(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "codex-prolite.json",
		Provider: "codex",
		FileName: "codex-prolite.json",
		Metadata: map[string]any{
			"email":    "prolite@example.com",
			"id_token": buildCodexAccountGroupTestJWT(t, "prolite@example.com", "prolite", "acct_prolite"),
		},
		Attributes: map[string]string{
			"path": "/tmp/codex-prolite.json",
		},
		Status: coreauth.StatusActive,
	}

	entry := (&Handler{}).buildAuthFileEntry(auth)
	if got := entry["plan_type"]; got != "prolite" {
		t.Fatalf("plan_type = %v, want prolite", got)
	}
	if got := entry["account_group"]; got != "pro" {
		t.Fatalf("account_group = %v, want pro", got)
	}
	if got := entry["account_group_label"]; got != "Codex Pro" {
		t.Fatalf("account_group_label = %v, want Codex Pro", got)
	}
}

func TestBuildAuthFileEntry_DerivesCodexAccountGroupFromPro20x(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "codex-pro-20x.json",
		Provider: "codex",
		FileName: "codex-pro-20x.json",
		Metadata: map[string]any{
			"email":    "pro20x@example.com",
			"id_token": buildCodexAccountGroupTestJWT(t, "pro20x@example.com", "pro 20x", "acct_pro20x"),
		},
		Attributes: map[string]string{
			"path": "/tmp/codex-pro-20x.json",
		},
		Status: coreauth.StatusActive,
	}

	entry := (&Handler{}).buildAuthFileEntry(auth)
	if got := entry["plan_type"]; got != "pro 20x" {
		t.Fatalf("plan_type = %v, want pro 20x", got)
	}
	if got := entry["account_group"]; got != "pro" {
		t.Fatalf("account_group = %v, want pro", got)
	}
	if got := entry["account_group_label"]; got != "Codex Pro" {
		t.Fatalf("account_group_label = %v, want Codex Pro", got)
	}
}

func TestBuildRuntimeAuthEntry_UsesExplicitAccountGroup(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "codex-custom.json",
		Provider: "codex",
		FileName: "codex-custom.json",
		Metadata: map[string]any{
			"email":    "custom@example.com",
			"id_token": buildCodexAccountGroupTestJWT(t, "custom@example.com", "plus", "acct_plus"),
		},
		Attributes: map[string]string{
			"path":          "/tmp/codex-custom.json",
			"account_group": "vip-pro",
		},
		Status: coreauth.StatusActive,
	}

	entry := buildRuntimeAuthEntry(auth, nil, time.Now())
	if got := entry["account_group"]; got != "vip-pro" {
		t.Fatalf("account_group = %v, want vip-pro", got)
	}
	if got := entry["account_group_label"]; got != "vip-pro" {
		t.Fatalf("account_group_label = %v, want vip-pro", got)
	}
}

func TestPatchAuthFileFields_UpdatesAccountGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "codex-pro.json",
		Provider: "codex",
		FileName: "codex-pro.json",
		Status:   coreauth.StatusActive,
	}
	if _, err := manager.Register(t.Context(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	h := &Handler{authManager: manager}
	body := `{"name":"codex-pro.json","account_group":"pro-only"}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	updated, ok := manager.GetByID("codex-pro.json")
	if !ok {
		t.Fatal("updated auth missing")
	}
	if got := strings.TrimSpace(updated.Attributes["account_group"]); got != "pro-only" {
		t.Fatalf("attributes.account_group = %q, want %q", got, "pro-only")
	}
	if got, _ := updated.Metadata["account_group"].(string); got != "pro-only" {
		t.Fatalf("metadata.account_group = %q, want %q", got, "pro-only")
	}
}
