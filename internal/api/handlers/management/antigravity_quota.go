package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

const (
	antigravityQuotaRequestTimeout = 30 * time.Second
	antigravityQuotaRefreshTimeout = 45 * time.Second
)

var antigravityQuotaFetchEndpoints = []string{
	"https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:fetchAvailableModels",
	"https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
	"https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
}

var antigravityQuotaSummaryPriority = []string{
	"gemini-3-flash",
	"gemini-3-pro-high",
	"claude-sonnet-4-5",
	"claude-sonnet-4-6",
	"gemini-3-pro-image",
	"gemini-2.5-flash",
	"gemini-2.5-pro",
}

type antigravityQuotaResponse struct {
	Error       bool                          `json:"error"`
	User        string                        `json:"user,omitempty"`
	APIKey      string                        `json:"api_key"`
	RequestedAt time.Time                     `json:"requested_at"`
	Prefix      string                        `json:"prefix,omitempty"`
	Model       string                        `json:"model,omitempty"`
	Details     bool                          `json:"details"`
	Accounts    []antigravityQuotaAccountInfo `json:"accounts"`
}

type antigravityQuotaAccountInfo struct {
	Account             string                      `json:"account"`
	Email               string                      `json:"email,omitempty"`
	Prefix              string                      `json:"prefix,omitempty"`
	State               string                      `json:"state"`
	Status              string                      `json:"status,omitempty"`
	StatusMessage       string                      `json:"status_message,omitempty"`
	RecentErrorType     string                      `json:"recent_error_type,omitempty"`
	AuthID              string                      `json:"auth_id,omitempty"`
	RuntimeModelCount   int                         `json:"runtime_model_count,omitempty"`
	TrackedModelCount   int                         `json:"tracked_model_count,omitempty"`
	MinRemainingPercent *int                        `json:"min_remaining_percent,omitempty"`
	MaxRemainingPercent *int                        `json:"max_remaining_percent,omitempty"`
	NextResetAt         *time.Time                  `json:"next_reset_at,omitempty"`
	Summary             string                      `json:"summary,omitempty"`
	MatchedModel        *antigravityQuotaModelInfo  `json:"matched_model,omitempty"`
	Models              []antigravityQuotaModelInfo `json:"models,omitempty"`
	FetchError          string                      `json:"fetch_error,omitempty"`
}

type antigravityQuotaModelInfo struct {
	Name             string     `json:"name"`
	DisplayName      string     `json:"display_name,omitempty"`
	RemainingPercent *int       `json:"remaining_percent,omitempty"`
	UsedPercent      *int       `json:"used_percent,omitempty"`
	ResetAt          *time.Time `json:"reset_at,omitempty"`
	MaxOutputTokens  *int       `json:"max_output_tokens,omitempty"`
}

type antigravityQuotaAPIResponse struct {
	Models map[string]antigravityQuotaRawModel `json:"models"`
}

type antigravityQuotaRawModel struct {
	QuotaInfo struct {
		RemainingFraction *float64 `json:"remainingFraction"`
		ResetTime         string   `json:"resetTime"`
	} `json:"quotaInfo"`
	DisplayName     string `json:"displayName"`
	MaxOutputTokens *int   `json:"maxOutputTokens"`
}

// GetAuthenticatedAntigravityQuota returns live Antigravity quota information for authenticated callers only.
func (h *Handler) GetAuthenticatedAntigravityQuota(c *gin.Context) {
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

	prefix := strings.TrimSpace(c.Query("prefix"))
	model := strings.TrimSpace(c.Query("model"))
	details := queryTruthy(c.Query("details"))
	timeout := antigravityQuotaRequestTimeout
	if raw := strings.TrimSpace(c.Query("timeout_seconds")); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			timeout = time.Duration(seconds) * time.Second
		}
	}

	rows := h.collectAntigravityQuotaRows(c.Request.Context(), prefix, model, details, timeout)

	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.Header("X-Content-Type-Options", "nosniff")
	c.JSON(http.StatusOK, antigravityQuotaResponse{
		Error:       false,
		User:        ownerFromKey(clientAPIKey),
		APIKey:      maskAPIKey(clientAPIKey),
		RequestedAt: time.Now().UTC(),
		Prefix:      prefix,
		Model:       model,
		Details:     details,
		Accounts:    rows,
	})
}

