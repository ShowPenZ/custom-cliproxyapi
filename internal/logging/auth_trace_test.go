package logging

import (
	"context"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAppendAuthRouteResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	ctx := context.WithValue(context.Background(), "gin", c)

	AppendAuthRouteResponse(ctx, "Attempt 1 failed provider=codex auth_id=auth-a error=context canceled")
	AppendAuthRouteResponse(ctx, "Attempt 2 succeeded provider=codex auth_id=auth-b")

	payload := string(GetAuthRouteResponse(c))
	if !strings.Contains(payload, "=== AUTH ROUTING RESULT ===") {
		t.Fatalf("payload missing section header: %q", payload)
	}
	if !strings.Contains(payload, "Attempt 1 failed provider=codex auth_id=auth-a error=context canceled") {
		t.Fatalf("payload missing first auth trace line: %q", payload)
	}
	if !strings.Contains(payload, "Attempt 2 succeeded provider=codex auth_id=auth-b") {
		t.Fatalf("payload missing success auth trace line: %q", payload)
	}
}
