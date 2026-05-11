package kiro

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestRegisterClientForAuthCodeWithIDC(t *testing.T) {
	var captured struct {
		Path    string
		Header  http.Header
		Payload map[string]any
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.Path = r.URL.Path
		captured.Header = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured.Payload)
		_ = json.NewEncoder(w).Encode(RegisterClientResponse{
			ClientID:     "client-id",
			ClientSecret: "client-secret",
		})
	}))
	defer ts.Close()

	client := NewSSOOIDCClient(&config.Config{
		OAuthEndpointOverrides: map[string]config.OAuthEndpointConfig{
			"kiro": {ApiBaseURL: ts.URL},
		},
	})
	resp, err := client.RegisterClientForAuthCodeWithIDC(context.Background(), "http://127.0.0.1:19877/oauth/callback", "https://example.awsapps.com/start", "us-west-2")
	if err != nil {
		t.Fatalf("RegisterClientForAuthCodeWithIDC() error = %v", err)
	}

	if captured.Path != "/client/register" {
		t.Fatalf("path = %q, want /client/register", captured.Path)
	}
	if captured.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", captured.Header.Get("Content-Type"))
	}
	if !strings.Contains(captured.Header.Get("User-Agent"), "KiroIDE") {
		t.Fatalf("User-Agent = %q, want KiroIDE fingerprint", captured.Header.Get("User-Agent"))
	}
	if captured.Payload["issuerUrl"] != "https://example.awsapps.com/start" {
		t.Fatalf("issuerUrl = %v", captured.Payload["issuerUrl"])
	}
	if got := captured.Payload["grantTypes"].([]any); got[0] != "authorization_code" || got[1] != "refresh_token" {
		t.Fatalf("grantTypes = %v", got)
	}
	if got := captured.Payload["redirectUris"].([]any); got[0] != "http://127.0.0.1:19877/oauth/callback" {
		t.Fatalf("redirectUris = %v", got)
	}
	if resp.ClientID != "client-id" || resp.ClientSecret != "client-secret" {
		t.Fatalf("response = %#v", resp)
	}
}

func TestCreateTokenWithAuthCodeAndRegion(t *testing.T) {
	var captured map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			t.Fatalf("path = %q, want /token", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		_ = json.NewEncoder(w).Encode(CreateTokenResponse{
			AccessToken:  "access",
			RefreshToken: "refresh",
			ExpiresIn:    3600,
		})
	}))
	defer ts.Close()

	client := NewSSOOIDCClient(&config.Config{
		OAuthEndpointOverrides: map[string]config.OAuthEndpointConfig{
			"kiro": {ApiBaseURL: ts.URL},
		},
	})
	resp, err := client.CreateTokenWithAuthCodeAndRegion(context.Background(), "client-id", "secret", "code", "verifier", "http://127.0.0.1/callback", "us-west-2")
	if err != nil {
		t.Fatalf("CreateTokenWithAuthCodeAndRegion() error = %v", err)
	}

	if captured["grantType"] != "authorization_code" {
		t.Fatalf("grantType = %q, want authorization_code", captured["grantType"])
	}
	if captured["codeVerifier"] != "verifier" || captured["redirectUri"] != "http://127.0.0.1/callback" {
		t.Fatalf("payload = %#v", captured)
	}
	if resp.AccessToken != "access" || resp.RefreshToken != "refresh" {
		t.Fatalf("response = %#v", resp)
	}
}

func TestBuildAuthorizationURL(t *testing.T) {
	authURL := buildAuthorizationURL("https://oidc.example.com/", "client", "http://127.0.0.1/cb", "scope:a,scope:b", "state", "challenge")
	if !strings.HasPrefix(authURL, "https://oidc.example.com/authorize?") {
		t.Fatalf("authURL = %q", authURL)
	}
	for _, want := range []string{
		"response_type=code",
		"client_id=client",
		"redirect_uri=http%3A%2F%2F127.0.0.1%2Fcb",
		"scopes=scope%3Aa%2Cscope%3Ab",
		"state=state",
		"code_challenge=challenge",
		"code_challenge_method=S256",
	} {
		if !strings.Contains(authURL, want) {
			t.Fatalf("authURL = %q, missing %s", authURL, want)
		}
	}
}
