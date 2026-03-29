package middleware

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
)

func TestExtractRequestBodyPrefersOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{
		requestInfo: &RequestInfo{Body: []byte("original-body")},
	}

	body := wrapper.extractRequestBody(c)
	if string(body) != "original-body" {
		t.Fatalf("request body = %q, want %q", string(body), "original-body")
	}

	c.Set(requestBodyOverrideContextKey, []byte("override-body"))
	body = wrapper.extractRequestBody(c)
	if string(body) != "override-body" {
		t.Fatalf("request body = %q, want %q", string(body), "override-body")
	}
}

func TestExtractRequestBodySupportsStringOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{}
	c.Set(requestBodyOverrideContextKey, "override-as-string")

	body := wrapper.extractRequestBody(c)
	if string(body) != "override-as-string" {
		t.Fatalf("request body = %q, want %q", string(body), "override-as-string")
	}
}

func TestExtractAPIResponseIncludesAuthRouteTrace(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	ctx := context.WithValue(context.Background(), "gin", c)

	logging.AppendAuthRouteResponse(ctx, "Attempt 1 failed provider=codex auth_id=auth-a error=context canceled")
	c.Set("API_RESPONSE", []byte("=== API RESPONSE 1 ===\nStatus: 200\n"))

	wrapper := &ResponseWriterWrapper{}
	payload := string(wrapper.extractAPIResponse(c))

	if !strings.Contains(payload, "=== AUTH ROUTING RESULT ===") {
		t.Fatalf("payload missing auth routing trace: %q", payload)
	}
	if !strings.Contains(payload, "Attempt 1 failed provider=codex auth_id=auth-a error=context canceled") {
		t.Fatalf("payload missing auth routing failure line: %q", payload)
	}
	if !strings.Contains(payload, "=== API RESPONSE 1 ===") {
		t.Fatalf("payload missing upstream api response: %q", payload)
	}
}
