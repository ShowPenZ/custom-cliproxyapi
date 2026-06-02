package auth

import (
	"testing"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestKiroCreateAuthRecord(t *testing.T) {
	authenticator := &KiroAuthenticator{}
	expiresAt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	record, err := authenticator.createAuthRecord(&kiroauth.KiroTokenData{
		AccessToken:  "access",
		RefreshToken: "refresh",
		ProfileArn:   "arn:aws:codewhisperer:us-east-1:123:profile/profile-id",
		ExpiresAt:    expiresAt,
		AuthMethod:   "kiro-cli",
		Provider:     "Kiro CLI",
		Email:        "user@example.com",
		Region:       "us-east-1",
	}, "cli")
	if err != nil {
		t.Fatalf("createAuthRecord() error = %v", err)
	}
	if record.Provider != "kiro" {
		t.Fatalf("Provider = %q, want kiro", record.Provider)
	}
	if record.FileName != "kiro-cli-user_at_example.com.json" {
		t.Fatalf("FileName = %q", record.FileName)
	}
	if record.Metadata["type"] != "kiro" || record.Metadata["refresh_token"] != "refresh" {
		t.Fatalf("unexpected metadata: %#v", record.Metadata)
	}
	if record.Attributes["source"] != "cli" || record.Attributes["region"] != "us-east-1" {
		t.Fatalf("unexpected attributes: %#v", record.Attributes)
	}
	if record.NextRefreshAfter.IsZero() {
		t.Fatal("expected NextRefreshAfter to be set")
	}
}

func TestExtractKiroIdentifierFallbacks(t *testing.T) {
	if got := extractKiroIdentifier("", "arn:aws:sso:::permissionSet/profile-id", "client-id"); got != "profile-id" {
		t.Fatalf("profile fallback = %q", got)
	}
	if got := extractKiroIdentifier("", "", "client/id"); got != "client_id" {
		t.Fatalf("client fallback = %q", got)
	}
}

func TestKiroCreateAPIKeyAuthRecord(t *testing.T) {
	authenticator := &KiroAuthenticator{}
	record, err := authenticator.LoginWithAPIKey(nil, &config.Config{}, &LoginOptions{
		Metadata: map[string]string{
			"api-key": "ksk_test_secret",
			"label":   "api-key",
		},
	})
	if err != nil {
		t.Fatalf("LoginWithAPIKey() error = %v", err)
	}
	if record.Provider != "kiro" {
		t.Fatalf("Provider = %q, want kiro", record.Provider)
	}
	if record.Metadata["auth_method"] != "api-key" {
		t.Fatalf("auth_method = %v, want api-key", record.Metadata["auth_method"])
	}
	if record.Metadata["api_key"] != "ksk_test_secret" || record.Metadata["access_token"] != "ksk_test_secret" {
		t.Fatalf("unexpected api key metadata: %#v", record.Metadata)
	}
	if _, ok := record.Metadata["refresh_token"]; ok {
		t.Fatalf("api key auth should not store refresh_token: %#v", record.Metadata)
	}
	if _, ok := record.Metadata["expires_at"]; ok {
		t.Fatalf("api key auth should not store expires_at: %#v", record.Metadata)
	}
	if record.Attributes["api_key"] != "ksk_test_secret" {
		t.Fatalf("api_key attribute = %q", record.Attributes["api_key"])
	}
	if _, account := record.AccountInfo(); account != "ksk_test_secret" {
		t.Fatalf("AccountInfo account = %q, want api key", account)
	}
}
