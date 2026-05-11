package kiro

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	pathGetUsageLimits      = "getUsageLimits"
	pathListAvailableModels = "ListAvailableModels"
	KiroOriginAIEditor      = "AI_EDITOR"
	KiroOriginCLI           = "KIRO_CLI"
)

func OriginForAuthMethod(authMethod string) string {
	if IsKiroCLIAuthMethod(authMethod) {
		return KiroOriginCLI
	}
	return KiroOriginAIEditor
}

// GetCodeWhispererLegacyEndpoint returns the legacy CodeWhisperer JSON-RPC endpoint.
func GetCodeWhispererLegacyEndpoint(region string) string {
	if region == "" {
		region = DefaultKiroRegion
	}
	return "https://codewhisperer." + region + ".amazonaws.com"
}

// GetKiroAPIEndpoint returns the Q API endpoint for the specified region.
func GetKiroAPIEndpoint(region string) string {
	if region == "" {
		region = DefaultKiroRegion
	}
	return "https://q." + region + ".amazonaws.com"
}

// GetKiroAPIEndpointFromProfileArn extracts region from profileArn and returns the endpoint.
func GetKiroAPIEndpointFromProfileArn(profileArn string) string {
	region := ExtractRegionFromProfileArn(profileArn)
	return GetKiroAPIEndpoint(region)
}

// ExtractRegionFromProfileArn extracts the AWS region from a CodeWhisperer profile ARN.
func ExtractRegionFromProfileArn(profileArn string) string {
	parts := strings.Split(profileArn, ":")
	if len(parts) < 6 || parts[0] != "arn" || parts[2] != "codewhisperer" || !strings.Contains(parts[3], "-") {
		return ""
	}
	return parts[3]
}

func buildURL(endpoint, path string, queryParams map[string]string) string {
	fullURL := fmt.Sprintf("%s/%s", endpoint, path)
	if len(queryParams) > 0 {
		values := url.Values{}
		for key, value := range queryParams {
			if value == "" {
				continue
			}
			values.Set(key, value)
		}
		if encoded := values.Encode(); encoded != "" {
			fullURL = fullURL + "?" + encoded
		}
	}
	return fullURL
}
