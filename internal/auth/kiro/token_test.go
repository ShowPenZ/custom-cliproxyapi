package kiro

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractEmailFromJWT(t *testing.T) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"user@example.com"}`))
	token := header + "." + payload + "."

	if got := ExtractEmailFromJWT(token); got != "user@example.com" {
		t.Fatalf("ExtractEmailFromJWT() = %q, want user@example.com", got)
	}
}

func TestSanitizeEmailForFilename(t *testing.T) {
	got := SanitizeEmailForFilename("../User+Name@example.com")
	if got != "User_Name_at_example.com" {
		t.Fatalf("SanitizeEmailForFilename() = %q", got)
	}
}

func TestLoadKiroTokenFromPath_LoadsDeviceRegistration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cacheDir := filepath.Join(home, ".aws", "sso", "cache")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "hash123.json"), []byte(`{"clientId":"cid","clientSecret":"secret"}`), 0o600); err != nil {
		t.Fatalf("write device registration: %v", err)
	}
	tokenPath := filepath.Join(home, "kiro-token.json")
	raw, _ := json.Marshal(KiroTokenData{
		AccessToken:  "access",
		RefreshToken: "refresh",
		ClientIDHash: "hash123",
		AuthMethod:   "IdC",
	})
	if err := os.WriteFile(tokenPath, raw, 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}

	token, err := LoadKiroTokenFromPath(tokenPath)
	if err != nil {
		t.Fatalf("LoadKiroTokenFromPath() error = %v", err)
	}
	if token.AuthMethod != "idc" {
		t.Fatalf("AuthMethod = %q, want idc", token.AuthMethod)
	}
	if token.ClientID != "cid" || token.ClientSecret != "secret" {
		t.Fatalf("device credentials = %q/%q", token.ClientID, token.ClientSecret)
	}
	if token.Region != DefaultKiroRegion {
		t.Fatalf("Region = %q, want %q", token.Region, DefaultKiroRegion)
	}
}
