package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// KiroAuthenticator implements Kiro OAuth login, token import, and refresh.
type KiroAuthenticator struct{}

// NewKiroAuthenticator constructs a Kiro authenticator.
func NewKiroAuthenticator() Authenticator { return &KiroAuthenticator{} }

// Provider returns the provider key.
func (KiroAuthenticator) Provider() string { return "kiro" }

// RefreshLead asks the auth manager to refresh Kiro tokens before expiry.
func (KiroAuthenticator) RefreshLead() *time.Duration {
	d := 20 * time.Minute
	return &d
}

// Login performs AWS SSO OIDC login for Kiro with Builder ID or IDC.
func (a *KiroAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("kiro auth: configuration is required")
	}
	applyKiroFingerprintConfig(cfg)

	var idcOpts *kiroauth.IDCLoginOptions
	if opts != nil && opts.Metadata != nil {
		if startURL := opts.Metadata["start-url"]; startURL != "" {
			idcOpts = &kiroauth.IDCLoginOptions{
				StartURL:      startURL,
				Region:        opts.Metadata["region"],
				UseDeviceCode: opts.Metadata["flow"] == "device",
			}
		}
	}

	tokenData, err := kiroauth.NewSSOOIDCClient(cfg).LoginWithMethodSelection(ctx, idcOpts)
	if err != nil {
		return nil, fmt.Errorf("kiro aws login failed: %w", err)
	}
	return a.createAuthRecord(tokenData, "aws")
}

// LoginWithAuthCode performs AWS Builder ID authorization-code login for Kiro.
func (a *KiroAuthenticator) LoginWithAuthCode(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("kiro auth: configuration is required")
	}
	applyKiroFingerprintConfig(cfg)
	tokenData, err := kiroauth.NewSSOOIDCClient(cfg).LoginWithBuilderIDAuthCode(ctx)
	if err != nil {
		return nil, fmt.Errorf("kiro aws auth-code login failed: %w", err)
	}
	return a.createAuthRecord(tokenData, "aws")
}

// LoginWithCLI performs native Kiro CLI OAuth.
func (a *KiroAuthenticator) LoginWithCLI(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("kiro auth: configuration is required")
	}
	applyKiroFingerprintConfig(cfg)
	noBrowser := false
	if opts != nil {
		noBrowser = opts.NoBrowser
	}
	tokenData, err := kiroauth.NewKiroCLIOAuth(cfg).LoginWithCLI(ctx, noBrowser)
	if err != nil {
		return nil, fmt.Errorf("kiro cli login failed: %w", err)
	}
	return a.createAuthRecord(tokenData, "cli")
}

// ImportFromKiroIDE imports the current Kiro IDE token file.
func (a *KiroAuthenticator) ImportFromKiroIDE(ctx context.Context, cfg *config.Config) (*coreauth.Auth, error) {
	applyKiroFingerprintConfig(cfg)
	tokenData, err := kiroauth.LoadKiroIDEToken()
	if err != nil {
		return nil, fmt.Errorf("failed to load Kiro IDE token: %w", err)
	}
	if tokenData.Email == "" {
		tokenData.Email = kiroauth.ExtractEmailFromJWT(tokenData.AccessToken)
	}
	return a.createAuthRecord(tokenData, "ide-import")
}

// Refresh refreshes a Kiro auth record.
func (a *KiroAuthenticator) Refresh(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if auth == nil || auth.Metadata == nil {
		return nil, fmt.Errorf("kiro auth: invalid auth record")
	}
	applyKiroFingerprintConfig(cfg)
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = map[string]any{}
	}
	refreshToken, _ := auth.Metadata["refresh_token"].(string)
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return updated, nil
	}
	authMethod, _ := auth.Metadata["auth_method"].(string)
	clientIDHash, _ := auth.Metadata["client_id_hash"].(string)
	clientID, _ := updated.Metadata["client_id"].(string)
	clientSecret, _ := updated.Metadata["client_secret"].(string)
	startURL, _ := updated.Metadata["start_url"].(string)
	region, _ := updated.Metadata["region"].(string)

	if clientIDHash != "" {
		if clientID, clientSecret, errLoad := kiroauth.LoadDeviceRegistrationCredentials(clientIDHash); errLoad == nil {
			if updated.Metadata["client_id"] == nil {
				updated.Metadata["client_id"] = clientID
			}
			if updated.Metadata["client_secret"] == nil {
				updated.Metadata["client_secret"] = clientSecret
			}
		}
	}
	if clientID == "" {
		clientID, _ = updated.Metadata["client_id"].(string)
	}
	if clientSecret == "" {
		clientSecret, _ = updated.Metadata["client_secret"].(string)
	}

	var tokenData *kiroauth.KiroTokenData
	var err error
	switch {
	case clientID != "" && clientSecret != "" && authMethod == "idc" && region != "":
		tokenData, err = kiroauth.NewSSOOIDCClient(cfg).RefreshTokenWithRegion(ctx, clientID, clientSecret, refreshToken, region, startURL)
	case clientID != "" && clientSecret != "" && (authMethod == "builder-id" || authMethod == "idc"):
		tokenData, err = kiroauth.NewSSOOIDCClient(cfg).RefreshToken(ctx, clientID, clientSecret, refreshToken)
	case kiroauth.IsKiroCLIAuthMethod(authMethod):
		tokenData, err = kiroauth.NewKiroCLIOAuth(cfg).RefreshToken(ctx, refreshToken)
	default:
		tokenData, err = kiroauth.NewKiroOAuth(cfg).RefreshToken(ctx, refreshToken)
	}
	if err != nil {
		return nil, fmt.Errorf("kiro token refresh failed: %w", err)
	}

	now := time.Now().UTC()
	updated.Metadata["type"] = "kiro"
	updated.Metadata["access_token"] = tokenData.AccessToken
	if tokenData.RefreshToken != "" {
		updated.Metadata["refresh_token"] = tokenData.RefreshToken
	}
	if tokenData.ProfileArn != "" {
		updated.Metadata["profile_arn"] = tokenData.ProfileArn
	}
	if tokenData.ExpiresAt != "" {
		updated.Metadata["expires_at"] = tokenData.ExpiresAt
		if expiresAt, errParse := time.Parse(time.RFC3339, tokenData.ExpiresAt); errParse == nil {
			updated.NextRefreshAfter = expiresAt.Add(-20 * time.Minute)
		}
	}
	if tokenData.AuthMethod != "" {
		updated.Metadata["auth_method"] = tokenData.AuthMethod
	}
	if tokenData.Region != "" {
		updated.Metadata["region"] = tokenData.Region
	}
	if tokenData.ClientID != "" {
		updated.Metadata["client_id"] = tokenData.ClientID
	}
	if tokenData.ClientSecret != "" {
		updated.Metadata["client_secret"] = tokenData.ClientSecret
	}
	if tokenData.StartURL != "" {
		updated.Metadata["start_url"] = tokenData.StartURL
	}
	updated.Metadata["last_refresh"] = now.Format(time.RFC3339)
	updated.LastRefreshedAt = now
	updated.UpdatedAt = now
	return updated, nil
}

