package codex

import "testing"

func TestAccountGroupFromPlanType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		planType string
		want     string
	}{
		{name: "pro", planType: "pro", want: "pro"},
		{name: "prolite", planType: "prolite", want: "pro"},
		{name: "plus", planType: "plus", want: "plus"},
		{name: "team", planType: "team", want: "team"},
		{name: "unknown stays empty", planType: "unknown", want: ""},
		{name: "custom stays custom", planType: "enterprise-lite", want: "enterprise-lite"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := AccountGroupFromPlanType(tt.planType); got != tt.want {
				t.Fatalf("AccountGroupFromPlanType(%q) = %q, want %q", tt.planType, got, tt.want)
			}
		})
	}
}
