package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	gin "github.com/gin-gonic/gin"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	internalusage "github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()

	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	cfg := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{"test-key"},
		},
		Port:                   0,
		AuthDir:                authDir,
		Debug:                  true,
		LoggingToFile:          false,
		UsageStatisticsEnabled: false,
	}

	authManager := auth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()

	configPath := filepath.Join(tmpDir, "config.yaml")
	return NewServer(cfg, authManager, accessManager, configPath)
}

func TestAmpProviderModelRoutes(t *testing.T) {
	testCases := []struct {
		name         string
		path         string
		wantStatus   int
		wantContains string
	}{
		{
			name:         "openai root models",
			path:         "/api/provider/openai/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "groq root models",
			path:         "/api/provider/groq/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "openai models",
			path:         "/api/provider/openai/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "anthropic models",
			path:         "/api/provider/anthropic/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"data"`,
		},
		{
			name:         "google models v1",
			path:         "/api/provider/google/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"models"`,
		},
		{
			name:         "google models v1beta",
			path:         "/api/provider/google/v1beta/models",
			wantStatus:   http.StatusOK,
			wantContains: `"models"`,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Authorization", "Bearer test-key")

			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)

			if rr.Code != tc.wantStatus {
				t.Fatalf("unexpected status code for %s: got %d want %d; body=%s", tc.path, rr.Code, tc.wantStatus, rr.Body.String())
			}
			if body := rr.Body.String(); !strings.Contains(body, tc.wantContains) {
				t.Fatalf("response body for %s missing %q: %s", tc.path, tc.wantContains, body)
			}
		})
	}
}

func TestClaudeCompatibilityRoutes(t *testing.T) {
	server := newTestServer(t)

	modelID := "claude-compat-route-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	registry.GetGlobalRegistry().RegisterClient("test-claude-compat-"+modelID, "antigravity", []*registry.ModelInfo{{
		ID:          modelID,
		OwnedBy:     "antigravity",
		Type:        "model",
		DisplayName: "Claude Compat Route",
	}})

	t.Run("models aliases return claude list shape", func(t *testing.T) {
		paths := []string{
			"/v1/models",
			"/v1/v1/models",
			"/v1/models/claude",
			"/v1/v1/models/claude",
			"/v1/messages/v1/models",
			"/v1/messages/v1/models/claude",
		}

		for _, path := range paths {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer test-key")
			req.Header.Set("User-Agent", "claude-cli/2.1.92 (external, cli)")

			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("unexpected status code for %s: got %d want %d; body=%s", path, rr.Code, http.StatusOK, rr.Body.String())
			}
			body := rr.Body.String()
			if !strings.Contains(body, `"object":"list"`) {
				t.Fatalf("response body for %s missing list object: %s", path, body)
			}
			if !strings.Contains(body, modelID) {
				t.Fatalf("response body for %s missing model id %q: %s", path, modelID, body)
			}
		}
	})

	t.Run("messages aliases are routed instead of 404", func(t *testing.T) {
		paths := []string{
			"/v1/messages",
			"/v1/v1/messages",
			"/v1/messages/v1/messages",
		}

		body := []byte(`{"model":"` + modelID + `","messages":[{"role":"user","content":"ping"}],"stream":false}`)
		for _, path := range paths {
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer test-key")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "claude-cli/2.1.92 (external, cli)")

			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)

			if rr.Code == http.StatusNotFound {
				t.Fatalf("route %s returned 404; body=%s", path, rr.Body.String())
			}
		}
	})
}