func (a *KiroAuthenticator) createAuthRecord(tokenData *kiroauth.KiroTokenData, source string) (*coreauth.Auth, error) {
	if tokenData == nil {
		return nil, fmt.Errorf("kiro auth: token data is nil")
	}
	if tokenData.Email == "" {
		tokenData.Email = kiroauth.ExtractEmailFromJWT(tokenData.AccessToken)
	}
	expiresAt, err := time.Parse(time.RFC3339, tokenData.ExpiresAt)
	if err != nil {
		expiresAt = time.Now().Add(time.Hour)
		tokenData.ExpiresAt = expiresAt.Format(time.RFC3339)
	}

	idPart := extractKiroIdentifier(tokenData.Email, tokenData.ProfileArn, tokenData.ClientID)
	label := "kiro-" + strings.Trim(kiroauth.SanitizeEmailForFilename(source), "-_")
	if label == "kiro-" {
		label = "kiro"
	}
	fileName := fmt.Sprintf("%s-%s.json", label, idPart)
	now := time.Now().UTC()

	metadata := map[string]any{
		"type":           "kiro",
		"access_token":   tokenData.AccessToken,
		"refresh_token":  tokenData.RefreshToken,
		"profile_arn":    tokenData.ProfileArn,
		"expires_at":     tokenData.ExpiresAt,
		"auth_method":    tokenData.AuthMethod,
		"provider":       tokenData.Provider,
		"client_id":      tokenData.ClientID,
		"client_secret":  tokenData.ClientSecret,
		"client_id_hash": tokenData.ClientIDHash,
		"email":          tokenData.Email,
		"region":         tokenData.Region,
		"start_url":      tokenData.StartURL,
	}
	attributes := map[string]string{
		"profile_arn": tokenData.ProfileArn,
		"source":      source,
		"email":       tokenData.Email,
		"region":      tokenData.Region,
	}

	return &coreauth.Auth{
		ID:               fileName,
		Provider:         "kiro",
		FileName:         fileName,
		Label:            label,
		Status:           coreauth.StatusActive,
		CreatedAt:        now,
		UpdatedAt:        now,
		Metadata:         metadata,
		Attributes:       attributes,
		NextRefreshAfter: expiresAt.Add(-20 * time.Minute),
	}, nil
}

func extractKiroIdentifier(email, profileARN, clientID string) string {
	if id := kiroauth.SanitizeEmailForFilename(email); id != "" {
		return id
	}
	if profileARN != "" {
		parts := strings.Split(profileARN, "/")
		if id := kiroauth.SanitizeEmailForFilename(parts[len(parts)-1]); id != "" {
			return id
		}
	}
	if id := kiroauth.SanitizeEmailForFilename(clientID); id != "" {
		return id
	}
	return fmt.Sprintf("%d", time.Now().UnixNano()%100000)
}

func applyKiroFingerprintConfig(cfg *config.Config) {
	if cfg == nil {
		return
	}
	fp := cfg.Kiro.Fingerprint
	kiroauth.SetGlobalFingerprintConfig(&kiroauth.FingerprintConfig{
		OIDCSDKVersion:      fp.OIDCSDKVersion,
		RuntimeSDKVersion:   fp.RuntimeSDKVersion,
		StreamingSDKVersion: fp.StreamingSDKVersion,
		OSType:              fp.OSType,
		OSVersion:           fp.OSVersion,
		NodeVersion:         fp.NodeVersion,
		KiroVersion:         fp.KiroVersion,
		KiroHash:            fp.KiroHash,
	})
}
