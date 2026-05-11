package kiro

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	ssoOIDCEndpoint   = "https://oidc.us-east-1.amazonaws.com"
	builderIDStartURL = "https://view.awsapps.com/start"
	defaultIDCRegion  = "us-east-1"
	pollInterval      = 5 * time.Second

	authCodeCallbackPath = "/oauth/callback"
	authCodeCallbackPort = 19877
)

var (
	ErrAuthorizationPending = errors.New("authorization_pending")
	ErrSlowDown             = errors.New("slow_down")
)

type SSOOIDCClient struct {
	httpClient *http.Client
	cfg        *config.Config
}

// NewSSOOIDCClient creates a new SSO OIDC client.
func NewSSOOIDCClient(cfg *config.Config) *SSOOIDCClient {
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		client = util.SetProxy(&cfg.SDKConfig, client)
	}
	return &SSOOIDCClient{
		httpClient: client,
		cfg:        cfg,
	}
}

func (c *SSOOIDCClient) getOIDCEndpointOverride() config.OAuthEndpointConfig {
	if c.cfg != nil {
		return c.cfg.GetOAuthEndpointOverride("kiro")
	}
	return config.OAuthEndpointConfig{}
}

func (c *SSOOIDCClient) getOIDCEndpointWithOverride(region string) string {
	if override := c.getOIDCEndpointOverride(); override.ApiBaseURL != "" {
		return strings.TrimRight(override.ApiBaseURL, "/")
	}
	return getOIDCEndpoint(region)
}

// RegisterClientResponse from AWS SSO OIDC.
type RegisterClientResponse struct {
	ClientID              string `json:"clientId"`
	ClientSecret          string `json:"clientSecret"`
	ClientIDIssuedAt      int64  `json:"clientIdIssuedAt"`
	ClientSecretExpiresAt int64  `json:"clientSecretExpiresAt"`
}

// StartDeviceAuthResponse from AWS SSO OIDC.
type StartDeviceAuthResponse struct {
	DeviceCode              string `json:"deviceCode"`
	UserCode                string `json:"userCode"`
	VerificationURI         string `json:"verificationUri"`
	VerificationURIComplete string `json:"verificationUriComplete"`
	ExpiresIn               int    `json:"expiresIn"`
	Interval                int    `json:"interval"`
}

// CreateTokenResponse from AWS SSO OIDC.
type CreateTokenResponse struct {
	AccessToken  string `json:"accessToken"`
	TokenType    string `json:"tokenType"`
	ExpiresIn    int    `json:"expiresIn"`
	RefreshToken string `json:"refreshToken"`
}

func getOIDCEndpoint(region string) string {
	if region == "" {
		region = defaultIDCRegion
	}
	return fmt.Sprintf("https://oidc.%s.amazonaws.com", region)
}

