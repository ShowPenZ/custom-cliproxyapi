// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing YAML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

// SDKConfig represents the application's configuration, loaded from a YAML file.
type SDKConfig struct {
	// ProxyURL is the URL of an optional proxy server to use for outbound requests.
	ProxyURL string `yaml:"proxy-url" json:"proxy-url"`

	// ForceModelPrefix requires explicit model prefixes (e.g., "teamA/gemini-3-pro-preview")
	// to target prefixed credentials. When false, unprefixed model requests may use prefixed
	// credentials as well.
	ForceModelPrefix bool `yaml:"force-model-prefix" json:"force-model-prefix"`

	// RequestLog enables or disables detailed request logging functionality.
	RequestLog bool `yaml:"request-log" json:"request-log"`

	// APIKeys is a list of keys for authenticating clients to this proxy server.
	APIKeys []string `yaml:"api-keys" json:"api-keys"`

	// ProxyOnlyAPIKeys restricts selected client API keys to model proxy endpoints only.
	// Keys listed here cannot access auxiliary public endpoints such as /v1/api/* or /api/usage.
	ProxyOnlyAPIKeys []string `yaml:"proxy-only-api-keys,omitempty" json:"proxy-only-api-keys,omitempty"`

	// ClaudeOnlyAPIKeys restricts selected client API keys to Claude-compatible proxy endpoints only.
	// Keys listed here are limited to Anthropic/Claude routes such as /v1/messages and
	// the Anthropic provider aliases under /api/provider/anthropic.
	ClaudeOnlyAPIKeys []string `yaml:"claude-only-api-keys,omitempty" json:"claude-only-api-keys,omitempty"`

	// CodexProAPIKeys whitelists client API keys that may use Codex auths in the "pro" account group.
	// When this list is non-empty, client keys not listed here are prevented from selecting Codex
	// auth entries whose account_group resolves to "pro". Requests may additionally pin a desired
	// account group via X-Codex-Account-Group / X-Account-Group headers or the
	// codex_account_group / account_group query parameter.
	CodexProAPIKeys []string `yaml:"codex-pro-api-keys,omitempty" json:"codex-pro-api-keys,omitempty"`

	// PassthroughHeaders controls whether upstream response headers are forwarded to downstream clients.
	// Default is false (disabled).
	PassthroughHeaders bool `yaml:"passthrough-headers" json:"passthrough-headers"`

	// Streaming configures server-side streaming behavior (keep-alives and safe bootstrap retries).
	Streaming StreamingConfig `yaml:"streaming" json:"streaming"`

	// NonStreamKeepAliveInterval controls how often blank lines are emitted for non-streaming responses.
	// <= 0 disables keep-alives. Value is in seconds.
	NonStreamKeepAliveInterval int `yaml:"nonstream-keepalive-interval,omitempty" json:"nonstream-keepalive-interval,omitempty"`
}

// StreamingConfig holds server streaming behavior configuration.
type StreamingConfig struct {
	// KeepAliveSeconds controls how often the server emits SSE heartbeats (": keep-alive\n\n").
	// <= 0 disables keep-alives. Default is 0.
	KeepAliveSeconds int `yaml:"keepalive-seconds,omitempty" json:"keepalive-seconds,omitempty"`

	// BootstrapRetries controls how many times the server may retry a streaming request before any bytes are sent,
	// to allow auth rotation / transient recovery.
	// <= 0 disables bootstrap retries. Default is 0.
	BootstrapRetries int `yaml:"bootstrap-retries,omitempty" json:"bootstrap-retries,omitempty"`
}