func TestDefaultRequestLoggerFactory_UsesResolvedLogDirectory(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")
	t.Setenv("writable_path", "")

	originalWD, errGetwd := os.Getwd()
	if errGetwd != nil {
		t.Fatalf("failed to get current working directory: %v", errGetwd)
	}

	tmpDir := t.TempDir()
	if errChdir := os.Chdir(tmpDir); errChdir != nil {
		t.Fatalf("failed to switch working directory: %v", errChdir)
	}
	defer func() {
		if errChdirBack := os.Chdir(originalWD); errChdirBack != nil {
			t.Fatalf("failed to restore working directory: %v", errChdirBack)
		}
	}()

	// Force ResolveLogDirectory to fallback to auth-dir/logs by making ./logs not a writable directory.
	if errWriteFile := os.WriteFile(filepath.Join(tmpDir, "logs"), []byte("not-a-directory"), 0o644); errWriteFile != nil {
		t.Fatalf("failed to create blocking logs file: %v", errWriteFile)
	}

	configDir := filepath.Join(tmpDir, "config")
	if errMkdirConfig := os.MkdirAll(configDir, 0o755); errMkdirConfig != nil {
		t.Fatalf("failed to create config dir: %v", errMkdirConfig)
	}
	configPath := filepath.Join(configDir, "config.yaml")

	authDir := filepath.Join(tmpDir, "auth")
	if errMkdirAuth := os.MkdirAll(authDir, 0o700); errMkdirAuth != nil {
		t.Fatalf("failed to create auth dir: %v", errMkdirAuth)
	}

	cfg := &proxyconfig.Config{
		SDKConfig: proxyconfig.SDKConfig{
			RequestLog: false,
		},
		AuthDir:           authDir,
		ErrorLogsMaxFiles: 10,
	}

	logger := defaultRequestLoggerFactory(cfg, configPath)
	fileLogger, ok := logger.(*internallogging.FileRequestLogger)
	if !ok {
		t.Fatalf("expected *FileRequestLogger, got %T", logger)
	}

	errLog := fileLogger.LogRequestWithOptions(
		"/v1/chat/completions",
		http.MethodPost,
		map[string][]string{"Content-Type": []string{"application/json"}},
		[]byte(`{"input":"hello"}`),
		http.StatusBadGateway,
		map[string][]string{"Content-Type": []string{"application/json"}},
		[]byte(`{"error":"upstream failure"}`),
		nil,
		nil,
		nil,
		true,
		"issue-1711",
		time.Now(),
		time.Now(),
	)
	if errLog != nil {
		t.Fatalf("failed to write forced error request log: %v", errLog)
	}

	authLogsDir := filepath.Join(authDir, "logs")
	authEntries, errReadAuthDir := os.ReadDir(authLogsDir)
	if errReadAuthDir != nil {
		t.Fatalf("failed to read auth logs dir %s: %v", authLogsDir, errReadAuthDir)
	}
	foundErrorLogInAuthDir := false
	for _, entry := range authEntries {
		if strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
			foundErrorLogInAuthDir = true
			break
		}
	}
	if !foundErrorLogInAuthDir {
		t.Fatalf("expected forced error log in auth fallback dir %s, got entries: %+v", authLogsDir, authEntries)
	}

	configLogsDir := filepath.Join(configDir, "logs")
	configEntries, errReadConfigDir := os.ReadDir(configLogsDir)
	if errReadConfigDir != nil && !os.IsNotExist(errReadConfigDir) {
		t.Fatalf("failed to inspect config logs dir %s: %v", configLogsDir, errReadConfigDir)
	}
	for _, entry := range configEntries {
		if strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
			t.Fatalf("unexpected forced error log in config dir %s", configLogsDir)
		}
	}
}