// RegisterClientWithRegion registers a new OIDC client with AWS using a specific region.
func (c *SSOOIDCClient) RegisterClientWithRegion(ctx context.Context, region string) (*RegisterClientResponse, error) {
	endpoint := c.getOIDCEndpointWithOverride(region)

	payload := map[string]interface{}{
		"clientName": "Kiro IDE",
		"clientType": "public",
		"scopes":     []string{"codewhisperer:completions", "codewhisperer:analysis", "codewhisperer:conversations", "codewhisperer:transformations", "codewhisperer:taskassist"},
		"grantTypes": []string{"urn:ietf:params:oauth:grant-type:device_code", "refresh_token"},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/client/register", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	SetOIDCHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("register client failed (status %d): %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("register client failed (status %d)", resp.StatusCode)
	}

	var result RegisterClientResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// StartDeviceAuthorizationWithIDC starts the device authorization flow for IDC.
func (c *SSOOIDCClient) StartDeviceAuthorizationWithIDC(ctx context.Context, clientID, clientSecret, startURL, region string) (*StartDeviceAuthResponse, error) {
	endpoint := c.getOIDCEndpointWithOverride(region)

	payload := map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"startUrl":     startURL,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/device_authorization", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	SetOIDCHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("start device auth failed (status %d): %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("start device auth failed (status %d)", resp.StatusCode)
	}

	var result StartDeviceAuthResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// CreateTokenWithRegion polls for the access token after user authorization using a specific region.
func (c *SSOOIDCClient) CreateTokenWithRegion(ctx context.Context, clientID, clientSecret, deviceCode, region string) (*CreateTokenResponse, error) {
	endpoint := c.getOIDCEndpointWithOverride(region)

	payload := map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"deviceCode":   deviceCode,
		"grantType":    "urn:ietf:params:oauth:grant-type:device_code",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/token", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	SetOIDCHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusBadRequest {
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(respBody, &errResp) == nil {
			if errResp.Error == "authorization_pending" {
				return nil, ErrAuthorizationPending
			}
			if errResp.Error == "slow_down" {
				return nil, ErrSlowDown
			}
		}
		log.Debugf("create token failed: %s", string(respBody))
		return nil, fmt.Errorf("create token failed")
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("create token failed (status %d): %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("create token failed (status %d)", resp.StatusCode)
	}

	var result CreateTokenResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// RefreshTokenWithRegion refreshes an access token using the refresh token with a specific OIDC region.
func (c *SSOOIDCClient) RefreshTokenWithRegion(ctx context.Context, clientID, clientSecret, refreshToken, region, startURL string) (*KiroTokenData, error) {
	if region == "" {
		region = defaultIDCRegion
	}
	endpoint := c.getOIDCEndpointWithOverride(region)

	payload := map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"refreshToken": refreshToken,
		"grantType":    "refresh_token",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/token", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	SetOIDCHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		log.Warnf("IDC token refresh failed (status %d): %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("token refresh failed (status %d)", resp.StatusCode)
	}

	var result CreateTokenResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	expiresAt := time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)

	return &KiroTokenData{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		ExpiresAt:    expiresAt.Format(time.RFC3339),
		AuthMethod:   "idc",
		Provider:     "AWS",
		ClientID:     clientID,
		ClientSecret: clientSecret,
		StartURL:     startURL,
		Region:       region,
	}, nil
}

// LoginWithIDC performs the full device code flow for AWS Identity Center (IDC).
func (c *SSOOIDCClient) LoginWithIDC(ctx context.Context, startURL, region string) (*KiroTokenData, error) {
	fmt.Println("\nKiro Authentication (AWS Identity Center)")

	fmt.Println("\nRegistering client...")
	regResp, err := c.RegisterClientWithRegion(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("failed to register client: %w", err)
	}
	log.Debugf("Client registered: %s", regResp.ClientID)

	fmt.Println("Starting device authorization...")
	authResp, err := c.StartDeviceAuthorizationWithIDC(ctx, regResp.ClientID, regResp.ClientSecret, startURL, region)
	if err != nil {
		return nil, fmt.Errorf("failed to start device auth: %w", err)
	}

	fmt.Println("\n============================================================")
	fmt.Println("  Confirm the following code in the browser:")
	fmt.Printf("  Code: %s\n", authResp.UserCode)
	fmt.Println("============================================================")
	fmt.Printf("\n  Open this URL: %s\n\n", authResp.VerificationURIComplete)

	c.setBrowserMode()
	if err := browser.OpenURL(authResp.VerificationURIComplete); err != nil {
		log.Warnf("Could not open browser automatically: %v", err)
		fmt.Println("  Please open the URL manually in your browser.")
	} else {
		fmt.Println("  Browser opened automatically.")
	}

	fmt.Println("Waiting for authorization...")
	interval := pollInterval
	if authResp.Interval > 0 {
		interval = time.Duration(authResp.Interval) * time.Second
	}
	deadline := time.Now().Add(time.Duration(authResp.ExpiresIn) * time.Second)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			browser.CloseBrowser()
			return nil, ctx.Err()
		case <-time.After(interval):
			tokenResp, err := c.CreateTokenWithRegion(ctx, regResp.ClientID, regResp.ClientSecret, authResp.DeviceCode, region)
			if err != nil {
				if errors.Is(err, ErrAuthorizationPending) {
					fmt.Print(".")
					continue
				}
				if errors.Is(err, ErrSlowDown) {
					interval += 5 * time.Second
					continue
				}
				browser.CloseBrowser()
				return nil, fmt.Errorf("token creation failed: %w", err)
			}

			fmt.Println("\n\nAuthorization successful.")
			_ = browser.CloseBrowser()

			fmt.Println("Fetching profile information...")
			profileArn := c.FetchProfileArn(ctx, tokenResp.AccessToken, regResp.ClientID, tokenResp.RefreshToken)
			email := FetchUserEmailWithFallback(ctx, c.cfg, tokenResp.AccessToken, regResp.ClientID, tokenResp.RefreshToken, "idc")
			if email != "" {
				fmt.Printf("  Logged in as: %s\n", email)
			}

			expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
			return &KiroTokenData{
				AccessToken:  tokenResp.AccessToken,
				RefreshToken: tokenResp.RefreshToken,
				ProfileArn:   profileArn,
				ExpiresAt:    expiresAt.Format(time.RFC3339),
				AuthMethod:   "idc",
				Provider:     "AWS",
				ClientID:     regResp.ClientID,
				ClientSecret: regResp.ClientSecret,
				Email:        email,
				StartURL:     startURL,
				Region:       region,
			}, nil
		}
	}

	_ = browser.CloseBrowser()
	return nil, fmt.Errorf("authorization timed out")
}

