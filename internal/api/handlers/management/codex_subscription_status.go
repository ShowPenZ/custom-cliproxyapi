package management

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type codexSubscriptionStatusResponse struct {
	Error       bool                             `json:"error"`
	User        string                           `json:"user,omitempty"`
	APIKey      string                           `json:"api_key"`
	RequestedAt time.Time                        `json:"requested_at"`
	Accounts    []codexSubscriptionStatusAccount `json:"accounts"`
}

type codexSubscriptionStatusAccount struct {
	Account                   string     `json:"account"`
	Email                     string     `json:"email,omitempty"`
	State                     string     `json:"state"`
	Status                    string     `json:"status,omitempty"`
	StatusMessage             string     `json:"status_message,omitempty"`
	AuthID                    string     `json:"auth_id,omitempty"`
	PlanType                  string     `json:"plan_type,omitempty"`
	ChatGPTAccountID          string     `json:"chatgpt_account_id,omitempty"`
	SubscriptionActiveStart   *time.Time `json:"subscription_active_start,omitempty"`
	SubscriptionActiveUntil   *time.Time `json:"subscription_active_until,omitempty"`
	SubscriptionLastCheckedAt *time.Time `json:"subscription_last_checked_at,omitempty"`
	TokenExpiresAt            *time.Time `json:"token_expires_at,omitempty"`
}

// GetAuthenticatedCodexSubscriptionStatus returns cached ChatGPT subscription metadata
// from Codex OAuth ID tokens for authenticated callers only.
func (h *Handler) GetAuthenticatedCodexSubscriptionStatus(c *gin.Context) {
	apiKey, _ := c.Get("apiKey")
	clientAPIKey, _ := apiKey.(string)
	clientAPIKey = strings.TrimSpace(clientAPIKey)
	if clientAPIKey == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": true, "message": "missing api key"})
		return
	}
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": true, "message": "core auth manager unavailable"})
		return
	}

	refresh := queryTruthy(c.Query("refresh"))
	rows := h.collectCodexSubscriptionStatusRows(c.Request.Context(), refresh)

	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.Header("X-Content-Type-Options", "nosniff")
	c.JSON(http.StatusOK, codexSubscriptionStatusResponse{
		Error:       false,
		User:        ownerFromKey(clientAPIKey),
		APIKey:      maskAPIKey(clientAPIKey),
		RequestedAt: time.Now().UTC(),
		Accounts:    rows,
	})
}

func (h *Handler) collectCodexSubscriptionStatusRows(ctx context.Context, forceRefresh bool) []codexSubscriptionStatusAccount {
	if h == nil || h.authManager == nil {
		return nil
	}

	now := time.Now()
	auths := h.authManager.List()
	rows := make([]codexSubscriptionStatusAccount, 0, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			continue
		}

		accountType, account := auth.AccountInfo()
		if !strings.EqualFold(strings.TrimSpace(accountType), "oauth") {
			continue
		}

		workingAuth, err := h.refreshCodexSubscriptionAuth(ctx, auth, forceRefresh)
		if err == nil && workingAuth != nil {
			auth = workingAuth
			accountType, account = auth.AccountInfo()
			if !strings.EqualFold(strings.TrimSpace(accountType), "oauth") {
				continue
			}
		}

		row := codexSubscriptionStatusAccount{
			Account:       strings.TrimSpace(account),
			Email:         authEmail(auth),
			State:         deriveRuntimeAuthState(auth, now),
			Status:        strings.TrimSpace(string(auth.Status)),
			StatusMessage: strings.TrimSpace(auth.StatusMessage),
			AuthID:        strings.TrimSpace(auth.ID),
		}
		if row.Account == "" {
			row.Account = row.Email
		}
		if row.Account == "" {
			row.Account = row.AuthID
		}
		if expiresAt, ok := auth.ExpirationTime(); ok {
			ts := expiresAt.UTC()
			row.TokenExpiresAt = &ts
		}

		snapshot := codexSubscriptionSnapshotFromAuth(auth)
		row.PlanType = snapshot.planType
		row.ChatGPTAccountID = snapshot.chatGPTAccountID
		row.SubscriptionActiveStart = snapshot.subscriptionActiveStart
		row.SubscriptionActiveUntil = snapshot.subscriptionActiveUntil
		row.SubscriptionLastCheckedAt = snapshot.subscriptionLastChecked

		rows = append(rows, row)
	}

	sort.Slice(rows, func(i, j int) bool {
		leftUntil := rows[i].SubscriptionActiveUntil
		rightUntil := rows[j].SubscriptionActiveUntil
		switch {
		case leftUntil == nil && rightUntil == nil:
			return strings.ToLower(rows[i].Account) < strings.ToLower(rows[j].Account)
		case leftUntil == nil:
			return false
		case rightUntil == nil:
			return true
		case leftUntil.Equal(*rightUntil):
			return strings.ToLower(rows[i].Account) < strings.ToLower(rows[j].Account)
		default:
			return leftUntil.Before(*rightUntil)
		}
	})
	return rows
}

func claimTimeValue(value any) *time.Time {
	switch v := value.(type) {
	case time.Time:
		if v.IsZero() {
			return nil
		}
		ts := v.UTC()
		return &ts
	case *time.Time:
		if v == nil || v.IsZero() {
			return nil
		}
		ts := v.UTC()
		return &ts
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return nil
		}
		if ts, err := time.Parse(time.RFC3339, trimmed); err == nil {
			utc := ts.UTC()
			return &utc
		}
	}
	return nil
}