func (h *Handler) collectAntigravityQuotaRows(ctx context.Context, prefix, model string, details bool, timeout time.Duration) []antigravityQuotaAccountInfo {
	now := time.Now()
	auths := h.authManager.List()
	rows := make([]antigravityQuotaAccountInfo, 0, len(auths))
	prefix = strings.ToLower(strings.TrimSpace(prefix))

	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), "antigravity") {
			continue
		}
		if prefix != "" && !strings.EqualFold(strings.TrimSpace(auth.Prefix), prefix) {
			continue
		}

		_, account := auth.AccountInfo()
		row := antigravityQuotaAccountInfo{
			Account:           strings.TrimSpace(account),
			Email:             authEmail(auth),
			Prefix:            strings.TrimSpace(auth.Prefix),
			State:             deriveRuntimeAuthState(auth, now),
			Status:            strings.TrimSpace(string(auth.Status)),
			StatusMessage:     strings.TrimSpace(auth.StatusMessage),
			RecentErrorType:   extractAntigravityErrorType(auth.StatusMessage),
			AuthID:            strings.TrimSpace(auth.ID),
			RuntimeModelCount: 0,
		}
		if row.Account == "" {
			row.Account = strings.TrimSpace(auth.Label)
		}
		if row.Account == "" {
			row.Account = row.Email
		}
		if row.Account == "" {
			row.Account = row.AuthID
		}

		if regCount := h.runtimeModelCount(auth.ID); regCount > 0 {
			row.RuntimeModelCount = regCount
		}

		h.applyAntigravityQuota(ctx, auth, &row, model, details, timeout)
		rows = append(rows, row)
	}

	sort.Slice(rows, func(i, j int) bool {
		leftPrefix := strings.ToLower(rows[i].Prefix)
		rightPrefix := strings.ToLower(rows[j].Prefix)
		if leftPrefix != rightPrefix {
			if leftPrefix == "" {
				return false
			}
			if rightPrefix == "" {
				return true
			}
			return leftPrefix < rightPrefix
		}
		return strings.ToLower(rows[i].Account) < strings.ToLower(rows[j].Account)
	})
	return rows
}

func (h *Handler) runtimeModelCount(authID string) int {
	if h == nil || h.authManager == nil {
		return 0
	}
	reg := registry.GetGlobalRegistry()
	if reg == nil {
		return 0
	}
	return len(reg.GetModelsForClient(authID))
}

func (h *Handler) applyAntigravityQuota(ctx context.Context, auth *coreauth.Auth, row *antigravityQuotaAccountInfo, model string, details bool, timeout time.Duration) {
	if h == nil || h.authManager == nil || auth == nil || row == nil {
		return
	}
	parsed, err := h.fetchAntigravityQuotaPayload(ctx, auth, timeout)
	if err != nil {
		row.FetchError = err.Error()
		return
	}

	models := collectAntigravityQuotaModels(parsed.Models)
	row.TrackedModelCount = len(models)
	if len(models) == 0 {
		row.Summary = "-"
		return
	}

	remainingValues := make([]int, 0, len(models))
	var earliest *time.Time
	for _, item := range models {
		if item.RemainingPercent != nil {
			remainingValues = append(remainingValues, *item.RemainingPercent)
		}
		if item.ResetAt != nil && (earliest == nil || item.ResetAt.Before(*earliest)) {
			ts := *item.ResetAt
			earliest = &ts
		}
	}
	if len(remainingValues) > 0 {
		sort.Ints(remainingValues)
		row.MinRemainingPercent = intPtr(remainingValues[0])
		row.MaxRemainingPercent = intPtr(remainingValues[len(remainingValues)-1])
	}
	row.NextResetAt = earliest
	row.Summary = buildAntigravityQuotaSummary(models)

	if model != "" {
		if matched := findAntigravityQuotaModel(models, model); matched != nil {
			copied := *matched
			row.MatchedModel = &copied
		}
	}
	if details {
		row.Models = models
	}
}