// IDCLoginOptions holds optional parameters for IDC login.
type IDCLoginOptions struct {
	StartURL      string
	Region        string
	UseDeviceCode bool
}

// LoginWithMethodSelection prompts the user to select between Builder ID and IDC, then performs the login.
func (c *SSOOIDCClient) LoginWithMethodSelection(ctx context.Context, opts *IDCLoginOptions) (*KiroTokenData, error) {
	if opts != nil && opts.StartURL != "" {
		region := opts.Region
		if region == "" {
			region = defaultIDCRegion
		}
		fmt.Printf("\n  Using IDC with Start URL: %s\n", opts.StartURL)
		fmt.Printf("  Region: %s\n", region)
		if opts.UseDeviceCode {
			return c.LoginWithIDCAndOptions(ctx, opts.StartURL, region)
		}
		return c.LoginWithIDCAuthCode(ctx, opts.StartURL, region)
	}

	return c.LoginWithBuilderID(ctx)
}

// LoginWithIDCAndOptions performs IDC login with the specified region.
func (c *SSOOIDCClient) LoginWithIDCAndOptions(ctx context.Context, startURL, region string) (*KiroTokenData, error) {
	return c.LoginWithIDC(ctx, startURL, region)
}

func (c *SSOOIDCClient) RegisterClient(ctx context.Context) (*RegisterClientResponse, error) {
	return c.RegisterClientWithRegion(ctx, defaultIDCRegion)
}

func (c *SSOOIDCClient) StartDeviceAuthorization(ctx context.Context, clientID, clientSecret string) (*StartDeviceAuthResponse, error) {
	return c.StartDeviceAuthorizationWithIDC(ctx, clientID, clientSecret, builderIDStartURL, defaultIDCRegion)
}

func (c *SSOOIDCClient) CreateToken(ctx context.Context, clientID, clientSecret, deviceCode string) (*CreateTokenResponse, error) {
	return c.CreateTokenWithRegion(ctx, clientID, clientSecret, deviceCode, defaultIDCRegion)
}

func (c *SSOOIDCClient) RefreshToken(ctx context.Context, clientID, clientSecret, refreshToken string) (*KiroTokenData, error) {
	tokenData, err := c.RefreshTokenWithRegion(ctx, clientID, clientSecret, refreshToken, defaultIDCRegion, "")
	if tokenData != nil {
		tokenData.AuthMethod = "builder-id"
	}
	return tokenData, err
}

