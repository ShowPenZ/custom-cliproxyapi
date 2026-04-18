package management

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestDeleteGeminiKey_RequiresBaseURLWhenAPIKeyMatchesMultipleEntries(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	cfg := &config.Config{
		GeminiKey: []config.GeminiKey{
			{APIKey: "dup-key", BaseURL: "https://a.example.com"},
			{APIKey: "dup-key", BaseURL: "https://b.example.com"},
		},
	}
	h := NewHandler(cfg, configPath, nil)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/config/gemini-key?api-key=dup-key", nil)

	h.DeleteGeminiKey(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "base-url is required") {
		t.Fatalf("body = %s, expected base-url guidance", rec.Body.String())
	}
	if len(h.cfg.GeminiKey) != 2 {
		t.Fatalf("gemini keys length = %d, want 2", len(h.cfg.GeminiKey))
	}
}

func TestDeleteGeminiKey_DeletesMatchingBaseURLOnly(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	cfg := &config.Config{
		GeminiKey: []config.GeminiKey{
			{APIKey: "dup-key", BaseURL: "https://a.example.com"},
			{APIKey: "dup-key", BaseURL: "https://b.example.com"},
		},
	}
	h := NewHandler(cfg, configPath, nil)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(
		http.MethodDelete,
		"/v0/management/config/gemini-key?api-key=dup-key&base-url="+url.QueryEscape("https://a.example.com"),
		nil,
	)

	h.DeleteGeminiKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(h.cfg.GeminiKey) != 1 {
		t.Fatalf("gemini keys length = %d, want 1", len(h.cfg.GeminiKey))
	}
	if got := h.cfg.GeminiKey[0].BaseURL; got != "https://b.example.com" {
		t.Fatalf("remaining base-url = %q, want %q", got, "https://b.example.com")
	}
}
