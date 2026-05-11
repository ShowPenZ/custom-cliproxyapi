package management

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestKiroRuntimeAuthEntryDoesNotExposeSecrets(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "kiro-user@example.com.json",
		Provider: "kiro",
		FileName: "kiro-user@example.com.json",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type":          "kiro",
			"email":         "user@example.com",
			"access_token":  "access-secret",
			"refresh_token": "refresh-secret",
			"client_secret": "client-secret",
			"profile_arn":   "arn:aws:sso:::permissionSet/example",
		},
		Attributes: map[string]string{
			"access_token":  "access-secret",
			"refresh_token": "refresh-secret",
			"client_secret": "client-secret",
		},
	}

	entry := buildRuntimeAuthEntry(auth, nil, time.Now())
	assertKiroManagementEntrySafe(t, entry)
}

func TestKiroAuthFileEntryDoesNotExposeSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kiro-user@example.com.json")
	if err := os.WriteFile(path, []byte(`{"type":"kiro"}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	auth := &coreauth.Auth{
		ID:       "kiro-user@example.com.json",
		Provider: "kiro",
		FileName: "kiro-user@example.com.json",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type":          "kiro",
			"email":         "user@example.com",
			"access_token":  "access-secret",
			"refresh_token": "refresh-secret",
			"client_secret": "client-secret",
			"auth_method":   "kiro-cli",
		},
		Attributes: map[string]string{
			"path":          path,
			"access_token":  "access-secret",
			"refresh_token": "refresh-secret",
			"client_secret": "client-secret",
		},
	}

	entry := (&Handler{}).buildAuthFileEntry(auth)
	assertKiroManagementEntrySafe(t, entry)
}

func assertKiroManagementEntrySafe(t *testing.T, entry map[string]any) {
	t.Helper()

	if entry == nil {
		t.Fatal("expected management entry")
	}
	if got := entry["provider"]; got != "kiro" {
		t.Fatalf("provider = %v, want kiro", got)
	}
	if got := entry["email"]; got != "user@example.com" {
		t.Fatalf("email = %v, want user@example.com", got)
	}
	if got := entry["account"]; got != "user@example.com" {
		t.Fatalf("account = %v, want user@example.com", got)
	}
	for _, key := range []string{"access_token", "refresh_token", "client_secret", "profile_arn", "auth_method"} {
		if _, ok := entry[key]; ok {
			t.Fatalf("management entry exposed sensitive/internal key %q: %#v", key, entry[key])
		}
	}
}
