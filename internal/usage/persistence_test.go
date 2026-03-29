package usage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestRequestStatisticsPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-stats.json")

	first := NewRequestStatistics()
	if err := first.EnablePersistence(path); err != nil {
		t.Fatalf("EnablePersistence(first): %v", err)
	}
	first.Record(context.Background(), coreusage.Record{
		APIKey:      "sk-team-user-1234567890abcdef",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 3, 29, 12, 30, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  11,
			OutputTokens: 7,
			TotalTokens:  18,
		},
	})
	if err := first.PersistNow(); err != nil {
		t.Fatalf("PersistNow(first): %v", err)
	}

	second := NewRequestStatistics()
	if err := second.EnablePersistence(path); err != nil {
		t.Fatalf("EnablePersistence(second): %v", err)
	}

	snapshot := second.SnapshotForAPI("sk-team-user-1234567890abcdef")
	if snapshot.TotalRequests != 1 {
		t.Fatalf("total_requests = %d, want 1", snapshot.TotalRequests)
	}
	if snapshot.TotalTokens != 18 {
		t.Fatalf("total_tokens = %d, want 18", snapshot.TotalTokens)
	}
	model := snapshot.APIs["sk-team-user-1234567890abcdef"].Models["gpt-5.4"]
	if model.TotalRequests != 1 {
		t.Fatalf("model total_requests = %d, want 1", model.TotalRequests)
	}
	if len(model.Details) != 1 {
		t.Fatalf("details len = %d, want 1", len(model.Details))
	}
}
