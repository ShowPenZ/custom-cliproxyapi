package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// Token is the repository-facing representation of a Kiro auth file.
type Token struct {
	ID           string
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	LastVerified time.Time
	ClientID     string
	ClientSecret string
	AuthMethod   string
	Provider     string
	StartURL     string
	Region       string
}

// TokenRepository stores Kiro tokens for background refresh implementations.
type TokenRepository interface {
	FindOldestUnverified(limit int) []*Token
	UpdateToken(token *Token) error
}

// FileTokenRepository implements TokenRepository using the auth directory.
type FileTokenRepository struct {
	mu      sync.RWMutex
	baseDir string
}

// NewFileTokenRepository creates a file-backed Kiro token repository.
func NewFileTokenRepository(baseDir string) *FileTokenRepository {
	return &FileTokenRepository{baseDir: baseDir}
}

// SetBaseDir updates the auth directory used by the repository.
func (r *FileTokenRepository) SetBaseDir(dir string) {
	r.mu.Lock()
	r.baseDir = strings.TrimSpace(dir)
	r.mu.Unlock()
}

// FindOldestUnverified finds Kiro tokens that are expired or close to expiry.
func (r *FileTokenRepository) FindOldestUnverified(limit int) []*Token {
	r.mu.RLock()
	baseDir := r.baseDir
	r.mu.RUnlock()

	if baseDir == "" {
		log.Debug("token repository: base directory not configured")
		return nil
	}

	var tokens []*Token
	err := filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasPrefix(name, "kiro-") || !strings.HasSuffix(strings.ToLower(name), ".json") {
			return nil
		}

		token, err := r.readTokenFile(path)
		if err != nil {
			log.Debugf("token repository: failed to read token file %s: %v", path, err)
			return nil
		}
		if token != nil && token.RefreshToken != "" && (token.ExpiresAt.IsZero() || time.Until(token.ExpiresAt) < 5*time.Minute) {
			tokens = append(tokens, token)
		}
		return nil
	})
	if err != nil {
		log.Warnf("token repository: error walking directory: %v", err)
	}

	sort.Slice(tokens, func(i, j int) bool {
		return tokens[i].LastVerified.Before(tokens[j].LastVerified)
	})
	if limit > 0 && len(tokens) > limit {
		tokens = tokens[:limit]
	}
	return tokens
}

// UpdateToken updates token fields while preserving unrelated auth metadata.
func (r *FileTokenRepository) UpdateToken(token *Token) error {
	if token == nil {
		return fmt.Errorf("token repository: token is nil")
	}

	r.mu.RLock()
	baseDir := r.baseDir
	r.mu.RUnlock()
	if baseDir == "" {
		return fmt.Errorf("token repository: base directory not configured")
	}

	filePath := filepath.Join(baseDir, token.ID)
	if !strings.HasSuffix(strings.ToLower(filePath), ".json") {
		filePath += ".json"
	}

	existingData := make(map[string]any)
	if data, err := os.ReadFile(filePath); err == nil {
		_ = json.Unmarshal(data, &existingData)
	}

	existingData["access_token"] = token.AccessToken
	existingData["refresh_token"] = token.RefreshToken
	existingData["last_refresh"] = time.Now().Format(time.RFC3339)
	if !token.ExpiresAt.IsZero() {
		existingData["expires_at"] = token.ExpiresAt.Format(time.RFC3339)
	}
	if token.ClientID != "" {
		existingData["client_id"] = token.ClientID
	}
	if token.ClientSecret != "" {
		existingData["client_secret"] = token.ClientSecret
	}
	if token.AuthMethod != "" {
		existingData["auth_method"] = token.AuthMethod
	}
	if token.Region != "" {
		existingData["region"] = token.Region
	}
	if token.StartURL != "" {
		existingData["start_url"] = token.StartURL
	}

	raw, err := json.MarshalIndent(existingData, "", "  ")
	if err != nil {
		return fmt.Errorf("token repository: marshal failed: %w", err)
	}

	tmpPath := filePath + ".tmp"
	if err := os.WriteFile(tmpPath, raw, 0o600); err != nil {
		return fmt.Errorf("token repository: write temp file failed: %w", err)
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("token repository: rename failed: %w", err)
	}

	log.Debugf("token repository: updated token %s", token.ID)
	return nil
}

func (r *FileTokenRepository) readTokenFile(path string) (*Token, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var metadata map[string]any
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, err
	}
	tokenType, _ := metadata["type"].(string)
	if tokenType != "kiro" {
		return nil, nil
	}

	authMethod, _ := metadata["auth_method"].(string)
	authMethod = strings.ToLower(authMethod)
	if authMethod != "idc" && authMethod != "builder-id" && authMethod != "kiro-cli" {
		return nil, nil
	}

	token := &Token{
		ID:         filepath.Base(path),
		AuthMethod: authMethod,
	}
	token.AccessToken, _ = metadata["access_token"].(string)
	token.RefreshToken, _ = metadata["refresh_token"].(string)
	token.ClientID, _ = metadata["client_id"].(string)
	token.ClientSecret, _ = metadata["client_secret"].(string)
	token.Region, _ = metadata["region"].(string)
	token.StartURL, _ = metadata["start_url"].(string)
	token.Provider, _ = metadata["provider"].(string)

	if expiresAtStr, ok := metadata["expires_at"].(string); ok && expiresAtStr != "" {
		if t, err := time.Parse(time.RFC3339, expiresAtStr); err == nil {
			token.ExpiresAt = t
		}
	}
	if lastRefreshStr, ok := metadata["last_refresh"].(string); ok && lastRefreshStr != "" {
		if t, err := time.Parse(time.RFC3339, lastRefreshStr); err == nil {
			token.LastVerified = t
		}
	}
	return token, nil
}

// ListKiroTokens lists all repository-readable Kiro tokens.
func (r *FileTokenRepository) ListKiroTokens(ctx context.Context) ([]*Token, error) {
	r.mu.RLock()
	baseDir := r.baseDir
	r.mu.RUnlock()

	if baseDir == "" {
		return nil, fmt.Errorf("token repository: base directory not configured")
	}

	var tokens []*Token
	err := filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasPrefix(name, "kiro-") || !strings.HasSuffix(strings.ToLower(name), ".json") {
			return nil
		}
		token, err := r.readTokenFile(path)
		if err != nil {
			return nil
		}
		if token != nil {
			tokens = append(tokens, token)
		}
		return nil
	})
	return tokens, err
}
