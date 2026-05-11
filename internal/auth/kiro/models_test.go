package kiro

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestKiroAuthListAvailableModels(t *testing.T) {
	var capturedPath string
	var capturedQuery url.Values
	var capturedAuth string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedQuery = r.URL.Query()
		capturedAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{
					"modelId":        "claude-sonnet-4.5",
					"modelName":      "Claude Sonnet 4.5",
					"description":    "test model",
					"rateMultiplier": 1.5,
					"rateUnit":       "credit",
					"tokenLimits": map[string]any{
						"maxInputTokens": 200000,
					},
				},
			},
		})
	}))
	defer ts.Close()

	client := &KiroAuth{httpClient: ts.Client()}
	client.httpClient.Transport = rewriteKiroModelTransport{
		base:      ts.Client().Transport,
		targetURL: ts.URL,
	}

	models, err := client.ListAvailableModels(context.Background(), &KiroTokenData{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		ClientID:     "client-id",
		ProfileArn:   "arn:aws:codewhisperer:us-east-1:123456789012:profile/test",
		AuthMethod:   "idc",
	})
	if err != nil {
		t.Fatalf("ListAvailableModels() error = %v", err)
	}
	if capturedPath != "/ListAvailableModels" {
		t.Fatalf("path = %q, want /ListAvailableModels", capturedPath)
	}
	if capturedQuery.Get("origin") != KiroOriginAIEditor {
		t.Fatalf("origin = %q, want %q", capturedQuery.Get("origin"), KiroOriginAIEditor)
	}
	if capturedQuery.Get("profileArn") == "" {
		t.Fatal("profileArn query is empty")
	}
	if capturedAuth != "Bearer access-token" {
		t.Fatalf("Authorization = %q", capturedAuth)
	}
	if len(models) != 1 {
		t.Fatalf("models len = %d, want 1", len(models))
	}
	if models[0].ModelID != "claude-sonnet-4.5" || models[0].MaxInputTokens != 200000 {
		t.Fatalf("model = %#v", models[0])
	}
}

type rewriteKiroModelTransport struct {
	base      http.RoundTripper
	targetURL string
}

func (t rewriteKiroModelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target, err := url.Parse(t.targetURL)
	if err != nil {
		return nil, err
	}
	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
	if t.base == nil {
		t.base = http.DefaultTransport
	}
	return t.base.RoundTrip(req)
}