// LoginWithBuilderID performs the full device code flow for AWS Builder ID.
func (c *SSOOIDCClient) LoginWithBuilderID(ctx context.Context) (*KiroTokenData, error) {
	fmt.Println("\nKiro Authentication (AWS Builder ID)")

	fmt.Println("\nRegistering client...")
	regResp, err := c.RegisterClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to register client: %w", err)
	}
	log.Debugf("Client registered: %s", regResp.ClientID)

	fmt.Println("Starting device authorization...")
	authResp, err := c.StartDeviceAuthorization(ctx, regResp.ClientID, regResp.ClientSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to start device auth: %w", err)
	}

	fmt.Println("\n============================================================")
	fmt.Println("  Open this URL in your browser:")
	fmt.Printf("  %s\n", authResp.VerificationURIComplete)
	fmt.Println("============================================================")
	fmt.Printf("\n  Or go to: %s\n", authResp.VerificationURI)
	fmt.Printf("  And enter code: %s\n\n", authResp.UserCode)

	c.setBrowserMode()
	if err := browser.OpenURL(authResp.VerificationURIComplete); err != nil {
		log.Warnf("Could not open browser automatically: %v", err)
		fmt.Println("  Please open the URL manually in your browser.")
	} else {
		fmt.Println("  Browser opened automatically.")
	}

	fmt.Println("Waiting for authorization...")
	interval := pollInterval
	if authResp.Interval > 0 {
		interval = time.Duration(authResp.Interval) * time.Second
	}
	deadline := time.Now().Add(time.Duration(authResp.ExpiresIn) * time.Second)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			browser.CloseBrowser()
			return nil, ctx.Err()
		case <-time.After(interval):
			tokenResp, err := c.CreateToken(ctx, regResp.ClientID, regResp.ClientSecret, authResp.DeviceCode)
			if err != nil {
				if errors.Is(err, ErrAuthorizationPending) {
					fmt.Print(".")
					continue
				}
				if errors.Is(err, ErrSlowDown) {
					interval += 5 * time.Second
					continue
				}
				browser.CloseBrowser()
				return nil, fmt.Errorf("token creation failed: %w", err)
			}

			fmt.Println("\n\nAuthorization successful.")
			_ = browser.CloseBrowser()

			email := FetchUserEmailWithFallback(ctx, c.cfg, tokenResp.AccessToken, regResp.ClientID, tokenResp.RefreshToken, "builder-id")
			if email != "" {
				fmt.Printf("  Logged in as: %s\n", email)
			}

			expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
			return &KiroTokenData{
				AccessToken:  tokenResp.AccessToken,
				RefreshToken: tokenResp.RefreshToken,
				ProfileArn:   "",
				ExpiresAt:    expiresAt.Format(time.RFC3339),
				AuthMethod:   "builder-id",
				Provider:     "AWS",
				ClientID:     regResp.ClientID,
				ClientSecret: regResp.ClientSecret,
				Email:        email,
				Region:       defaultIDCRegion,
			}, nil
		}
	}

	_ = browser.CloseBrowser()
	return nil, fmt.Errorf("authorization timed out")
}

func (c *SSOOIDCClient) setBrowserMode() {
	if c.cfg != nil {
		browser.SetIncognitoMode(c.cfg.IncognitoBrowser)
		if !c.cfg.IncognitoBrowser {
			log.Info("kiro: using normal browser mode. You may not be able to select a different account.")
		} else {
			log.Debug("kiro: using incognito mode for multi-account support")
		}
		return
	}
	browser.SetIncognitoMode(true)
	log.Debug("kiro: using incognito mode for multi-account support (default)")
}

// FetchUserEmail retrieves the user's email from AWS SSO OIDC userinfo endpoint.
func (c *SSOOIDCClient) FetchUserEmail(ctx context.Context, accessToken string) string {
	email := c.tryUserInfoEndpoint(ctx, accessToken)
	if email != "" {
		return email
	}
	return ExtractEmailFromJWT(accessToken)
}

