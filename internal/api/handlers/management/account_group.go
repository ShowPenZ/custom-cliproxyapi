package management

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func codexPlanTypeFromMetadata(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	if raw, ok := metadata["plan_type"].(string); ok {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			return trimmed
		}
	}
	idTokenRaw, _ := metadata["id_token"].(string)
	idToken := strings.TrimSpace(idTokenRaw)
	if idToken == "" {
		return ""
	}
	claims, err := codex.ParseJWTToken(idToken)
	if err != nil || claims == nil {
		return ""
	}
	return strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType)
}

func authPlanType(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if planType := strings.TrimSpace(authAttribute(auth, "plan_type")); planType != "" {
		return planType
	}
	if planType := codexPlanTypeFromMetadata(auth.Metadata); planType != "" {
		return planType
	}
	return ""
}

func configuredAccountGroupFromMetadata(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	if raw, ok := metadata["account_group"].(string); ok {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			return trimmed
		}
	}
	if raw, ok := metadata["group"].(string); ok {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func configuredAccountGroup(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if group := strings.TrimSpace(authAttribute(auth, "account_group")); group != "" {
		return group
	}
	return configuredAccountGroupFromMetadata(auth.Metadata)
}

func accountGroupLabel(provider, group string) string {
	group = strings.TrimSpace(group)
	if group == "" {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(provider), "codex") {
		switch strings.ToLower(group) {
		case "pro":
			return "Codex Pro"
		case "plus":
			return "Codex Plus"
		case "team":
			return "Codex Team"
		case "free":
			return "Codex Free"
		}
	}
	return group
}

func authAccountGroup(auth *coreauth.Auth) (string, string) {
	if auth == nil {
		return "", ""
	}
	if group := configuredAccountGroup(auth); group != "" {
		return group, accountGroupLabel(auth.Provider, group)
	}
	if strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		if group := codex.AccountGroupFromPlanType(authPlanType(auth)); group != "" {
			return group, accountGroupLabel(auth.Provider, group)
		}
	}
	return "", ""
}
