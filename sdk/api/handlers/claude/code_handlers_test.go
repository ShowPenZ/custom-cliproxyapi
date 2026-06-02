package claude

import "testing"

func TestKiroClaudeCodeModel(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  string
	}{
		{name: "sonnet alias", model: "sonnet", want: "kiro-claude-sonnet-4-6"},
		{name: "current sonnet", model: "claude-sonnet-4-6", want: "kiro-claude-sonnet-4-6"},
		{name: "dated sonnet 4.5", model: "claude-sonnet-4-5-20250929", want: "kiro-claude-sonnet-4-5"},
		{name: "opus alias", model: "opus", want: "kiro-claude-opus-4-6"},
		{name: "unsupported dated opus falls back", model: "claude-opus-4-7", want: "kiro-claude-opus-4-6"},
		{name: "dated opus 4.5", model: "claude-opus-4-5-20251101", want: "kiro-claude-opus-4-5"},
		{name: "haiku title model", model: "claude-haiku-4-5-20251001", want: "kiro-claude-haiku-4-5"},
		{name: "already kiro", model: "kiro-claude-sonnet-4-5", want: "kiro-claude-sonnet-4-5"},
		{name: "unknown passthrough", model: "custom-model", want: "custom-model"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := kiroClaudeCodeModel(tt.model); got != tt.want {
				t.Fatalf("kiroClaudeCodeModel(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}