func (c *SSOOIDCClient) tryUserInfoEndpoint(ctx context.Context, accessToken string) string {
	override := c.getOIDCEndpointOverride()
	userinfoURL := ssoOIDCEndpoint + "/userinfo"
	if override.UserinfoURL != "" {
		userinfoURL = override.UserinfoURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userinfoURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Debugf("userinfo request failed: %v", err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.Debugf("userinfo endpoint returned status %d: %s", resp.StatusCode, string(respBody))
		return ""
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}

	var userInfo struct {
		Email             string `json:"email"`
		Sub               string `json:"sub"`
		PreferredUsername string `json:"preferred_username"`
		Name              string `json:"name"`
	}
	if err := json.Unmarshal(respBody, &userInfo); err != nil {
		return ""
	}
	if userInfo.Email != "" {
		return userInfo.Email
	}
	if userInfo.PreferredUsername != "" && strings.Contains(userInfo.PreferredUsername, "@") {
		return userInfo.PreferredUsername
	}
	return ""
}

// FetchProfileArn fetches the profile ARN from ListAvailableProfiles API.
func (c *SSOOIDCClient) FetchProfileArn(ctx context.Context, accessToken, clientID, refreshToken string) string {
	profileArn := c.tryListAvailableProfiles(ctx, accessToken, clientID, refreshToken)
	if profileArn != "" {
		return profileArn
	}
	return c.tryListProfilesLegacy(ctx, accessToken)
}

func (c *SSOOIDCClient) tryListAvailableProfiles(ctx context.Context, accessToken, clientID, refreshToken string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, GetKiroAPIEndpoint("")+"/ListAvailableProfiles", strings.NewReader("{}"))
	if err != nil {
		return ""
	}

	req.Header.Set("Content-Type", "application/json")
	accountKey := GetAccountKey(clientID, refreshToken)
	setRuntimeHeaders(req, accessToken, accountKey, "")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Debugf("ListAvailableProfiles request failed: %v", err)
		return ""
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		log.Debugf("ListAvailableProfiles failed (status %d): %s", resp.StatusCode, string(respBody))
		return ""
	}

	var result struct {
		Profiles []struct {
			Arn         string `json:"arn"`
			ProfileName string `json:"profileName"`
		} `json:"profiles"`
		NextToken *string `json:"nextToken"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		log.Debugf("ListAvailableProfiles parse error: %v", err)
		return ""
	}
	if len(result.Profiles) > 0 {
		log.Debugf("Found profile: %s (%s)", result.Profiles[0].ProfileName, result.Profiles[0].Arn)
		return result.Profiles[0].Arn
	}
	return ""
}

func (c *SSOOIDCClient) tryListProfilesLegacy(ctx context.Context, accessToken string) string {
	payload := map[string]interface{}{
		"origin": "AI_EDITOR",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return ""
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, GetCodeWhispererLegacyEndpoint(""), strings.NewReader(string(body)))
	if err != nil {
		return ""
	}

	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("x-amz-target", "AmazonCodeWhispererService.ListProfiles")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		log.Debugf("ListProfiles (legacy) failed (status %d): %s", resp.StatusCode, string(respBody))
		return ""
	}

	var result struct {
		Profiles []struct {
			Arn string `json:"arn"`
		} `json:"profiles"`
		ProfileArn string `json:"profileArn"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return ""
	}
	if result.ProfileArn != "" {
		return result.ProfileArn
	}
	if len(result.Profiles) > 0 {
		return result.Profiles[0].Arn
	}
	return ""
}

