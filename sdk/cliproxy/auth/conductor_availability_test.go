package auth

import (
	"context"
	"testing"
	"time"
)

func TestUpdateAggregatedAvailability_UnavailableWithoutNextRetryDoesNotBlockAuth(t *testing.T) {
	t.Parallel()

	now := time.Now()
	model := "test-model"
	auth := &Auth{
		ID: "a",
		ModelStates: map[string]*ModelState{
			model: {
				Status:      StatusError,
				Unavailable: true,
			},
		},
	}

	updateAggregatedAvailability(auth, now)

	if auth.Unavailable {
		t.Fatalf("auth.Unavailable = true, want false")
	}
	if !auth.NextRetryAfter.IsZero() {
		t.Fatalf("auth.NextRetryAfter = %v, want zero", auth.NextRetryAfter)
	}
}

func TestUpdateAggregatedAvailability_FutureNextRetryBlocksAuth(t *testing.T) {
	t.Parallel()

	now := time.Now()
	model := "test-model"
	next := now.Add(5 * time.Minute)
	auth := &Auth{
		ID: "a",
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: next,
			},
		},
	}

	updateAggregatedAvailability(auth, now)

	if !auth.Unavailable {
		t.Fatalf("auth.Unavailable = false, want true")
	}
	if auth.NextRetryAfter.IsZero() {
		t.Fatalf("auth.NextRetryAfter = zero, want %v", next)
	}
	if auth.NextRetryAfter.Sub(next) > time.Second || next.Sub(auth.NextRetryAfter) > time.Second {
		t.Fatalf("auth.NextRetryAfter = %v, want %v", auth.NextRetryAfter, next)
	}
}

func TestManagerMarkResult_ValidationRequiredDoesNotCooldownModel(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "antigravity-auth",
		Provider: "antigravity",
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	model := "gemini-2.5-flash"
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error: &Error{
			HTTPStatus: 403,
			Message:    `{"error":{"message":"Verify your account to continue.","details":[{"reason":"VALIDATION_REQUIRED"}]}}`,
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to remain registered")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state for %q", model)
	}
	if !state.Unavailable {
		t.Fatalf("expected model state to record the upstream failure")
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected validation-required state to avoid cooldown, got %v", state.NextRetryAfter)
	}
	if state.StatusMessage != "validation_required" {
		t.Fatalf("state.StatusMessage = %q, want %q", state.StatusMessage, "validation_required")
	}
	blocked, reason, _ := isAuthBlockedForModel(updated, model, time.Now())
	if blocked {
		t.Fatalf("expected auth to stay selectable, blocked with reason %v", reason)
	}
	if updated.Unavailable {
		t.Fatalf("auth.Unavailable = true, want false")
	}
}

func TestApplyAuthFailureState_ValidationRequiredDoesNotCooldownAuth(t *testing.T) {
	t.Parallel()

	now := time.Now()
	auth := &Auth{ID: "antigravity-auth", Provider: "antigravity"}
	err := &Error{
		HTTPStatus: 403,
		Message:    `{"error":{"message":"Verify your account to continue.","details":[{"reason":"VALIDATION_REQUIRED"}]}}`,
	}

	applyAuthFailureState(auth, err, nil, now)

	if !auth.Unavailable {
		t.Fatalf("auth.Unavailable = false, want true")
	}
	if !auth.NextRetryAfter.IsZero() {
		t.Fatalf("auth.NextRetryAfter = %v, want zero", auth.NextRetryAfter)
	}
	if auth.StatusMessage != "validation_required" {
		t.Fatalf("auth.StatusMessage = %q, want %q", auth.StatusMessage, "validation_required")
	}
}
