package oauthenv

import (
	"os"
	"strings"
)

const (
	GeminiClientSecretEnv      = "CLIPROXY_GEMINI_OAUTH_CLIENT_SECRET"
	AntigravityClientSecretEnv = "CLIPROXY_ANTIGRAVITY_CLIENT_SECRET"
	IFlowClientSecretEnv       = "CLIPROXY_IFLOW_OAUTH_CLIENT_SECRET"
)

func trimmedEnv(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

func GeminiClientSecret() string {
	return trimmedEnv(GeminiClientSecretEnv)
}

func AntigravityClientSecret() string {
	return trimmedEnv(AntigravityClientSecretEnv)
}

func IFlowClientSecret() string {
	return trimmedEnv(IFlowClientSecretEnv)
}
