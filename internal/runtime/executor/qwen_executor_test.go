package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func TestQwenExecutorParseSuffix(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		wantBase  string
		wantLevel string
	}{
		{"no suffix", "qwen-max", "qwen-max", ""},
		{"with level suffix", "qwen-max(high)", "qwen-max", "high"},
		{"with budget suffix", "qwen-max(16384)", "qwen-max", "16384"},
		{"complex model name", "qwen-plus-latest(medium)", "qwen-plus-latest", "medium"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := thinking.ParseSuffix(tt.model)
			if result.ModelName != tt.wantBase {
				t.Errorf("ParseSuffix(%q).ModelName = %q, want %q", tt.model, result.ModelName, tt.wantBase)
			}
		})
	}
}

func TestQwenExecutorExecute_429RetryAfterHeaderPropagatesToStatusErr(t *testing.T) {
	qwenRateLimiter.Lock()
	qwenRateLimiter.requests = make(map[string][]time.Time)
	qwenRateLimiter.Unlock()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limit_exceeded","message":"rate limited","type":"rate_limit_exceeded"}}`))
	}))
	defer srv.Close()

	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "auth-test",
		Provider: "qwen",
		Attributes: map[string]string{
			"base_url": srv.URL + "/v1",
		},
		Metadata: map[string]any{
			"access_token": "test-token",
		},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "qwen-max",
		Payload: []byte(`{"model":"qwen-max","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err == nil {
		t.Fatal("Execute() expected error, got nil")
	}
	status, ok := err.(statusErr)
	if !ok {
		t.Fatalf("Execute() error type = %T, want statusErr", err)
	}
	if status.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("Execute() status code = %d, want %d", status.StatusCode(), http.StatusTooManyRequests)
	}
	if status.RetryAfter() == nil {
		t.Fatal("Execute() RetryAfter is nil, want non-nil")
	}
	if got := *status.RetryAfter(); got != 2*time.Second {
		t.Fatalf("Execute() RetryAfter = %v, want %v", got, 2*time.Second)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("upstream calls = %d, want 1", atomic.LoadInt32(&calls))
	}
}

func TestQwenExecutorExecuteStream_429RetryAfterHeaderPropagatesToStatusErr(t *testing.T) {
	qwenRateLimiter.Lock()
	qwenRateLimiter.requests = make(map[string][]time.Time)
	qwenRateLimiter.Unlock()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limit_exceeded","message":"rate limited","type":"rate_limit_exceeded"}}`))
	}))
	defer srv.Close()

	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "auth-test",
		Provider: "qwen",
		Attributes: map[string]string{
			"base_url": srv.URL + "/v1",
		},
		Metadata: map[string]any{
			"access_token": "test-token",
		},
	}

	_, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "qwen-max",
		Payload: []byte(`{"model":"qwen-max","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err == nil {
		t.Fatal("ExecuteStream() expected error, got nil")
	}
	status, ok := err.(statusErr)
	if !ok {
		t.Fatalf("ExecuteStream() error type = %T, want statusErr", err)
	}
	if status.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("ExecuteStream() status code = %d, want %d", status.StatusCode(), http.StatusTooManyRequests)
	}
	if status.RetryAfter() == nil {
		t.Fatal("ExecuteStream() RetryAfter is nil, want non-nil")
	}
	if got := *status.RetryAfter(); got != 2*time.Second {
		t.Fatalf("ExecuteStream() RetryAfter = %v, want %v", got, 2*time.Second)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("upstream calls = %d, want 1", atomic.LoadInt32(&calls))
	}
}

func TestQwenExecutorExecute_429QuotaExhausted_DisableCoolingSetsDefaultRetryAfter(t *testing.T) {
	qwenRateLimiter.Lock()
	qwenRateLimiter.requests = make(map[string][]time.Time)
	qwenRateLimiter.Unlock()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"quota_exceeded","message":"quota exceeded","type":"quota_exceeded"}}`))
	}))
	defer srv.Close()

	exec := NewQwenExecutor(&config.Config{DisableCooling: true})
	auth := &cliproxyauth.Auth{
		ID:       "auth-test",
		Provider: "qwen",
		Attributes: map[string]string{
			"base_url": srv.URL + "/v1",
		},
		Metadata: map[string]any{
			"access_token": "test-token",
		},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "qwen-max",
		Payload: []byte(`{"model":"qwen-max","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err == nil {
		t.Fatal("Execute() expected error, got nil")
	}
	status, ok := err.(statusErr)
	if !ok {
		t.Fatalf("Execute() error type = %T, want statusErr", err)
	}
	if status.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("Execute() status code = %d, want %d", status.StatusCode(), http.StatusTooManyRequests)
	}
	if status.RetryAfter() == nil {
		t.Fatal("Execute() RetryAfter is nil, want non-nil")
	}
	if got := *status.RetryAfter(); got != time.Second {
		t.Fatalf("Execute() RetryAfter = %v, want %v", got, time.Second)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("upstream calls = %d, want 1", atomic.LoadInt32(&calls))
	}
}
