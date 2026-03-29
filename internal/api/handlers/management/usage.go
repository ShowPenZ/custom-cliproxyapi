package management

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

type usageExportPayload struct {
	Version    int                      `json:"version"`
	ExportedAt time.Time                `json:"exported_at"`
	Usage      usage.StatisticsSnapshot `json:"usage"`
}

type usageImportPayload struct {
	Version int                      `json:"version"`
	Usage   usage.StatisticsSnapshot `json:"usage"`
}

type userUsageModelSnapshot struct {
	TotalRequests int64      `json:"total_requests"`
	TotalTokens   int64      `json:"total_tokens"`
	LastRequestAt *time.Time `json:"last_request_at,omitempty"`
}

type userUsageSnapshot struct {
	TotalRequests  int64                             `json:"total_requests"`
	SuccessCount   int64                             `json:"success_count"`
	FailureCount   int64                             `json:"failure_count"`
	TotalTokens    int64                             `json:"total_tokens"`
	RequestsByDay  map[string]int64                  `json:"requests_by_day,omitempty"`
	RequestsByHour map[string]int64                  `json:"requests_by_hour,omitempty"`
	TokensByDay    map[string]int64                  `json:"tokens_by_day,omitempty"`
	TokensByHour   map[string]int64                  `json:"tokens_by_hour,omitempty"`
	Models         map[string]userUsageModelSnapshot `json:"models,omitempty"`
	LastRequestAt  *time.Time                        `json:"last_request_at,omitempty"`
}

type userUsageResponse struct {
	Error            bool              `json:"error"`
	User             string            `json:"user,omitempty"`
	APIKey           string            `json:"api_key"`
	Usage            userUsageSnapshot `json:"usage"`
	Balance          *float64          `json:"balance"`
	Unit             *string           `json:"unit"`
	BalanceAvailable bool              `json:"balance_available"`
	Message          string            `json:"message,omitempty"`
}

var teamKeySuffixPattern = regexp.MustCompile(`^[0-9a-f]{16,}$`)

// GetUsageStatistics returns the in-memory request statistics snapshot.
func (h *Handler) GetUsageStatistics(c *gin.Context) {
	var snapshot usage.StatisticsSnapshot
	if h != nil && h.usageStats != nil {
		snapshot = h.usageStats.Snapshot()
	}
	c.JSON(http.StatusOK, gin.H{
		"usage":           snapshot,
		"failed_requests": snapshot.FailureCount,
	})
}

// ExportUsageStatistics returns a complete usage snapshot for backup/migration.
func (h *Handler) ExportUsageStatistics(c *gin.Context) {
	var snapshot usage.StatisticsSnapshot
	if h != nil && h.usageStats != nil {
		snapshot = h.usageStats.Snapshot()
	}
	c.JSON(http.StatusOK, usageExportPayload{
		Version:    1,
		ExportedAt: time.Now().UTC(),
		Usage:      snapshot,
	})
}

// ImportUsageStatistics merges a previously exported usage snapshot into memory.
func (h *Handler) ImportUsageStatistics(c *gin.Context) {
	if h == nil || h.usageStats == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "usage statistics unavailable"})
		return
	}

	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}

	var payload usageImportPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}
	if payload.Version != 0 && payload.Version != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported version"})
		return
	}

	result := h.usageStats.MergeSnapshot(payload.Usage)
	snapshot := h.usageStats.Snapshot()
	c.JSON(http.StatusOK, gin.H{
		"added":           result.Added,
		"skipped":         result.Skipped,
		"total_requests":  snapshot.TotalRequests,
		"failed_requests": snapshot.FailureCount,
	})
}

// GetAuthenticatedUsage returns the current caller's own aggregated usage only.
func (h *Handler) GetAuthenticatedUsage(c *gin.Context) {
	apiKey, _ := c.Get("apiKey")
	clientAPIKey, _ := apiKey.(string)
	clientAPIKey = strings.TrimSpace(clientAPIKey)
	if clientAPIKey == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": true, "message": "missing api key"})
		return
	}

	var snapshot usage.StatisticsSnapshot
	if h != nil && h.usageStats != nil {
		snapshot = h.usageStats.SnapshotForAPI(clientAPIKey)
	}
	apiSnapshot, _ := snapshot.APIs[clientAPIKey]

	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.Header("X-Content-Type-Options", "nosniff")

	c.JSON(http.StatusOK, userUsageResponse{
		Error:            false,
		User:             ownerFromKey(clientAPIKey),
		APIKey:           maskAPIKey(clientAPIKey),
		Usage:            sanitizeUserUsage(snapshot, apiSnapshot),
		Balance:          nil,
		Unit:             nil,
		BalanceAvailable: false,
		Message:          "USD balance unavailable because no billing ledger is configured; this endpoint returns locally persisted usage totals instead.",
	})
}

func sanitizeUserUsage(snapshot usage.StatisticsSnapshot, apiSnapshot usage.APISnapshot) userUsageSnapshot {
	result := userUsageSnapshot{
		TotalRequests:  snapshot.TotalRequests,
		SuccessCount:   snapshot.SuccessCount,
		FailureCount:   snapshot.FailureCount,
		TotalTokens:    snapshot.TotalTokens,
		RequestsByDay:  cloneInt64Map(snapshot.RequestsByDay),
		RequestsByHour: cloneInt64Map(snapshot.RequestsByHour),
		TokensByDay:    cloneInt64Map(snapshot.TokensByDay),
		TokensByHour:   cloneInt64Map(snapshot.TokensByHour),
	}
	if len(apiSnapshot.Models) == 0 {
		return result
	}

	result.Models = make(map[string]userUsageModelSnapshot, len(apiSnapshot.Models))
	for modelName, modelSnapshot := range apiSnapshot.Models {
		modelView := userUsageModelSnapshot{
			TotalRequests: modelSnapshot.TotalRequests,
			TotalTokens:   modelSnapshot.TotalTokens,
		}
		for _, detail := range modelSnapshot.Details {
			ts := detail.Timestamp
			if ts.IsZero() {
				continue
			}
			if modelView.LastRequestAt == nil || ts.After(*modelView.LastRequestAt) {
				copied := ts
				modelView.LastRequestAt = &copied
			}
			if result.LastRequestAt == nil || ts.After(*result.LastRequestAt) {
				copied := ts
				result.LastRequestAt = &copied
			}
		}
		result.Models[modelName] = modelView
	}

	return result
}

func cloneInt64Map(source map[string]int64) map[string]int64 {
	if len(source) == 0 {
		return nil
	}
	out := make(map[string]int64, len(source))
	for k, v := range source {
		out[k] = v
	}
	return out
}

func maskAPIKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 16 {
		return value
	}
	return value[:12] + "..." + value[len(value)-4:]
}

func ownerFromKey(key string) string {
	const prefix = "sk-team-"
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, prefix) {
		return "manual"
	}
	rest := strings.TrimPrefix(key, prefix)
	idx := strings.LastIndex(rest, "-")
	if idx <= 0 || idx >= len(rest)-1 {
		return "manual"
	}
	user := rest[:idx]
	suffix := rest[idx+1:]
	if !teamKeySuffixPattern.MatchString(suffix) {
		return "manual"
	}
	return user
}
