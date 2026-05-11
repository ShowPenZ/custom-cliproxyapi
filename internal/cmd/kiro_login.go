package cmd

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoKiroCLILogin triggers native Kiro CLI OAuth and saves the resulting auth file.
func DoKiroCLILogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}
	manager := newAuthManager()
	authenticator := &sdkAuth.KiroAuthenticator{}
	record, err := authenticator.LoginWithCLI(context.Background(), cfg, &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		Metadata:  map[string]string{},
		Prompt:    options.Prompt,
	})
	if err != nil {
		log.Errorf("Kiro CLI authentication failed: %v", err)
		fmt.Println("\nTroubleshooting:")
		fmt.Println("1. Complete the browser login flow")
		fmt.Println("2. Ensure callback port 3128 is available")
		fmt.Println("3. If callback fails, try --kiro-import after logging in via Kiro IDE")
		return
	}
	savedPath, err := manager.SaveAuth(context.Background(), record, cfg)
	if err != nil {
		log.Errorf("Failed to save Kiro auth: %v", err)
		return
	}
	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Authenticated as %s\n", record.Label)
	}
	fmt.Println("Kiro CLI authentication successful!")
}

// DoKiroImport imports token material from Kiro IDE and saves it as a proxy auth file.
func DoKiroImport(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}
	manager := newAuthManager()
	authenticator := &sdkAuth.KiroAuthenticator{}
	record, err := authenticator.ImportFromKiroIDE(context.Background(), cfg)
	if err != nil {
		log.Errorf("Kiro token import failed: %v", err)
		fmt.Println("\nMake sure you have logged in to Kiro IDE first.")
		return
	}
	savedPath, err := manager.SaveAuth(context.Background(), record, cfg)
	if err != nil {
		log.Errorf("Failed to save Kiro auth: %v", err)
		return
	}
	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Imported as %s\n", record.Label)
	}
	fmt.Println("Kiro token import successful!")
}

// DoKiroIDCLogin triggers AWS Identity Center login and saves the resulting auth file.
func DoKiroIDCLogin(cfg *config.Config, options *LoginOptions, startURL, region, flow string) {
	if options == nil {
		options = &LoginOptions{}
	}
	if startURL == "" {
		log.Errorf("Kiro IDC login requires --kiro-idc-start-url")
		fmt.Println("\nUsage: --kiro-idc-login --kiro-idc-start-url https://d-xxx.awsapps.com/start")
		return
	}

	manager := newAuthManager()
	record, err := sdkAuth.NewKiroAuthenticator().Login(context.Background(), cfg, &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		Metadata: map[string]string{
			"start-url": startURL,
			"region":    region,
			"flow":      flow,
		},
		Prompt: options.Prompt,
	})
	if err != nil {
		log.Errorf("Kiro IDC authentication failed: %v", err)
		fmt.Println("\nTroubleshooting:")
		fmt.Println("1. Make sure your IDC Start URL is correct")
		fmt.Println("2. Complete the authorization in the browser")
		fmt.Println("3. If auth code flow fails, try: --kiro-idc-flow device")
		return
	}

	savedPath, err := manager.SaveAuth(context.Background(), record, cfg)
	if err != nil {
		log.Errorf("Failed to save Kiro auth: %v", err)
		return
	}
	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Authenticated as %s\n", record.Label)
	}
	fmt.Println("Kiro IDC authentication successful!")
}
