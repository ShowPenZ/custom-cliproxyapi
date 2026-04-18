package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func synthesizeConfigAuths(t *testing.T, cfg *config.Config) []*coreauth.Auth {
	t.Helper()

	auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{
		Config:      cfg,
		Now:         time.Unix(0, 0),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if err != nil {
		t.Fatalf("synthesize config auths: %v", err)
	}
	return auths
}

func findAuth(t *testing.T, auths []*coreauth.Auth, predicate func(*coreauth.Auth) bool) *coreauth.Auth {
	t.Helper()
	for _, auth := range auths {
		if predicate(auth) {
			return auth
		}
	}
	return nil
}

func TestGetGeminiKeys_IncludesAuthIndex(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{
		GeminiKey: []config.GeminiKey{
			{APIKey: "gem-key-1", BaseURL: "https://gemini.example.com"},
		},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	idGen := synthesizer.NewStableIDGenerator()
	authID, _ := idGen.Next("gemini:apikey", "gem-key-1", "https://gemini.example.com")
	registered, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       authID,
		Provider: "gemini",
		Status:   coreauth.StatusActive,
	})
	if err != nil {
		t.Fatalf("register auth: %v", err)
	}
	expectedIndex := registered.EnsureIndex()

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/config/gemini-key", nil)

	h.GetGeminiKeys(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Items []struct {
			APIKey    string `json:"api-key"`
			BaseURL   string `json:"base-url"`
			AuthIndex string `json:"auth-index"`
		} `json:"gemini-api-key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("items length = %d, want 1", len(payload.Items))
	}
	if payload.Items[0].AuthIndex != expectedIndex {
		t.Fatalf("auth-index = %q, want %q", payload.Items[0].AuthIndex, expectedIndex)
	}
}

func TestGetOpenAICompat_IncludesPerEntryAuthIndex(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "bohe",
				BaseURL: "https://compat.example.com/v1",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "compat-key-1", ProxyURL: "http://proxy.example.com:8080"},
				},
				Models: []config.OpenAICompatibilityModel{
					{Name: "kimi-k2.5", Alias: "kimi-k2"},
				},
			},
		},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	idGen := synthesizer.NewStableIDGenerator()
	authID, _ := idGen.Next(
		"openai-compatibility:bohe",
		"compat-key-1",
		"https://compat.example.com/v1",
		"http://proxy.example.com:8080",
	)
	registered, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       authID,
		Provider: "bohe",
		Status:   coreauth.StatusActive,
	})
	if err != nil {
		t.Fatalf("register auth: %v", err)
	}
	expectedIndex := registered.EnsureIndex()

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/config/openai-compatibility", nil)

	h.GetOpenAICompat(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Items []struct {
			Name          string `json:"name"`
			APIKeyEntries []struct {
				APIKey    string `json:"api-key"`
				ProxyURL  string `json:"proxy-url"`
				AuthIndex string `json:"auth-index"`
			} `json:"api-key-entries"`
		} `json:"openai-compatibility"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("items length = %d, want 1", len(payload.Items))
	}
	if len(payload.Items[0].APIKeyEntries) != 1 {
		t.Fatalf("api-key-entries length = %d, want 1", len(payload.Items[0].APIKeyEntries))
	}
	if payload.Items[0].APIKeyEntries[0].AuthIndex != expectedIndex {
		t.Fatalf("auth-index = %q, want %q", payload.Items[0].APIKeyEntries[0].AuthIndex, expectedIndex)
	}
}

