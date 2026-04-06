// Package antigravity provides OAuth2 authentication functionality for the Antigravity provider.
package antigravity

import (
	"fmt"
	"os"
	"strings"
)

const (
	CallbackPort             = 51121
	ClientIDEnvVar           = "CLIPROXY_ANTIGRAVITY_CLIENT_ID"
	ClientSecretEnvVar       = "CLIPROXY_ANTIGRAVITY_CLIENT_SECRET"
	LegacyClientIDEnvVar     = "CLIPROXY_ANTIGRAVITY_OAUTH_CLIENT_ID"
	LegacyClientSecretEnvVar = "CLIPROXY_ANTIGRAVITY_OAUTH_CLIENT_SECRET"
)

// Scopes defines the OAuth scopes required for Antigravity authentication
var Scopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

// OAuth2 endpoints for Google authentication
const (
	TokenEndpoint    = "https://oauth2.googleapis.com/token"
	AuthEndpoint     = "https://accounts.google.com/o/oauth2/v2/auth"
	UserInfoEndpoint = "https://www.googleapis.com/oauth2/v1/userinfo?alt=json"
)

// Antigravity API configuration
const (
	APIEndpoint    = "https://cloudcode-pa.googleapis.com"
	APIVersion     = "v1internal"
	APIUserAgent   = "google-api-nodejs-client/9.15.1"
	APIClient      = "google-cloud-sdk vscode_cloudshelleditor/0.1"
	ClientMetadata = `{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}`
)

func OAuthClientID() string {
	return firstNonEmptyEnv(ClientIDEnvVar, LegacyClientIDEnvVar)
}

func OAuthClientSecret() string {
	return firstNonEmptyEnv(ClientSecretEnvVar, LegacyClientSecretEnvVar)
}

func OAuthCredentials() (string, string, error) {
	clientID := OAuthClientID()
	clientSecret := OAuthClientSecret()
	if clientID == "" || clientSecret == "" {
		return "", "", fmt.Errorf(
			"antigravity oauth credentials are not configured; set %s and %s",
			ClientIDEnvVar,
			ClientSecretEnvVar,
		)
	}
	return clientID, clientSecret, nil
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
