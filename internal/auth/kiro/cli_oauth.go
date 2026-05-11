package kiro

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	kiroCLICallbackAddr      = "localhost:3128"
	kiroCLITokenRedirectURI  = "http://localhost:3128/oauth/callback?login_option=google"
	kiroCLISignInURLTemplate = "https://app.kiro.dev/signin?state=%s&code_challenge=%s&code_challenge_method=S256&redirect_uri=http%%3A%%2F%%2Flocalhost%%3A3128&redirect_from=kirocli"
	kiroCLITokenEndpoint     = "https://prod.us-east-1.auth.desktop.kiro.dev/oauth/token"
	kiroCLIRefreshEndpoint   = "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken"
	kiroCLIAuthTimeout       = 10 * time.Minute
)

// KiroCLIOAuth implements Kiro's native localhost OAuth flow.
type KiroCLIOAuth struct {
	httpClient *http.Client
	cfg        *config.Config
}

type cliCallbackResult struct {
	Code  string
	State string
	Err   string
}

type cliTokenExchangeRequest struct {
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
	RedirectURI  string `json:"redirect_uri"`
}

// NewKiroCLIOAuth constructs a native Kiro CLI OAuth client.
func NewKiroCLIOAuth(cfg *config.Config) *KiroCLIOAuth {
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		client = util.SetProxy(&cfg.SDKConfig, client)
	}
	return &KiroCLIOAuth{httpClient: client, cfg: cfg}
}

// LoginWithCLI runs the Kiro CLI OAuth flow.
func (o *KiroCLIOAuth) LoginWithCLI(ctx context.Context, noBrowser bool) (*KiroTokenData, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	state, err := generateKiroCLIState()
	if err != nil {
		return nil, err
	}
	verifier, challenge, err := generateKiroCLIPKCE()
	if err != nil {
		return nil, err
	}

	callbackCtx, cancel := context.WithTimeout(ctx, kiroCLIAuthTimeout)
	defer cancel()
	resultCh, shutdown, err := o.startCallbackServer(callbackCtx, state)
	if err != nil {
		return nil, err
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		_ = shutdown(shutdownCtx)
	}()

	signInURL := o.signInURL(state, challenge)
	if noBrowser {
		util.PrintSSHTunnelInstructions(3128)
		fmt.Printf("Visit the following URL to continue Kiro authentication:\n%s\n", signInURL)
	} else if !browser.IsAvailable() {
		log.Warn("No browser available; please open the Kiro login URL manually")
		util.PrintSSHTunnelInstructions(3128)
		fmt.Printf("Visit the following URL to continue Kiro authentication:\n%s\n", signInURL)
	} else if errOpen := browser.OpenURL(signInURL); errOpen != nil {
		log.Warnf("Failed to open browser automatically: %v", errOpen)
		util.PrintSSHTunnelInstructions(3128)
		fmt.Printf("Visit the following URL to continue Kiro authentication:\n%s\n", signInURL)
	}

	fmt.Println("Waiting for Kiro authentication callback...")
	select {
	case <-callbackCtx.Done():
		return nil, fmt.Errorf("kiro cli authentication timed out")
	case result := <-resultCh:
		if result.Err != "" {
			return nil, fmt.Errorf("kiro cli authentication failed: %s", result.Err)
		}
		if result.State != state {
			return nil, fmt.Errorf("kiro cli authentication failed: invalid state")
		}
		if strings.TrimSpace(result.Code) == "" {
			return nil, fmt.Errorf("kiro cli authentication failed: missing code")
		}
		return o.exchangeCodeForToken(ctx, result.Code, verifier)
	}
}

// RefreshToken refreshes a Kiro CLI token.
func (o *KiroCLIOAuth) RefreshToken(ctx context.Context, refreshToken string) (*KiroTokenData, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is empty")
	}
	body, err := json.Marshal(map[string]string{"refreshToken": refreshToken})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.refreshEndpoint(), strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Kiro-CLI")
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kiro cli refresh request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kiro cli refresh failed: status %d: %s", resp.StatusCode, string(respBody))
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
		AuthMethod:   "kiro-cli",
		Provider:     "Kiro CLI",
		Region:       DefaultKiroRegion,
	}, nil
}