func TestConfigAuthIndexResolvesLiveIndexes(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		GeminiKey: []config.GeminiKey{
			{APIKey: "shared-key", BaseURL: "https://a.example.com"},
			{APIKey: "shared-key", BaseURL: "https://b.example.com"},
		},
		ClaudeKey: []config.ClaudeKey{
			{APIKey: "claude-key", BaseURL: "https://claude.example.com"},
		},
		CodexKey: []config.CodexKey{
			{APIKey: "codex-key", BaseURL: "https://codex.example.com/v1"},
		},
		VertexCompatAPIKey: []config.VertexCompatKey{
			{APIKey: "vertex-key", BaseURL: "https://vertex.example.com", ProxyURL: "http://proxy.example.com:8080"},
		},
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "bohe",
				BaseURL: "https://bohe.example.com/v1",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "compat-key"},
				},
			},
		},
	}

	auths := synthesizeConfigAuths(t, cfg)
	manager := coreauth.NewManager(nil, nil, nil)
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register auth %q: %v", auth.ID, err)
		}
	}

	h := &Handler{cfg: cfg, authManager: manager}

	geminiAuthA := findAuth(t, auths, func(auth *coreauth.Auth) bool {
		return auth != nil &&
			auth.Provider == "gemini" &&
			auth.Attributes["api_key"] == "shared-key" &&
			auth.Attributes["base_url"] == "https://a.example.com"
	})
	if geminiAuthA == nil {
		t.Fatal("expected synthesized gemini auth (base a)")
	}
	geminiAuthB := findAuth(t, auths, func(auth *coreauth.Auth) bool {
		return auth != nil &&
			auth.Provider == "gemini" &&
			auth.Attributes["api_key"] == "shared-key" &&
			auth.Attributes["base_url"] == "https://b.example.com"
	})
	if geminiAuthB == nil {
		t.Fatal("expected synthesized gemini auth (base b)")
	}

	gemini := h.geminiKeysWithAuthIndex()
	if len(gemini) != 2 {
		t.Fatalf("gemini keys = %d, want 2", len(gemini))
	}
	if got, want := gemini[0].AuthIndex, geminiAuthA.EnsureIndex(); got != want {
		t.Fatalf("gemini[0] auth-index = %q, want %q", got, want)
	}
	if got, want := gemini[1].AuthIndex, geminiAuthB.EnsureIndex(); got != want {
		t.Fatalf("gemini[1] auth-index = %q, want %q", got, want)
	}
	if gemini[0].AuthIndex == gemini[1].AuthIndex {
		t.Fatalf("duplicate gemini entries returned the same auth-index %q", gemini[0].AuthIndex)
	}

	claudeAuth := findAuth(t, auths, func(auth *coreauth.Auth) bool {
		return auth != nil &&
			auth.Provider == "claude" &&
			auth.Attributes["api_key"] == "claude-key"
	})
	if claudeAuth == nil {
		t.Fatal("expected synthesized claude auth")
	}
	claude := h.claudeKeysWithAuthIndex()
	if len(claude) != 1 {
		t.Fatalf("claude keys = %d, want 1", len(claude))
	}
	if got, want := claude[0].AuthIndex, claudeAuth.EnsureIndex(); got != want {
		t.Fatalf("claude auth-index = %q, want %q", got, want)
	}

	codexAuth := findAuth(t, auths, func(auth *coreauth.Auth) bool {
		return auth != nil &&
			auth.Provider == "codex" &&
			auth.Attributes["api_key"] == "codex-key"
	})
	if codexAuth == nil {
		t.Fatal("expected synthesized codex auth")
	}
	codex := h.codexKeysWithAuthIndex()
	if len(codex) != 1 {
		t.Fatalf("codex keys = %d, want 1", len(codex))
	}
	if got, want := codex[0].AuthIndex, codexAuth.EnsureIndex(); got != want {
		t.Fatalf("codex auth-index = %q, want %q", got, want)
	}

	vertexAuth := findAuth(t, auths, func(auth *coreauth.Auth) bool {
		return auth != nil &&
			auth.Provider == "vertex" &&
			auth.Attributes["api_key"] == "vertex-key"
	})
	if vertexAuth == nil {
		t.Fatal("expected synthesized vertex auth")
	}
	vertex := h.vertexCompatKeysWithAuthIndex()
	if len(vertex) != 1 {
		t.Fatalf("vertex keys = %d, want 1", len(vertex))
	}
	if got, want := vertex[0].AuthIndex, vertexAuth.EnsureIndex(); got != want {
		t.Fatalf("vertex auth-index = %q, want %q", got, want)
	}

	compatAuth := findAuth(t, auths, func(auth *coreauth.Auth) bool {
		if auth == nil || auth.Provider != "bohe" {
			return false
		}
		if auth.Attributes["provider_key"] != "bohe" || auth.Attributes["compat_name"] != "bohe" {
			return false
		}
		return auth.Attributes["api_key"] == "compat-key"
	})
	if compatAuth == nil {
		t.Fatal("expected synthesized openai-compat auth")
	}
	compat := h.openAICompatibilityWithAuthIndex()
	if len(compat) != 1 {
		t.Fatalf("openai-compat providers = %d, want 1", len(compat))
	}
	if len(compat[0].APIKeyEntries) != 1 {
		t.Fatalf("openai-compat api-key-entries = %d, want 1", len(compat[0].APIKeyEntries))
	}
	if compat[0].AuthIndex != "" {
		t.Fatalf("provider-level auth-index should be empty when api-key-entries exist, got %q", compat[0].AuthIndex)
	}
	if got, want := compat[0].APIKeyEntries[0].AuthIndex, compatAuth.EnsureIndex(); got != want {
		t.Fatalf("openai-compat auth-index = %q, want %q", got, want)
	}
}

func TestConfigAuthIndexOmitsIndexesNotInManager(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		GeminiKey: []config.GeminiKey{
			{APIKey: "gemini-key", BaseURL: "https://a.example.com"},
		},
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "bohe",
				BaseURL: "https://bohe.example.com/v1",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "compat-key"},
				},
			},
		},
	}

	auths := synthesizeConfigAuths(t, cfg)
	geminiAuth := findAuth(t, auths, func(auth *coreauth.Auth) bool {
		return auth != nil &&
			auth.Provider == "gemini" &&
			auth.Attributes["api_key"] == "gemini-key"
	})
	if geminiAuth == nil {
		t.Fatal("expected synthesized gemini auth")
	}

	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), geminiAuth); err != nil {
		t.Fatalf("register gemini auth: %v", err)
	}

	h := &Handler{cfg: cfg, authManager: manager}

	gemini := h.geminiKeysWithAuthIndex()
	if len(gemini) != 1 {
		t.Fatalf("gemini keys = %d, want 1", len(gemini))
	}
	if gemini[0].AuthIndex == "" {
		t.Fatal("expected gemini auth-index to be set")
	}

	compat := h.openAICompatibilityWithAuthIndex()
	if len(compat) != 1 {
		t.Fatalf("openai-compat providers = %d, want 1", len(compat))
	}
	if len(compat[0].APIKeyEntries) != 1 {
		t.Fatalf("openai-compat api-key-entries = %d, want 1", len(compat[0].APIKeyEntries))
	}
	if compat[0].APIKeyEntries[0].AuthIndex != "" {
		t.Fatalf("openai-compat auth-index = %q, want empty", compat[0].APIKeyEntries[0].AuthIndex)
	}
}
