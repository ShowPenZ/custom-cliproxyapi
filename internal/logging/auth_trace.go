package logging

import (
	"context"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const authRouteResponseKey = "__auth_route_response__"

type authRouteTrace struct {
	builder strings.Builder
}

// AppendAuthRouteResponse records an auth routing outcome line for request-log payloads.
// The line is appended to a per-request "AUTH ROUTING RESULT" section stored on Gin context.
func AppendAuthRouteResponse(ctx context.Context, line string) {
	ginCtx, _ := ctx.Value("gin").(*gin.Context)
	if ginCtx == nil {
		return
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}

	trace := ensureAuthRouteTrace(ginCtx)
	if trace == nil {
		return
	}
	if trace.builder.Len() == 0 {
		trace.builder.WriteString("=== AUTH ROUTING RESULT ===\n")
		trace.builder.WriteString("Timestamp: ")
		trace.builder.WriteString(time.Now().Format(time.RFC3339Nano))
		trace.builder.WriteString("\n\n")
	}
	trace.builder.WriteString(line)
	trace.builder.WriteString("\n")
}

// GetAuthRouteResponse returns the aggregated auth routing result payload for the request.
func GetAuthRouteResponse(c *gin.Context) []byte {
	if c == nil {
		return nil
	}
	value, exists := c.Get(authRouteResponseKey)
	if !exists {
		return nil
	}
	trace, ok := value.(*authRouteTrace)
	if !ok || trace == nil || trace.builder.Len() == 0 {
		return nil
	}
	return []byte(trace.builder.String())
}

func ensureAuthRouteTrace(c *gin.Context) *authRouteTrace {
	if c == nil {
		return nil
	}
	if value, exists := c.Get(authRouteResponseKey); exists {
		if trace, ok := value.(*authRouteTrace); ok && trace != nil {
			return trace
		}
	}
	trace := &authRouteTrace{}
	c.Set(authRouteResponseKey, trace)
	return trace
}
