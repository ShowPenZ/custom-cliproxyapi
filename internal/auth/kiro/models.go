package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

// KiroModel represents a model available through the Kiro/CodeWhisperer API.
type KiroModel struct {
	ModelID        string  `json:"modelId"`
	ModelName      string  `json:"modelName"`
	Description    string  `json:"description"`
	RateMultiplier float64 `json:"rateMultiplier"`
	RateUnit       string  `json:"rateUnit"`
	MaxInputTokens int     `json:"maxInputTokens,omitempty"`
}

// KiroAuth handles lightweight Kiro runtime API calls used outside the executor.
type KiroAuth struct {
	httpClient *http.Client
}

// NewKiroAuth creates a Kiro runtime API helper.
func NewKiroAuth(cfg *config.Config) *KiroAuth {
	client := &http.Client{Timeout: 120 * time.Second}
	if cfg != nil {
		client = util.SetProxy(&cfg.SDKConfig, client)
	}
	return &KiroAuth{httpClient: client}
}

func (k *KiroAuth) makeRequest(ctx context.Context, path string, tokenData *KiroTokenData, queryParams map[string]string) ([]byte, error) {
	if tokenData == nil {
		return nil, fmt.Errorf("kiro auth: token data is nil")
	}
	endpoint := GetKiroAPIEndpointFromProfileArn(queryParams["profileArn"])
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, buildURL(endpoint, path, queryParams), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	accountKey := GetAccountKey(tokenData.ClientID, tokenData.RefreshToken)
	if tokenData.APIKey != "" {
		accountKey = GenerateAccountKey(tokenData.APIKey)
	}
	setRuntimeHeaders(req, tokenData.AccessToken, accountKey, tokenData.AuthMethod)

	resp, err := k.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}
	return body, nil
}

// ListAvailableModels retrieves available models from the Kiro runtime API.
func (k *KiroAuth) ListAvailableModels(ctx context.Context, tokenData *KiroTokenData) ([]*KiroModel, error) {
	queryParams := map[string]string{
		"origin":     OriginForAuthMethod(tokenData.AuthMethod),
		"profileArn": tokenData.ProfileArn,
	}

	body, err := k.makeRequest(ctx, pathListAvailableModels, tokenData, queryParams)
	if err != nil {
		return nil, err
	}

	var result struct {
		Models []struct {
			ModelID        string  `json:"modelId"`
			ModelName      string  `json:"modelName"`
			Description    string  `json:"description"`
			RateMultiplier float64 `json:"rateMultiplier"`
			RateUnit       string  `json:"rateUnit"`
			TokenLimits    *struct {
				MaxInputTokens int `json:"maxInputTokens"`
			} `json:"tokenLimits"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse models response: %w", err)
	}

	models := make([]*KiroModel, 0, len(result.Models))
	for _, m := range result.Models {
		maxInputTokens := 0
		if m.TokenLimits != nil {
			maxInputTokens = m.TokenLimits.MaxInputTokens
		}
		models = append(models, &KiroModel{
			ModelID:        m.ModelID,
			ModelName:      m.ModelName,
			Description:    m.Description,
			RateMultiplier: m.RateMultiplier,
			RateUnit:       m.RateUnit,
			MaxInputTokens: maxInputTokens,
		})
	}
	return models, nil
}