func (h *Handler) fetchAntigravityQuotaPayload(ctx context.Context, auth *coreauth.Auth, timeout time.Duration) (*antigravityQuotaAPIResponse, error) {
	if h == nil || h.authManager == nil || auth == nil {
		return nil, nil
	}

	projectID := ""
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["project_id"].(string); ok {
			projectID = strings.TrimSpace(v)
		}
	}
	bodyPayload := map[string]string{}
	if projectID != "" {
		bodyPayload["project"] = projectID
	}
	bodyBytes, _ := json.Marshal(bodyPayload)

	var lastErr string
	currentAuth := auth
	refreshed := false
	projectReloaded := false

	for _, endpoint := range antigravityQuotaFetchEndpoints {
		reqCtx, cancel := context.WithTimeout(ctx, timeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			cancel()
			lastErr = err.Error()
			continue
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := h.authManager.HttpRequest(reqCtx, currentAuth, req)
		if err != nil {
			cancel()
			lastErr = err.Error()
			continue
		}

		rawBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		cancel()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			var parsed antigravityQuotaAPIResponse
			if err := json.Unmarshal(rawBody, &parsed); err != nil {
				lastErr = err.Error()
				continue
			}
			return &parsed, nil
		}

		if resp.StatusCode == http.StatusUnauthorized && !refreshed {
			updated, err := h.refreshAntigravityQuotaAuth(ctx, currentAuth)
			if err == nil && updated != nil {
				currentAuth = updated
				refreshed = true
				continue
			}
		}
		if resp.StatusCode == http.StatusBadRequest && !projectReloaded {
			updated, err := h.refreshAntigravityQuotaProjectID(ctx, currentAuth)
			if err == nil && updated != nil {
				currentAuth = updated
				projectReloaded = true
				if pid, ok := updated.Metadata["project_id"].(string); ok && strings.TrimSpace(pid) != "" {
					bodyPayload = map[string]string{"project": strings.TrimSpace(pid)}
					bodyBytes, _ = json.Marshal(bodyPayload)
				}
				continue
			}
		}

		lastErr = strings.TrimSpace(string(rawBody))
		if lastErr == "" {
			lastErr = http.StatusText(resp.StatusCode)
		}
	}

	if lastErr == "" {
		lastErr = "quota request failed"
	}
	return nil, fmt.Errorf("%s", lastErr)
}

func (h *Handler) refreshAntigravityQuotaAuth(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if h == nil || h.authManager == nil || auth == nil {
		return nil, nil
	}
	exec, ok := h.authManager.Executor(auth.Provider)
	if !ok || exec == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, antigravityQuotaRefreshTimeout)
	defer cancel()

	refreshed, err := exec.Refresh(ctx, auth.Clone())
	if err != nil || refreshed == nil {
		return nil, err
	}
	updated, err := h.authManager.Update(ctx, refreshed)
	if err != nil {
		return refreshed, err
	}
	return updated, nil
}

func (h *Handler) refreshAntigravityQuotaProjectID(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if h == nil || h.authManager == nil || auth == nil {
		return nil, nil
	}
	accessToken := ""
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["access_token"].(string); ok {
			accessToken = strings.TrimSpace(v)
		}
	}
	if accessToken == "" {
		return nil, fmt.Errorf("missing access token")
	}
	ctx, cancel := context.WithTimeout(ctx, antigravityQuotaRefreshTimeout)
	defer cancel()
	projectID, err := sdkauth.FetchAntigravityProjectID(ctx, accessToken, http.DefaultClient)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("empty project id")
	}
	updatedAuth := auth.Clone()
	if updatedAuth.Metadata == nil {
		updatedAuth.Metadata = make(map[string]any)
	}
	updatedAuth.Metadata["project_id"] = strings.TrimSpace(projectID)
	updated, err := h.authManager.Update(ctx, updatedAuth)
	if err != nil {
		return updatedAuth, err
	}
	return updated, nil
}

