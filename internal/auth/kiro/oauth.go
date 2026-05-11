package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

const kiroAuthEndpoint = "https://prod.us-east-1.auth.desktop.kiro.dev"

// KiroOAuth implements the generic Kiro IDE refresh endpoint used by imported tokens.
type KiroOAuth struct {
	httpClient *http.Client
	cfg        *config.Config
}

// NewKiroOAuth constructs a generic Kiro OAuth helper.
func NewKiroOAuth(cfg *config.Config) *KiroOAuth {
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		client = util.SetProxy(&cfg.SDKConfig, client)
	}
	return &KiroOAuth{httpClient: client, cfg: cfg}
}

// RefreshToken refreshes an imported Kiro token through Kiro's refresh endpoint.
func (o *KiroOAuth) RefreshToken(ctx context.Context, refreshToken string) (*KiroTokenData, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is empty")
	}
	body, err := json.Marshal(map[string]string{"refreshToken": refreshToken})
	if err != nil {
		return nil, err
	}
	refreshURL := kiroAuthEndpoint + "/refreshToken"
	if o.cfg != nil {
		override := o.cfg.GetOAuthEndpointOverride("kiro")
		switch {
		case override.RefreshURL != "":
			refreshURL = override.RefreshURL
		case override.TokenURL != "":
			refreshURL = override.TokenURL
		case override.ApiBaseURL != "":
			refreshURL = strings.TrimRight(override.ApiBaseURL, "/") + "/refreshToken"
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, refreshURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", "KiroIDE")
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kiro refresh request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kiro refresh failed: status %d: %s", resp.StatusCode, string(respBody))
	}
	var tokenResp KiroTokenResponse
	if err = json.Unmarshal(respBody, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse Kiro refresh response: %w", err)
	}
	return &KiroTokenData{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ProfileArn:   tokenResp.ProfileArn,
		ExpiresAt:    ExpiresAtFromSeconds(tokenResp.ExpiresIn),
		AuthMethod:   "social",
		Region:       DefaultKiroRegion,
	}, nil
}
