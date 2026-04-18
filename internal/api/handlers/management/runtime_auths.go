package management

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func (h *Handler) ListRuntimeAuths(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(503, gin.H{"error": "core auth manager unavailable"})
		return
	}

	providerFilter := strings.ToLower(strings.TrimSpace(c.Query("provider")))
	now := time.Now()
	reg := registry.GetGlobalRegistry()
	auths := h.authManager.List()
	items := make([]gin.H, 0, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		if providerFilter != "" && provider != providerFilter {
			continue
		}
		items = append(items, buildRuntimeAuthEntry(auth, reg, now))
	}

	sort.Slice(items, func(i, j int) bool {
		leftProvider, _ := items[i]["provider"].(string)
		rightProvider, _ := items[j]["provider"].(string)
		if leftProvider != rightProvider {
			return leftProvider < rightProvider
		}
		leftAccount, _ := items[i]["account"].(string)
		rightAccount, _ := items[j]["account"].(string)
		if leftAccount != rightAccount {
			return strings.ToLower(leftAccount) < strings.ToLower(rightAccount)
		}
		leftID, _ := items[i]["id"].(string)
		rightID, _ := items[j]["id"].(string)
		return strings.ToLower(leftID) < strings.ToLower(rightID)
	})

	c.JSON(200, gin.H{"auths": items})
}

func buildRuntimeAuthEntry(auth *coreauth.Auth, reg *registry.ModelRegistry, now time.Time) gin.H {
	auth.EnsureIndex()
	accountType, account := auth.AccountInfo()
	source := "runtime"
	if fileName := strings.TrimSpace(auth.FileName); fileName != "" || strings.TrimSpace(authAttribute(auth, "path")) != "" {
		source = "file"
	} else if src := strings.TrimSpace(authAttribute(auth, "source")); strings.HasPrefix(strings.ToLower(src), "config:") {
		source = "config"
	}

	displayAccount := strings.TrimSpace(account)
	if accountType == "api_key" && displayAccount != "" {
		displayAccount = util.HideAPIKey(displayAccount)
	}

	entry := gin.H{
		"id":             auth.ID,
		"auth_index":     auth.Index,
		"provider":       strings.TrimSpace(auth.Provider),
		"label":          auth.Label,
		"status":         auth.Status,
		"status_message": auth.StatusMessage,
		"disabled":       auth.Disabled,
		"unavailable":    auth.Unavailable,
		"runtime_only":   isRuntimeOnlyAuth(auth),
		"source":         source,
		"state":          deriveRuntimeAuthState(auth, now),
	}

	if prefix := strings.TrimSpace(auth.Prefix); prefix != "" {
		entry["prefix"] = prefix
	}
	if accountType != "" {
		entry["account_type"] = accountType
	}
	if displayAccount != "" {
		entry["account"] = displayAccount
	}
	if accountType == "api_key" {
		entry["api_key_masked"] = displayAccount
	}
	if email := authEmail(auth); email != "" {
		entry["email"] = email
	}
	if baseURL := strings.TrimSpace(authAttribute(auth, "base_url")); baseURL != "" {
		entry["base_url"] = baseURL
	}
	if rawPriority := strings.TrimSpace(authAttribute(auth, "priority")); rawPriority != "" {
		if priority, err := strconv.Atoi(rawPriority); err == nil {
			entry["priority"] = priority
		}
	}
	if planType := authPlanType(auth); planType != "" {
		entry["plan_type"] = planType
	}
	if group, label := authAccountGroup(auth); group != "" {
		entry["account_group"] = group
		if label != "" {
			entry["account_group_label"] = label
		}
	}
	if !auth.CreatedAt.IsZero() {
		entry["created_at"] = auth.CreatedAt
	}
	if !auth.UpdatedAt.IsZero() {
		entry["updated_at"] = auth.UpdatedAt
	}
	if !auth.LastRefreshedAt.IsZero() {
		entry["last_refreshed_at"] = auth.LastRefreshedAt
	}
	if !auth.NextRefreshAfter.IsZero() {
		entry["next_refresh_after"] = auth.NextRefreshAfter
	}
	if !auth.NextRetryAfter.IsZero() {
		entry["next_retry_after"] = auth.NextRetryAfter
	}
	if expiresAt, ok := auth.ExpirationTime(); ok {
		entry["expires_at"] = expiresAt
	}

	entry["quota_exceeded"] = auth.Quota.Exceeded
	if reason := strings.TrimSpace(auth.Quota.Reason); reason != "" {
		entry["quota_reason"] = reason
	}
	if !auth.Quota.NextRecoverAt.IsZero() {
		entry["quota_next_recover_at"] = auth.Quota.NextRecoverAt
	}
	if auth.Quota.BackoffLevel > 0 {
		entry["quota_backoff_level"] = auth.Quota.BackoffLevel
	}

	if claims := extractCodexIDTokenClaims(auth); claims != nil {
		if accountID, ok := claims["chatgpt_account_id"]; ok {
			entry["chatgpt_account_id"] = accountID
		}
		if activeUntil, ok := claims["chatgpt_subscription_active_until"]; ok {
			entry["subscription_active_until"] = activeUntil
		}
	}

	if reg != nil {
		entry["model_count"] = len(reg.GetModelsForClient(auth.ID))
	}

	return entry
}

func deriveRuntimeAuthState(auth *coreauth.Auth, now time.Time) string {
	if auth == nil {
		return "unknown"
	}
	if auth.Disabled || auth.Status == coreauth.StatusDisabled {
		return "disabled"
	}
	if auth.Quota.Exceeded && !auth.Quota.NextRecoverAt.IsZero() && auth.Quota.NextRecoverAt.After(now) {
		return "cooling"
	}
	if auth.Unavailable && !auth.NextRetryAfter.IsZero() && auth.NextRetryAfter.After(now) {
		return "retry-wait"
	}
	if auth.Status == coreauth.StatusActive && !auth.Unavailable {
		return "online"
	}
	status := strings.TrimSpace(string(auth.Status))
	if status == "" {
		return "unknown"
	}
	return status
}
