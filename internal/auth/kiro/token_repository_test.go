package kiro

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileTokenRepositoryFindAndUpdate(t *testing.T) {
	dir := t.TempDir()
	expired := time.Now().Add(-time.Minute).Format(time.RFC3339)
	future := time.Now().Add(time.Hour).Format(time.RFC3339)

	writeJSON(t, filepath.Join(dir, "kiro-expired.json"), map[string]any{
		"type":          "kiro",
		"auth_method":   "IdC",
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
		"expires_at":    expired,
		"last_refresh":  "2026-01-01T00:00:00Z",
		"client_id":     "client",
		"client_secret": "secret",
		"region":        "us-east-1",
		"start_url":     "https://example.awsapps.com/start",
		"keep":          "value",
	})
	writeJSON(t, filepath.Join(dir, "kiro-fresh.json"), map[string]any{
		"type":          "kiro",
		"auth_method":   "builder-id",
		"access_token":  "fresh-access",
		"refresh_token": "fresh-refresh",
		"expires_at":    future,
		"last_refresh":  "2026-01-02T00:00:00Z",
	})
	writeJSON(t, filepath.Join(dir, "codex.json"), map[string]any{
		"type":          "codex",
		"refresh_token": "refresh",
	})

	repo := NewFileTokenRepository(dir)
	tokens := repo.FindOldestUnverified(10)
	if len(tokens) != 1 {
		t.Fatalf("FindOldestUnverified() returned %d tokens, want 1", len(tokens))
	}
	token := tokens[0]
	if token.ID != "kiro-expired.json" || token.AuthMethod != "idc" {
		t.Fatalf("token = %#v", token)
	}

	token.AccessToken = "new-access"
	token.RefreshToken = "new-refresh"
	token.ExpiresAt = time.Now().Add(30 * time.Minute).UTC()
	if err := repo.UpdateToken(token); err != nil {
		t.Fatalf("UpdateToken() error = %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "kiro-expired.json"))
	if err != nil {
		t.Fatalf("read updated token: %v", err)
	}
	var updated map[string]any
	if err := json.Unmarshal(raw, &updated); err != nil {
		t.Fatalf("unmarshal updated token: %v", err)
	}
	if updated["access_token"] != "new-access" || updated["refresh_token"] != "new-refresh" {
		t.Fatalf("updated token = %#v", updated)
	}
	if updated["keep"] != "value" {
		t.Fatalf("preserved metadata keep = %v", updated["keep"])
	}

	listed, err := repo.ListKiroTokens(context.Background())
	if err != nil {
		t.Fatalf("ListKiroTokens() error = %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("ListKiroTokens() returned %d tokens, want 2", len(listed))
	}
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