func TestUserUsageEndpointReturnsOnlyCurrentAPIUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	selfKey := "sk-team-self-1234567890abcdef1234567890abcdef"
	otherKey := "sk-team-other-fedcba0987654321fedcba0987654321"
	cfg := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{selfKey, otherKey},
		},
		Port:                   0,
		AuthDir:                authDir,
		Debug:                  true,
		LoggingToFile:          false,
		UsageStatisticsEnabled: true,
	}

	authManager := auth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()
	configPath := filepath.Join(tmpDir, "config.yaml")
	server := NewServer(cfg, authManager, accessManager, configPath)

	stats := internalusage.NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      selfKey,
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 3, 29, 11, 50, 0, 0, time.FixedZone("CST", 8*3600)),
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
	})
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      selfKey,
		Model:       "gpt-5.4-mini",
		RequestedAt: time.Date(2026, 3, 29, 11, 51, 0, 0, time.FixedZone("CST", 8*3600)),
		Failed:      true,
		Source:      "internal@example.com",
		AuthIndex:   "secret-auth-index",
		Detail: coreusage.Detail{
			InputTokens:  5,
			OutputTokens: 0,
			TotalTokens:  5,
		},
	})
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      otherKey,
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 3, 29, 11, 52, 0, 0, time.FixedZone("CST", 8*3600)),
		Source:      "other@example.com",
		AuthIndex:   "other-auth-index",
		Detail: coreusage.Detail{
			InputTokens:  100,
			OutputTokens: 200,
			TotalTokens:  300,
		},
	})
	server.mgmt.SetUsageStatistics(stats)

	req := httptest.NewRequest(http.MethodPost, "/api/usage", nil)
	req.Header.Set("Authorization", "Bearer "+selfKey)
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control = %q, want no-store", got)
	}

	body := rr.Body.String()
	if strings.Contains(body, "internal@example.com") || strings.Contains(body, "secret-auth-index") {
		t.Fatalf("response leaked internal usage detail: %s", body)
	}
	if strings.Contains(body, otherKey) || strings.Contains(body, "other@example.com") || strings.Contains(body, "other-auth-index") {
		t.Fatalf("response leaked other tenant detail: %s", body)
	}

	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode payload: %v", err)
	}
	if got := payload["user"]; got != "self" {
		t.Fatalf("user = %v, want self", got)
	}
	if got := payload["balance_available"]; got != false {
		t.Fatalf("balance_available = %v, want false", got)
	}
	usageMap, ok := payload["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage missing or wrong type: %#v", payload["usage"])
	}
	if got := int64(usageMap["total_requests"].(float64)); got != 2 {
		t.Fatalf("usage.total_requests = %d, want 2", got)
	}
	if got := int64(usageMap["failure_count"].(float64)); got != 1 {
		t.Fatalf("usage.failure_count = %d, want 1", got)
	}
	if got := int64(usageMap["total_tokens"].(float64)); got != 35 {
		t.Fatalf("usage.total_tokens = %d, want 35", got)
	}
	models, ok := usageMap["models"].(map[string]any)
	if !ok || len(models) != 2 {
		t.Fatalf("usage.models = %#v, want 2 models", usageMap["models"])
	}
}

func TestUserUsageEndpointAvailableUnderV1Namespace(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	selfKey := "sk-team-self-1234567890abcdef1234567890abcdef"
	cfg := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{selfKey},
		},
		Port:                   0,
		AuthDir:                authDir,
		Debug:                  true,
		LoggingToFile:          false,
		UsageStatisticsEnabled: true,
	}

	authManager := auth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()
	configPath := filepath.Join(tmpDir, "config.yaml")
	server := NewServer(cfg, authManager, accessManager, configPath)

	stats := internalusage.NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      selfKey,
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 3, 29, 12, 0, 0, 0, time.FixedZone("CST", 8*3600)),
		Detail: coreusage.Detail{
			TotalTokens: 30,
		},
	})
	server.mgmt.SetUsageStatistics(stats)

	req := httptest.NewRequest(http.MethodPost, "/v1/api/usage", nil)
	req.Header.Set("Authorization", "Bearer "+selfKey)
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "\"total_requests\":1") {
		t.Fatalf("response missing total_requests: %s", rr.Body.String())
	}
}

func TestOAuthQuotaEndpointRateLimitedUnderV1Namespace(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	selfKey := "sk-team-quota-1234567890abcdef1234567890abcdef"
	cfg := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{selfKey},
		},
		Port:          0,
		AuthDir:       authDir,
		Debug:         true,
		LoggingToFile: false,
	}

	authManager := auth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()
	configPath := filepath.Join(tmpDir, "config.yaml")
	server := NewServer(cfg, authManager, accessManager, configPath)

	makeRequest := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/api/oauth-quota?probe=1", nil)
		req.Header.Set("Authorization", "Bearer "+selfKey)
		req.RemoteAddr = "198.51.100.10:12345"
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		return rr
	}

	for i := 0; i < 2; i++ {
		rr := makeRequest()
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d unexpected status: got %d want %d; body=%s", i+1, rr.Code, http.StatusOK, rr.Body.String())
		}
	}

	rr := makeRequest()
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("third request unexpected status: got %d want %d; body=%s", rr.Code, http.StatusTooManyRequests, rr.Body.String())
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Fatalf("expected Retry-After header on rate-limited response, got headers=%v", rr.Header())
	}
	if !strings.Contains(rr.Body.String(), "\"policy\":\"oauth-quota-probe\"") {
		t.Fatalf("response missing rate-limit policy: %s", rr.Body.String())
	}
}