func collectAntigravityQuotaModels(raw map[string]antigravityQuotaRawModel) []antigravityQuotaModelInfo {
	models := make([]antigravityQuotaModelInfo, 0, len(raw))
	for name, item := range raw {
		if !isAntigravityPublicModel(name) {
			continue
		}
		model := antigravityQuotaModelInfo{
			Name:            strings.TrimSpace(name),
			DisplayName:     strings.TrimSpace(item.DisplayName),
			MaxOutputTokens: item.MaxOutputTokens,
		}
		if model.DisplayName == "" {
			model.DisplayName = model.Name
		}
		if item.QuotaInfo.RemainingFraction != nil {
			remaining := antigravityFractionToPercent(*item.QuotaInfo.RemainingFraction)
			model.RemainingPercent = &remaining
			used := max(0, 100-remaining)
			model.UsedPercent = &used
		}
		if resetAt := parseRFC3339ToUTC8(item.QuotaInfo.ResetTime); resetAt != nil {
			model.ResetAt = resetAt
		}
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool {
		return strings.ToLower(models[i].Name) < strings.ToLower(models[j].Name)
	})
	return models
}

func antigravityFractionToPercent(value float64) int {
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}
	return int(value * 100)
}

func parseRFC3339ToUTC8(value string) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	ts, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	converted := ts.In(oauthQuotaUTCPlus8)
	return &converted
}

func isAntigravityPublicModel(name string) bool {
	lowered := strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(lowered, "gemini") ||
		strings.HasPrefix(lowered, "claude") ||
		strings.HasPrefix(lowered, "gpt") ||
		strings.HasPrefix(lowered, "image") ||
		strings.HasPrefix(lowered, "imagen")
}

func findAntigravityQuotaModel(models []antigravityQuotaModelInfo, requested string) *antigravityQuotaModelInfo {
	wanted := strings.ToLower(strings.TrimSpace(requested))
	if wanted == "" {
		return nil
	}
	for i := range models {
		if strings.EqualFold(models[i].Name, wanted) {
			return &models[i]
		}
	}
	for i := range models {
		if strings.Contains(strings.ToLower(models[i].Name), wanted) {
			return &models[i]
		}
	}
	return nil
}

func buildAntigravityQuotaSummary(models []antigravityQuotaModelInfo) string {
	if len(models) == 0 {
		return "-"
	}
	byName := make(map[string]antigravityQuotaModelInfo, len(models))
	for _, model := range models {
		byName[strings.ToLower(model.Name)] = model
	}
	parts := make([]string, 0, 4)
	seen := map[string]struct{}{}
	for _, name := range antigravityQuotaSummaryPriority {
		model, ok := byName[strings.ToLower(name)]
		if !ok {
			continue
		}
		parts = append(parts, antigravitySummaryPart(model))
		seen[strings.ToLower(model.Name)] = struct{}{}
		if len(parts) >= 4 {
			return strings.Join(parts, " ")
		}
	}
	for _, model := range models {
		key := strings.ToLower(model.Name)
		if _, ok := seen[key]; ok {
			continue
		}
		parts = append(parts, antigravitySummaryPart(model))
		if len(parts) >= 4 {
			break
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}

func antigravitySummaryPart(model antigravityQuotaModelInfo) string {
	shown := "-"
	if model.RemainingPercent != nil {
		shown = strconv.Itoa(*model.RemainingPercent) + "%"
	}
	return model.Name + "=" + shown
}

func extractAntigravityErrorType(message string) string {
	text := strings.TrimSpace(message)
	if text == "" {
		return ""
	}
	type errorPayload struct {
		Error struct {
			Status  string `json:"status"`
			Code    string `json:"code"`
			Message string `json:"message"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	var payload errorPayload
	if err := json.Unmarshal([]byte(text), &payload); err == nil {
		for _, detail := range payload.Error.Details {
			if strings.TrimSpace(detail.Reason) != "" {
				return strings.TrimSpace(detail.Reason)
			}
		}
		for _, value := range []string{payload.Error.Status, payload.Error.Code, payload.Error.Message} {
			if strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	if strings.Contains(strings.ToLower(text), "verify your account") {
		return "VALIDATION_REQUIRED"
	}
	return text
}
