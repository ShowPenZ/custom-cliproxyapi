package management

import (
	"context"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

const (
	codexSubscriptionRefreshTimeout = 45 * time.Second
	codexSubscriptionStaleAfter     = 24 * time.Hour
	codexTokenRefreshLead           = 15 * time.Minute
)

type codexSubscriptionSnapshot struct {
	planType                string
	chatGPTAccountID        string
	subscriptionActiveStart *time.Time
	subscriptionActiveUntil *time.Time
	subscriptionLastChecked *time.Time
	tokenExpiresAt          *time.Time
}

func codexSubscriptionSnapshotFromAuth(auth *coreauth.Auth) codexSubscriptionSnapshot {
	snapshot := codexSubscriptionSnapshot{}
	if auth == nil {
		return snapshot
	}

	if claims := extractCodexIDTokenClaims(auth); claims != nil {
		if planType, ok := claims["plan_type"].(string); ok {
			snapshot.planType = strings.TrimSpace(planType)
		}
		if accountID, ok := claims["chatgpt_account_id"].(string); ok {
			snapshot.chatGPTAccountID = strings.TrimSpace(accountID)
		}
		snapshot.subscriptionActiveStart = claimTimeValue(claims["chatgpt_subscription_active_start"])
		snapshot.subscriptionActiveUntil = claimTimeValue(claims["chatgpt_subscription_active_until"])
		snapshot.subscriptionLastChecked = claimTimeValue(claims["chatgpt_subscription_last_checked"])
	}

	if expiresAt, ok := auth.ExpirationTime(); ok {
		ts := expiresAt.UTC()
		snapshot.tokenExpiresAt = &ts
	}
	return snapshot
}

func shouldRefreshCodexSubscription(auth *coreauth.Auth, force bool, now time.Time) bool {
	if auth == nil {
		return false
	}
	if force {
		return true
	}
	if auth.Metadata == nil {
		return false
	}
	if strings.TrimSpace(auth.Provider) == "" || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return false
	}
	if strings.TrimSpace(stringValue(auth.Metadata, "refresh_token")) == "" {
		return false
	}

	snapshot := codexSubscriptionSnapshotFromAuth(auth)
	if snapshot.subscriptionActiveUntil == nil {
		return true
	}
	if !snapshot.subscriptionActiveUntil.After(now) {
		return true
	}
	if snapshot.subscriptionLastChecked == nil {
		return false
	}
	if now.Sub(snapshot.subscriptionLastChecked.UTC()) < codexSubscriptionStaleAfter {
		return false
	}
	if snapshot.tokenExpiresAt == nil {
		return true
	}
	return !snapshot.tokenExpiresAt.After(now.Add(codexTokenRefreshLead))
}

func (h *Handler) refreshCodexSubscriptionAuth(ctx context.Context, auth *coreauth.Auth, force bool) (*coreauth.Auth, error) {
	if h == nil || h.authManager == nil || auth == nil {
		return auth, nil
	}
	if !shouldRefreshCodexSubscription(auth, force, time.Now().UTC()) {
		return auth, nil
	}

	exec, ok := h.authManager.Executor(auth.Provider)
	if !ok || exec == nil {
		return auth, nil
	}

	ctx, cancel := context.WithTimeout(ctx, codexSubscriptionRefreshTimeout)
	defer cancel()

	refreshed, err := exec.Refresh(ctx, auth.Clone())
	if err != nil {
		return auth, err
	}
	if refreshed == nil {
		return auth, nil
	}

	updated, err := h.authManager.Update(ctx, refreshed)
	if err != nil {
		return refreshed, err
	}
	if updated != nil {
		return updated, nil
	}
	return refreshed, nil
}