func (o *KiroCLIOAuth) startCallbackServer(ctx context.Context, expectedState string) (<-chan cliCallbackResult, func(context.Context) error, error) {
	listener, err := net.Listen("tcp", kiroCLICallbackAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to bind callback listener on %s: %w", kiroCLICallbackAddr, err)
	}
	resultCh := make(chan cliCallbackResult, 1)
	server := &http.Server{ReadHeaderTimeout: 10 * time.Second}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/callback", func(w http.ResponseWriter, r *http.Request) {
		res := cliCallbackResult{
			Code:  strings.TrimSpace(r.URL.Query().Get("code")),
			State: strings.TrimSpace(r.URL.Query().Get("state")),
			Err:   strings.TrimSpace(r.URL.Query().Get("error")),
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if res.Err == "" && res.Code != "" && res.State == expectedState {
			_, _ = w.Write([]byte("<h1>Kiro login successful</h1><p>You can close this window.</p>"))
		} else {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("<h1>Kiro login failed</h1><p>Please check the CLI output.</p>"))
		}
		select {
		case resultCh <- res:
		default:
		}
	})
	server.Handler = mux
	go func() {
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)
		}()
		if errServe := server.Serve(listener); errServe != nil && errServe != http.ErrServerClosed {
			select {
			case resultCh <- cliCallbackResult{Err: errServe.Error()}:
			default:
			}
		}
	}()
	return resultCh, server.Shutdown, nil
}

func (o *KiroCLIOAuth) exchangeCodeForToken(ctx context.Context, code, verifier string) (*KiroTokenData, error) {
	payload := cliTokenExchangeRequest{
		Code:         code,
		CodeVerifier: verifier,
		RedirectURI:  kiroCLITokenRedirectURI,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.tokenEndpoint(), strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Kiro-CLI")
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kiro cli token exchange failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kiro cli token exchange failed: status %d: %s", resp.StatusCode, string(respBody))
	}
	var tokenResp KiroTokenResponse
	if err = json.Unmarshal(respBody, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse Kiro token response: %w", err)
	}
	email := ExtractEmailFromJWT(tokenResp.AccessToken)
	return &KiroTokenData{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ProfileArn:   tokenResp.ProfileArn,
		ExpiresAt:    ExpiresAtFromSeconds(tokenResp.ExpiresIn),
		AuthMethod:   "kiro-cli",
		Provider:     "Kiro CLI",
		Email:        email,
		Region:       DefaultKiroRegion,
	}, nil
}

func (o *KiroCLIOAuth) signInURL(state, challenge string) string {
	if o.cfg != nil {
		override := o.cfg.GetOAuthEndpointOverride("kiro")
		if override.AuthorizeURL != "" {
			return fmt.Sprintf("%s?state=%s&code_challenge=%s&code_challenge_method=S256&redirect_uri=http%%3A%%2F%%2Flocalhost%%3A3128&redirect_from=kirocli",
				strings.TrimRight(override.AuthorizeURL, "?"), state, challenge)
		}
	}
	return fmt.Sprintf(kiroCLISignInURLTemplate, state, challenge)
}

func (o *KiroCLIOAuth) tokenEndpoint() string {
	if o.cfg != nil {
		override := o.cfg.GetOAuthEndpointOverride("kiro")
		if override.TokenURL != "" {
			return override.TokenURL
		}
		if override.ApiBaseURL != "" {
			return strings.TrimRight(override.ApiBaseURL, "/") + "/oauth/token"
		}
	}
	return kiroCLITokenEndpoint
}

func (o *KiroCLIOAuth) refreshEndpoint() string {
	if o.cfg != nil {
		override := o.cfg.GetOAuthEndpointOverride("kiro")
		if override.RefreshURL != "" {
			return override.RefreshURL
		}
		if override.TokenURL != "" {
			return override.TokenURL
		}
		if override.ApiBaseURL != "" {
			return strings.TrimRight(override.ApiBaseURL, "/") + "/refreshToken"
		}
	}
	return kiroCLIRefreshEndpoint
}

func generateKiroCLIState() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, len(b))
	for i := range b {
		out[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(out), nil
}

func generateKiroCLIPKCE() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}
