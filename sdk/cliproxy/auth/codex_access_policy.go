package auth

import (
	"strconv"
	"strings"

	internalcodex "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// AuthAccountGroup resolves the logical account group for an auth entry.
func AuthAccountGroup(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if group := strings.TrimSpace(auth.Attributes["account_group"]); group != "" {
			return strings.ToLower(group)
		}
		if strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			if planType := strings.TrimSpace(auth.Attributes["plan_type"]); planType != "" {
				if group := strings.TrimSpace(internalcodex.AccountGroupFromPlanType(planType)); group != "" {
					return strings.ToLower(group)
				}
			}
		}
	}
	return ""
}

// RequestedAccountGroupFromMetadata resolves the requested account group from execution metadata.
func RequestedAccountGroupFromMetadata(meta map[string]any) string {
	return metadataString(meta, cliproxyexecutor.RequestedAccountGroupMetadataKey)
}

// CodexAllowProFromMetadata reports whether Codex "pro" auths may be used.
// When the metadata key is absent, the default is true to preserve legacy behaviour.
func CodexAllowProFromMetadata(meta map[string]any) bool {
	if len(meta) == 0 {
		return true
	}
	raw, ok := meta[cliproxyexecutor.CodexAllowProMetadataKey]
	if !ok || raw == nil {
		return true
	}
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(v))
		if err == nil {
			return parsed
		}
	case []byte:
		parsed, err := strconv.ParseBool(strings.TrimSpace(string(v)))
		if err == nil {
			return parsed
		}
	}
	return true
}

// CodexDeniedAccountGroupsFromMetadata returns request-scoped Codex groups that must be excluded.
func CodexDeniedAccountGroupsFromMetadata(meta map[string]any) map[string]struct{} {
	denied := make(map[string]struct{})
	if len(meta) == 0 {
		return denied
	}
	raw, ok := meta[cliproxyexecutor.CodexDeniedAccountGroupsMetadataKey]
	if !ok || raw == nil {
		return denied
	}
	add := func(value string) {
		for _, part := range strings.Split(value, ",") {
			if group := strings.ToLower(strings.TrimSpace(part)); group != "" {
				denied[group] = struct{}{}
			}
		}
	}
	switch v := raw.(type) {
	case string:
		add(v)
	case []byte:
		add(string(v))
	case []string:
		for _, item := range v {
			add(item)
		}
	case []any:
		for _, item := range v {
			switch typed := item.(type) {
			case string:
				add(typed)
			case []byte:
				add(string(typed))
			}
		}
	}
	return denied
}

// AuthAllowedForMetadata applies request-scoped account-group policy to an auth entry.
func AuthAllowedForMetadata(auth *Auth, meta map[string]any) bool {
	if auth == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return true
	}

	group := AuthAccountGroup(auth)
	if strings.EqualFold(group, "pro") && !CodexAllowProFromMetadata(meta) {
		return false
	}
	if _, denied := CodexDeniedAccountGroupsFromMetadata(meta)[strings.ToLower(strings.TrimSpace(group))]; denied {
		return false
	}

	requestedGroup := RequestedAccountGroupFromMetadata(meta)
	if requestedGroup == "" {
		return true
	}
	return strings.EqualFold(group, requestedGroup)
}

func metadataString(meta map[string]any, key string) string {
	if len(meta) == 0 {
		return ""
	}
	raw, ok := meta[key]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return strings.ToLower(strings.TrimSpace(v))
	case []byte:
		return strings.ToLower(strings.TrimSpace(string(v)))
	default:
		return ""
	}
}
