package config

import "testing"

func TestSanitizeOAuthModelAlias_PreservesForkFlag(t *testing.T) {
	cfg := &Config{
		OAuthModelAlias: map[string][]OAuthModelAlias{
			" CoDeX ": {
				{Name: " gpt-5 ", Alias: " g5 ", Fork: true},
				{Name: "gpt-6", Alias: "g6"},
			},
		},
	}

	cfg.SanitizeOAuthModelAlias()

	aliases := cfg.OAuthModelAlias["codex"]
	if len(aliases) != 2 {
		t.Fatalf("expected 2 sanitized aliases, got %d", len(aliases))
	}
	if aliases[0].Name != "gpt-5" || aliases[0].Alias != "g5" || !aliases[0].Fork {
		t.Fatalf("expected first alias to be gpt-5->g5 fork=true, got name=%q alias=%q fork=%v", aliases[0].Name, aliases[0].Alias, aliases[0].Fork)
	}
	if aliases[1].Name != "gpt-6" || aliases[1].Alias != "g6" || aliases[1].Fork {
		t.Fatalf("expected second alias to be gpt-6->g6 fork=false, got name=%q alias=%q fork=%v", aliases[1].Name, aliases[1].Alias, aliases[1].Fork)
	}
}

func TestSanitizeOAuthModelAlias_AllowsMultipleAliasesForSameName(t *testing.T) {
	cfg := &Config{
		OAuthModelAlias: map[string][]OAuthModelAlias{
			"antigravity": {
				{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5-20251101", Fork: true},
				{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5-20251101-thinking", Fork: true},
				{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5", Fork: true},
			},
		},
	}

	cfg.SanitizeOAuthModelAlias()

	aliases := cfg.OAuthModelAlias["antigravity"]
	expected := []OAuthModelAlias{
		{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5-20251101", Fork: true},
		{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5-20251101-thinking", Fork: true},
		{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5", Fork: true},
	}
	if len(aliases) != len(expected) {
		t.Fatalf("expected %d sanitized aliases, got %d", len(expected), len(aliases))
	}
	for i, exp := range expected {
		if aliases[i].Name != exp.Name || aliases[i].Alias != exp.Alias || aliases[i].Fork != exp.Fork {
			t.Fatalf("expected alias %d to be name=%q alias=%q fork=%v, got name=%q alias=%q fork=%v", i, exp.Name, exp.Alias, exp.Fork, aliases[i].Name, aliases[i].Alias, aliases[i].Fork)
		}
	}
}

func TestSanitizeKiroConfig(t *testing.T) {
	cfg := &Config{
		Kiro: KiroConfig{
			PreferredEndpoint: " AMAZONQ ",
			Fingerprint: KiroFingerprintConfig{
				UserAgent:    " Kiro ",
				AmzUserAgent: " aws-toolkit ",
			},
			Auths: []KiroKey{
				{},
				{
					Email:             " user@example.com ",
					RefreshToken:      " refresh ",
					PreferredEndpoint: " codewhisperer ",
					Prefix:            "/team/",
					ProxyURL:          " direct ",
					ExcludedModels:    []string{" kiro-claude-opus-4-5 ", ""},
				},
				{
					Email:             "invalid@example.com",
					PreferredEndpoint: "invalid",
					Prefix:            "team/a",
				},
			},
		},
	}

	cfg.SanitizeKiroConfig()

	if cfg.Kiro.PreferredEndpoint != "amazonq" {
		t.Fatalf("expected preferred endpoint amazonq, got %q", cfg.Kiro.PreferredEndpoint)
	}
	if cfg.Kiro.Fingerprint.UserAgent != "Kiro" || cfg.Kiro.Fingerprint.AmzUserAgent != "aws-toolkit" {
		t.Fatalf("unexpected fingerprint: %#v", cfg.Kiro.Fingerprint)
	}
	if len(cfg.Kiro.Auths) != 2 {
		t.Fatalf("expected 2 non-empty Kiro auths, got %d", len(cfg.Kiro.Auths))
	}
	if got := cfg.Kiro.Auths[0].PreferredEndpoint; got != "codewhisperer" {
		t.Fatalf("expected entry endpoint codewhisperer, got %q", got)
	}
	if got := cfg.Kiro.Auths[0].Prefix; got != "team" {
		t.Fatalf("expected normalized prefix team, got %q", got)
	}
	if got := cfg.Kiro.Auths[0].ProxyURL; got != "direct" {
		t.Fatalf("expected proxy direct, got %q", got)
	}
	if len(cfg.Kiro.Auths[0].ExcludedModels) != 1 || cfg.Kiro.Auths[0].ExcludedModels[0] != "kiro-claude-opus-4-5" {
		t.Fatalf("unexpected excluded models: %#v", cfg.Kiro.Auths[0].ExcludedModels)
	}
	if got := cfg.Kiro.Auths[1].PreferredEndpoint; got != "" {
		t.Fatalf("expected invalid endpoint to be cleared, got %q", got)
	}
	if got := cfg.Kiro.Auths[1].Prefix; got != "" {
		t.Fatalf("expected invalid prefix to be cleared, got %q", got)
	}
}
