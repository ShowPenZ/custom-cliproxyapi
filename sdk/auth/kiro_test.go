package auth

import (
	"testing"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kiro"
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