// RegisterClientForAuthCode registers a new OIDC client for authorization code flow.
func (c *SSOOIDCClient) RegisterClientForAuthCode(ctx context.Context, redirectURI string) (*RegisterClientResponse, error) {
	endpoint := c.getOIDCEndpointWithOverride(defaultIDCRegion)

	payload := map[string]interface{}{
		"clientName":   "Kiro IDE",
		"clientType":   "public",
		"scopes":       []string{"codewhisperer:completions", "codewhisperer:analysis", "codewhisperer:conversations", "codewhisperer:transformations", "codewhisperer:taskassist"},
		"grantTypes":   []string{"authorization_code", "refresh_token"},
		"redirectUris": []string{redirectURI},
		"issuerUrl":    builderIDStartURL,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/client/register", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	SetOIDCHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("register client for auth code failed (status %d): %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("register client failed (status %d)", resp.StatusCode)
	}

	var result RegisterClientResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// RegisterClientForAuthCodeWithIDC registers an IDC OIDC client for authorization code flow.
func (c *SSOOIDCClient) RegisterClientForAuthCodeWithIDC(ctx context.Context, redirectURI, issuerURL, region string) (*RegisterClientResponse, error) {
	endpoint := c.getOIDCEndpointWithOverride(region)

	payload := map[string]interface{}{
		"clientName":   "Kiro IDE",
		"clientType":   "public",
		"scopes":       []string{"codewhisperer:completions", "codewhisperer:analysis", "codewhisperer:conversations", "codewhisperer:transformations", "codewhisperer:taskassist"},
		"grantTypes":   []string{"authorization_code", "refresh_token"},
		"redirectUris": []string{redirectURI},
		"issuerUrl":    issuerURL,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/client/register", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	SetOIDCHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("register client for auth code with IDC failed (status %d): %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("register client failed (status %d)", resp.StatusCode)
	}

	var result RegisterClientResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// AuthCodeCallbackResult contains the result from authorization code callback.
type AuthCodeCallbackResult struct {
	Code  string
	State string
	Error string
}

func (c *SSOOIDCClient) startAuthCodeCallbackServer(ctx context.Context, expectedState string) (string, <-chan AuthCodeCallbackResult, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", authCodeCallbackPort))
	if err != nil {
		log.Warnf("sso oidc: default port %d is busy, falling back to dynamic port", authCodeCallbackPort)
		listener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return "", nil, fmt.Errorf("failed to start callback server: %w", err)
		}
	}

	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d%s", port, authCodeCallbackPath)
	resultChan := make(chan AuthCodeCallbackResult, 1)
	doneChan := make(chan struct{})

	server := &http.Server{ReadHeaderTimeout: 10 * time.Second}
	mux := http.NewServeMux()
	mux.HandleFunc(authCodeCallbackPath, func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")
		errParam := r.URL.Query().Get("error")

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if errParam != "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><title>Login Failed</title></head>
<body><h1>Login Failed</h1><p>Error: %s</p><p>You can close this window.</p></body></html>`, html.EscapeString(errParam))
			resultChan <- AuthCodeCallbackResult{Error: errParam}
			close(doneChan)
			return
		}

		if state != expectedState {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `<!DOCTYPE html>
<html><head><title>Login Failed</title></head>
<body><h1>Login Failed</h1><p>Invalid state parameter</p><p>You can close this window.</p></body></html>`)
			resultChan <- AuthCodeCallbackResult{Error: "state mismatch"}
			close(doneChan)
			return
		}

		fmt.Fprint(w, `<!DOCTYPE html>
<html><head><title>Login Successful</title></head>
<body><h1>Login Successful!</h1><p>You can close this window and return to the terminal.</p>
<script>window.close();</script></body></html>`)
		resultChan <- AuthCodeCallbackResult{Code: code, State: state}
		close(doneChan)
	})
	server.Handler = mux

	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Debugf("auth code callback server error: %v", err)
		}
	}()

	go func() {
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Minute):
		case <-doneChan:
		}
		_ = server.Shutdown(context.Background())
	}()

	return redirectURI, resultChan, nil
}

func generatePKCEForAuthCode() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge, nil
}

func generateStateForAuthCode() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// CreateTokenWithAuthCode exchanges authorization code for tokens.
func (c *SSOOIDCClient) CreateTokenWithAuthCode(ctx context.Context, clientID, clientSecret, code, codeVerifier, redirectURI string) (*CreateTokenResponse, error) {
	return c.CreateTokenWithAuthCodeAndRegion(ctx, clientID, clientSecret, code, codeVerifier, redirectURI, defaultIDCRegion)
}

func (c *SSOOIDCClient) CreateTokenWithAuthCodeAndRegion(ctx context.Context, clientID, clientSecret, code, codeVerifier, redirectURI, region string) (*CreateTokenResponse, error) {
	endpoint := c.getOIDCEndpointWithOverride(region)

	payload := map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"code":         code,
		"codeVerifier": codeVerifier,
		"redirectUri":  redirectURI,
		"grantType":    "authorization_code",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/token", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	SetOIDCHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("create token with auth code failed (status %d): %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("create token failed (status %d)", resp.StatusCode)
	}

	var result CreateTokenResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// LoginWithBuilderIDAuthCode performs the authorization code flow for AWS Builder ID.
func (c *SSOOIDCClient) LoginWithBuilderIDAuthCode(ctx context.Context) (*KiroTokenData, error) {
	fmt.Println("\nKiro Authentication (AWS Builder ID - Auth Code)")

	codeVerifier, codeChallenge, err := generatePKCEForAuthCode()
	if err != nil {
		return nil, fmt.Errorf("failed to generate PKCE: %w", err)
	}
	state, err := generateStateForAuthCode()
	if err != nil {
		return nil, fmt.Errorf("failed to generate state: %w", err)
	}

	fmt.Println("\nStarting callback server...")
	redirectURI, resultChan, err := c.startAuthCodeCallbackServer(ctx, state)
	if err != nil {
		return nil, fmt.Errorf("failed to start callback server: %w", err)
	}
	log.Debugf("Callback server started, redirect URI: %s", redirectURI)

	fmt.Println("Registering client...")
	regResp, err := c.RegisterClientForAuthCode(ctx, redirectURI)
	if err != nil {
		return nil, fmt.Errorf("failed to register client: %w", err)
	}

	endpoint := c.getOIDCEndpointWithOverride(defaultIDCRegion)
	scopes := "codewhisperer:completions,codewhisperer:analysis,codewhisperer:conversations"
	authURL := buildAuthorizationURL(endpoint, regResp.ClientID, redirectURI, scopes, state, codeChallenge)

	fmt.Println("\nOpening browser for authentication...")
	fmt.Printf("\n  URL: %s\n\n", authURL)
	c.setBrowserMode()
	if err := browser.OpenURL(authURL); err != nil {
		log.Warnf("Could not open browser automatically: %v", err)
		fmt.Println("  Please open the URL above in your browser manually.")
	} else {
		fmt.Println("  Browser opened automatically.")
	}

	fmt.Println("\nWaiting for authorization callback...")
	select {
	case <-ctx.Done():
		_ = browser.CloseBrowser()
		return nil, ctx.Err()
	case <-time.After(10 * time.Minute):
		_ = browser.CloseBrowser()
		return nil, fmt.Errorf("authorization timed out")
	case result := <-resultChan:
		if result.Error != "" {
			_ = browser.CloseBrowser()
			return nil, fmt.Errorf("authorization failed: %s", result.Error)
		}
		fmt.Println("\nAuthorization received.")
		_ = browser.CloseBrowser()

		fmt.Println("Exchanging code for tokens...")
		tokenResp, err := c.CreateTokenWithAuthCode(ctx, regResp.ClientID, regResp.ClientSecret, result.Code, codeVerifier, redirectURI)
		if err != nil {
			return nil, fmt.Errorf("failed to exchange code for tokens: %w", err)
		}

		email := FetchUserEmailWithFallback(ctx, c.cfg, tokenResp.AccessToken, regResp.ClientID, tokenResp.RefreshToken, "idc")
		if email != "" {
			fmt.Printf("  Logged in as: %s\n", email)
		}

		expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		return &KiroTokenData{
			AccessToken:  tokenResp.AccessToken,
			RefreshToken: tokenResp.RefreshToken,
			ProfileArn:   "",
			ExpiresAt:    expiresAt.Format(time.RFC3339),
			AuthMethod:   "builder-id",
			Provider:     "AWS",
			ClientID:     regResp.ClientID,
			ClientSecret: regResp.ClientSecret,
			Email:        email,
			Region:       defaultIDCRegion,
		}, nil
	}
}

// LoginWithIDCAuthCode performs the authorization code flow for AWS Identity Center.
func (c *SSOOIDCClient) LoginWithIDCAuthCode(ctx context.Context, startURL, region string) (*KiroTokenData, error) {
	fmt.Println("\nKiro Authentication (AWS IDC - Auth Code)")
	if region == "" {
		region = defaultIDCRegion
	}

	codeVerifier, codeChallenge, err := generatePKCEForAuthCode()
	if err != nil {
		return nil, fmt.Errorf("failed to generate PKCE: %w", err)
	}
	state, err := generateStateForAuthCode()
	if err != nil {
		return nil, fmt.Errorf("failed to generate state: %w", err)
	}

	fmt.Println("\nStarting callback server...")
	redirectURI, resultChan, err := c.startAuthCodeCallbackServer(ctx, state)
	if err != nil {
		return nil, fmt.Errorf("failed to start callback server: %w", err)
	}
	log.Debugf("Callback server started, redirect URI: %s", redirectURI)

	fmt.Println("Registering client...")
	regResp, err := c.RegisterClientForAuthCodeWithIDC(ctx, redirectURI, startURL, region)
	if err != nil {
		return nil, fmt.Errorf("failed to register client: %w", err)
	}

	endpoint := c.getOIDCEndpointWithOverride(region)
	scopes := "codewhisperer:completions,codewhisperer:analysis,codewhisperer:conversations,codewhisperer:transformations,codewhisperer:taskassist"
	authURL := buildAuthorizationURL(endpoint, regResp.ClientID, redirectURI, scopes, state, codeChallenge)

	fmt.Println("\nOpening browser for authentication...")
	fmt.Printf("\n  URL: %s\n\n", authURL)
	c.setBrowserMode()
	if err := browser.OpenURL(authURL); err != nil {
		log.Warnf("Could not open browser automatically: %v", err)
		fmt.Println("  Please open the URL above in your browser manually.")
	} else {
		fmt.Println("  Browser opened automatically.")
	}

	fmt.Println("\nWaiting for authorization callback...")
	select {
	case <-ctx.Done():
		_ = browser.CloseBrowser()
		return nil, ctx.Err()
	case <-time.After(10 * time.Minute):
		_ = browser.CloseBrowser()
		return nil, fmt.Errorf("authorization timed out")
	case result := <-resultChan:
		if result.Error != "" {
			_ = browser.CloseBrowser()
			return nil, fmt.Errorf("authorization failed: %s", result.Error)
		}
		fmt.Println("\nAuthorization received.")
		_ = browser.CloseBrowser()

		fmt.Println("Exchanging code for tokens...")
		tokenResp, err := c.CreateTokenWithAuthCodeAndRegion(ctx, regResp.ClientID, regResp.ClientSecret, result.Code, codeVerifier, redirectURI, region)
		if err != nil {
			return nil, fmt.Errorf("failed to exchange code for tokens: %w", err)
		}

		fmt.Println("Fetching profile information...")
		profileArn := c.FetchProfileArn(ctx, tokenResp.AccessToken, regResp.ClientID, tokenResp.RefreshToken)
		email := FetchUserEmailWithFallback(ctx, c.cfg, tokenResp.AccessToken, regResp.ClientID, tokenResp.RefreshToken, "idc")
		if email != "" {
			fmt.Printf("  Logged in as: %s\n", email)
		}

		expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		return &KiroTokenData{
			AccessToken:  tokenResp.AccessToken,
			RefreshToken: tokenResp.RefreshToken,
			ProfileArn:   profileArn,
			ExpiresAt:    expiresAt.Format(time.RFC3339),
			AuthMethod:   "idc",
			Provider:     "AWS",
			ClientID:     regResp.ClientID,
			ClientSecret: regResp.ClientSecret,
			Email:        email,
			StartURL:     startURL,
			Region:       region,
		}, nil
	}
}

func buildAuthorizationURL(endpoint, clientID, redirectURI, scopes, state, codeChallenge string) string {
	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", clientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("scopes", scopes)
	params.Set("state", state)
	params.Set("code_challenge", codeChallenge)
	params.Set("code_challenge_method", "S256")
	return fmt.Sprintf("%s/authorize?%s", strings.TrimRight(endpoint, "/"), params.Encode())
}
