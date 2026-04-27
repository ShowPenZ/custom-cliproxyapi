package codex

import (
	"fmt"
	"strings"
	"unicode"
)

// CredentialFileName returns the filename used to persist Codex OAuth credentials.
// When planType is available (e.g. "plus", "team"), it is appended after the email
// as a suffix to disambiguate subscriptions.
func CredentialFileName(email, planType, hashAccountID string, includeProviderPrefix bool) string {
	email = strings.TrimSpace(email)
	plan := NormalizePlanType(planType)

	prefix := ""
	if includeProviderPrefix {
		prefix = "codex"
	}

	if plan == "" {
		return fmt.Sprintf("%s-%s.json", prefix, email)
	} else if plan == "team" {
		return fmt.Sprintf("%s-%s-%s-%s.json", prefix, hashAccountID, email, plan)
	}
	return fmt.Sprintf("%s-%s-%s.json", prefix, email, plan)
}

// NormalizePlanType converts a provider plan value into a lower-case, filename-safe form.
func NormalizePlanType(planType string) string {
	planType = strings.TrimSpace(planType)
	if planType == "" {
		return ""
	}

	parts := strings.FieldsFunc(planType, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(parts) == 0 {
		return ""
	}

	for i, part := range parts {
		parts[i] = strings.ToLower(strings.TrimSpace(part))
	}
	return strings.Join(parts, "-")
}

// AccountGroupFromPlanType maps Codex subscription plans into stable account groups.
func AccountGroupFromPlanType(planType string) string {
	normalized := NormalizePlanType(planType)
	switch {
	case normalized == "", normalized == "unknown":
		return ""
	case normalized == "pro", normalized == "prolite", normalized == "pro20x",
		strings.HasPrefix(normalized, "pro-"), strings.HasSuffix(normalized, "-pro"):
		return "pro"
	case normalized == "plus", strings.HasSuffix(normalized, "-plus"):
		return "plus"
	case normalized == "free", strings.HasSuffix(normalized, "-free"):
		return "free"
	case normalized == "team", normalized == "business", normalized == "enterprise", normalized == "go":
		return "team"
	case strings.HasSuffix(normalized, "-team"), strings.HasSuffix(normalized, "-business"),
		strings.HasSuffix(normalized, "-enterprise"), strings.HasSuffix(normalized, "-go"):
		return "team"
	default:
		return normalized
	}
}
