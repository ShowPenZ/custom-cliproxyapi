// Package kiro provides token helpers and OAuth flows for Kiro/AWS Q Developer.
package kiro

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// KiroTokenData holds OAuth token information used by Kiro.
type KiroTokenData struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	APIKey       string `json:"apiKey,omitempty"`
	ProfileArn   string `json:"profileArn"`
	ExpiresAt    string `json:"expiresAt"`
	AuthMethod   string `json:"authMethod"`
	Provider     string `json:"provider"`
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
	ClientIDHash string `json:"clientIdHash,omitempty"`
	Email        string `json:"email,omitempty"`
	StartURL     string `json:"startUrl,omitempty"`
	Region       string `json:"region,omitempty"`
}

// KiroTokenResponse is returned by Kiro OAuth token endpoints.
type KiroTokenResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ProfileArn   string `json:"profileArn"`
	ExpiresIn    int    `json:"expiresIn"`
}

const (
	// KiroIDETokenFile is the default Kiro IDE token path relative to the home directory.
	KiroIDETokenFile = ".aws/sso/cache/kiro-auth-token.json"

	// DefaultKiroRegion is used when token metadata does not carry an AWS region.
	DefaultKiroRegion = "us-east-1"
)

// LoadKiroIDEToken loads token data from the default Kiro IDE token file.
func LoadKiroIDEToken() (*KiroTokenData, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}
	return LoadKiroTokenFromPath(filepath.Join(homeDir, KiroIDETokenFile))
}

// LoadKiroTokenFromPath loads token data from a Kiro token file.
func LoadKiroTokenFromPath(tokenPath string) (*KiroTokenData, error) {
	tokenPath = strings.TrimSpace(tokenPath)
	if tokenPath == "" {
		return nil, fmt.Errorf("kiro token path is empty")
	}
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read Kiro token file (%s): %w", tokenPath, err)
	}
	var token KiroTokenData
	if err = json.Unmarshal(data, &token); err != nil {
		return nil, fmt.Errorf("failed to parse Kiro token file: %w", err)
	}
	token.normalize()
	if token.AccessToken == "" && token.APIKey != "" {
		token.AccessToken = token.APIKey
	}
	if token.AccessToken == "" {
		return nil, fmt.Errorf("access token is empty in Kiro token file")
	}
	if token.Email == "" {
		token.Email = ExtractEmailFromJWT(token.AccessToken)
	}
	if token.ClientIDHash != "" && (token.ClientID == "" || token.ClientSecret == "") {
		if clientID, clientSecret, errLoad := LoadDeviceRegistrationCredentials(token.ClientIDHash); errLoad == nil {
			token.ClientID = clientID
			token.ClientSecret = clientSecret
		}
	}
	return &token, nil
}

func (t *KiroTokenData) normalize() {
	if t == nil {
		return
	}
	t.AccessToken = strings.TrimSpace(t.AccessToken)
	t.RefreshToken = strings.TrimSpace(t.RefreshToken)
	t.APIKey = strings.TrimSpace(t.APIKey)
	t.ProfileArn = strings.TrimSpace(t.ProfileArn)
	t.ExpiresAt = strings.TrimSpace(t.ExpiresAt)
	t.AuthMethod = strings.ToLower(strings.TrimSpace(t.AuthMethod))
	t.Provider = strings.TrimSpace(t.Provider)
	t.ClientID = strings.TrimSpace(t.ClientID)
	t.ClientSecret = strings.TrimSpace(t.ClientSecret)
	t.ClientIDHash = strings.TrimSpace(t.ClientIDHash)
	t.Email = strings.TrimSpace(t.Email)
	t.StartURL = strings.TrimSpace(t.StartURL)
	t.Region = strings.TrimSpace(t.Region)
	if t.Region == "" {
		t.Region = DefaultKiroRegion
	}
}

// LoadDeviceRegistrationCredentials loads AWS SSO client credentials by clientIdHash.
func LoadDeviceRegistrationCredentials(clientIDHash string) (clientID, clientSecret string, err error) {
	clientIDHash = strings.TrimSpace(clientIDHash)
	if clientIDHash == "" {
		return "", "", fmt.Errorf("clientIdHash is empty")
	}
	if strings.Contains(clientIDHash, "/") || strings.Contains(clientIDHash, "\\") || strings.Contains(clientIDHash, "..") {
		return "", "", fmt.Errorf("invalid clientIdHash")
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("failed to get home directory: %w", err)
	}
	deviceRegPath := filepath.Join(homeDir, ".aws", "sso", "cache", clientIDHash+".json")
	data, err := os.ReadFile(deviceRegPath)
	if err != nil {
		return "", "", fmt.Errorf("failed to read device registration file: %w", err)
	}
	var deviceReg struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	}
	if err = json.Unmarshal(data, &deviceReg); err != nil {
		return "", "", fmt.Errorf("failed to parse device registration: %w", err)
	}
	clientID = strings.TrimSpace(deviceReg.ClientID)
	clientSecret = strings.TrimSpace(deviceReg.ClientSecret)
	if clientID == "" || clientSecret == "" {
		return "", "", fmt.Errorf("device registration missing clientId or clientSecret")
	}
	return clientID, clientSecret, nil
}

// ExtractEmailFromJWT extracts an email-like claim from a JWT access token.
func ExtractEmailFromJWT(accessToken string) string {
	parts := strings.Split(strings.TrimSpace(accessToken), ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]any
	if err = json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	for _, key := range []string{"email", "preferred_username", "username"} {
		if value, ok := claims[key].(string); ok {
			if email := strings.TrimSpace(value); strings.Contains(email, "@") {
				return email
			}
		}
	}
	return ""
}

var filenameUnsafe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// SanitizeEmailForFilename converts account identifiers into safe filename parts.
func SanitizeEmailForFilename(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, ".")
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, "@", "_at_")
	value = filenameUnsafe.ReplaceAllString(value, "_")
	value = strings.Trim(value, "._-")
	if len(value) > 96 {
		value = value[:96]
		value = strings.Trim(value, "._-")
	}
	return value
}

// ExtractIDCIdentifier derives a stable filename part from an Identity Center start URL.
func ExtractIDCIdentifier(startURL string) string {
	startURL = strings.TrimSpace(startURL)
	startURL = strings.TrimPrefix(startURL, "https://")
	startURL = strings.TrimPrefix(startURL, "http://")
	startURL = strings.Split(startURL, "/")[0]
	return SanitizeEmailForFilename(startURL)
}

// IsKiroCLIAuthMethod reports whether the token came from native Kiro CLI OAuth.
func IsKiroCLIAuthMethod(authMethod string) bool {
	switch strings.ToLower(strings.TrimSpace(authMethod)) {
	case "kiro-cli", "cli":
		return true
	default:
		return false
	}
}

// ExpiresAtFromSeconds converts an OAuth expires_in value into RFC3339.
func ExpiresAtFromSeconds(expiresIn int) string {
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	return time.Now().Add(time.Duration(expiresIn) * time.Second).Format(time.RFC3339)
}
