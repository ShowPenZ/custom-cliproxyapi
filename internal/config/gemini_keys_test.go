package config

import "testing"

func TestSanitizeGeminiKeys_KeepsSameAPIKeyAcrossDifferentBaseURLs(t *testing.T) {
	cfg := &Config{
		GeminiKey: []GeminiKey{
			{APIKey: "dup-key", BaseURL: "https://a.example.com"},
			{APIKey: "dup-key", BaseURL: "https://b.example.com"},
			{APIKey: "dup-key", BaseURL: "https://a.example.com"},
		},
	}

	cfg.SanitizeGeminiKeys()

	if len(cfg.GeminiKey) != 2 {
		t.Fatalf("gemini keys length = %d, want 2", len(cfg.GeminiKey))
	}
	if got := cfg.GeminiKey[0].BaseURL; got != "https://a.example.com" {
		t.Fatalf("first base-url = %q, want %q", got, "https://a.example.com")
	}
	if got := cfg.GeminiKey[1].BaseURL; got != "https://b.example.com" {
		t.Fatalf("second base-url = %q, want %q", got, "https://b.example.com")
	}
}
