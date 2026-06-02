package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestKiroExecutorMapModelToKiro(t *testing.T) {
	exec := NewKiroExecutor(nil)

	tests := []struct {
		name  string
		model string
		want  string
	}{
		{name: "static alias", model: "kiro-claude-sonnet-4-5", want: "claude-sonnet-4.5"},
		{name: "agentic alias", model: "kiro-claude-sonnet-4-5-agentic", want: "claude-sonnet-4.5"},
		{name: "third party alias", model: "kiro-glm-5", want: "glm-5"},
		{name: "backend id passthrough", model: "glm-5", want: "glm-5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exec.mapModelToKiro(tt.model); got != tt.want {
				t.Fatalf("mapModelToKiro(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

func TestKiroExecutorCountTokens(t *testing.T) {
	exec := NewKiroExecutor(nil)
	resp, err := exec.CountTokens(context.Background(), &cliproxyauth.Auth{Provider: "kiro"}, cliproxyexecutor.Request{
		Model:   "kiro-claude-sonnet-4-5",
		Payload: []byte(`{"messages":[{"role":"user","content":"hello from kiro"}]}`),
	}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("CountTokens() error = %v", err)
	}

	var body struct {
		Count int64 `json:"count"`
	}
	if err := json.Unmarshal(resp.Payload, &body); err != nil {
		t.Fatalf("CountTokens() payload is invalid JSON: %v", err)
	}
	if body.Count <= 0 {
		t.Fatalf("CountTokens() count = %d, want > 0", body.Count)
	}
}

func TestKiroCredentialsUsesAPIKey(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "kiro",
		Metadata: map[string]any{
			"type":        "kiro",
			"auth_method": "api-key",
			"api_key":     "ksk_test_secret",
		},
	}

	credential, profileArn := kiroCredentials(auth)
	if credential != "ksk_test_secret" {
		t.Fatalf("credential = %q, want api key", credential)
	}
	if profileArn != "" {
		t.Fatalf("profileArn = %q, want empty", profileArn)
	}
	if !isKiroAPIKeyAuth(auth) {
		t.Fatal("isKiroAPIKeyAuth() = false")
	}
}

func TestKiroPrepareRequestUsesAPIKeyBearer(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "kiro",
		Metadata: map[string]any{
			"type":        "kiro",
			"auth_method": "api-key",
			"api_key":     "ksk_test_secret",
		},
	}
	req := httptest.NewRequest(http.MethodPost, "https://q.us-east-1.amazonaws.com/", nil)
	if err := NewKiroExecutor(nil).PrepareRequest(req, auth); err != nil {
		t.Fatalf("PrepareRequest() error = %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer ksk_test_secret" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestKiroExecutorReadEventStreamMessage(t *testing.T) {
	frame := buildKiroEventStreamFrame(t, "assistantResponseEvent", []byte(`{"assistantResponseEvent":{"content":"hello"}}`))
	msg, err := NewKiroExecutor(nil).readEventStreamMessage(bufio.NewReader(bytes.NewReader(frame)))
	if err != nil {
		t.Fatalf("readEventStreamMessage() error = %v", err)
	}
	if msg == nil {
		t.Fatal("readEventStreamMessage() returned nil message")
	}
	if msg.EventType != "assistantResponseEvent" {
		t.Fatalf("EventType = %q, want assistantResponseEvent", msg.EventType)
	}
	if !strings.Contains(string(msg.Payload), `"content":"hello"`) {
		t.Fatalf("Payload = %s", string(msg.Payload))
	}
}

func TestKiroExecutorParseEventStream(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(buildKiroEventStreamFrame(t, "assistantResponseEvent", []byte(`{"assistantResponseEvent":{"content":"hello ","stopReason":"end_turn"}}`)))
	stream.Write(buildKiroEventStreamFrame(t, "assistantResponseEvent", []byte(`{"assistantResponseEvent":{"content":"world"}}`)))
	stream.Write(buildKiroEventStreamFrame(t, "messageMetadataEvent", []byte(`{"messageMetadataEvent":{"inputTokens":3,"outputTokens":2,"totalTokens":5}}`)))

	content, tools, usageInfo, stopReason, err := NewKiroExecutor(nil).parseEventStream(bytes.NewReader(stream.Bytes()))
	if err != nil {
		t.Fatalf("parseEventStream() error = %v", err)
	}
	if content != "hello world" {
		t.Fatalf("content = %q, want hello world", content)
	}
	if len(tools) != 0 {
		t.Fatalf("tools = %#v, want none", tools)
	}
	if stopReason != "end_turn" {
		t.Fatalf("stopReason = %q, want end_turn", stopReason)
	}
	if usageInfo.InputTokens != 3 || usageInfo.OutputTokens != 2 || usageInfo.TotalTokens != 5 {
		t.Fatalf("usage = %#v", usageInfo)
	}
}

func buildKiroEventStreamFrame(t *testing.T, eventType string, payload []byte) []byte {
	t.Helper()

	var headers bytes.Buffer
	writeEventStreamStringHeader(t, &headers, ":event-type", eventType)

	headersLen := headers.Len()
	totalLen := 12 + headersLen + len(payload) + 4
	frame := make([]byte, totalLen)
	binary.BigEndian.PutUint32(frame[0:4], uint32(totalLen))
	binary.BigEndian.PutUint32(frame[4:8], uint32(headersLen))
	copy(frame[12:12+headersLen], headers.Bytes())
	copy(frame[12+headersLen:totalLen-4], payload)
	return frame
}

func writeEventStreamStringHeader(t *testing.T, buf *bytes.Buffer, name, value string) {
	t.Helper()
	if len(name) > 255 {
		t.Fatalf("event stream header name too long: %s", name)
	}
	buf.WriteByte(byte(len(name)))
	buf.WriteString(name)
	buf.WriteByte(7)
	if len(value) > 65535 {
		t.Fatalf("event stream header value too long: %s", value)
	}
	var valueLen [2]byte
	binary.BigEndian.PutUint16(valueLen[:], uint16(len(value)))
	buf.Write(valueLen[:])
	buf.WriteString(value)
}
