package ratelimit

import (
	"net/http"
	"testing"
	"time"
)

func TestParseHeaders_FiveHourWindow(t *testing.T) {
	tracker := NewTracker(nil)

	h := http.Header{}
	h.Set("x-ratelimit-limit-five-hour", "100")
	h.Set("x-ratelimit-remaining-five-hour", "80")
	h.Set("x-ratelimit-reset-five-hour", "2026-01-01T05:00:00Z")

	tracker.ParseHeaders(h)

	info := tracker.GetInfo()
	if info == nil {
		t.Fatal("expected non-nil info")
	}
	if info.RateLimitType != RateLimitFiveHour {
		t.Errorf("expected five_hour, got %s", info.RateLimitType)
	}
	if info.Status != RateLimitAllowed {
		t.Errorf("expected allowed, got %s", info.Status)
	}
	if info.Utilization < 0.19 || info.Utilization > 0.21 {
		t.Errorf("expected ~0.2, got %f", info.Utilization)
	}
}

func TestParseHeaders_Warning(t *testing.T) {
	tracker := NewTracker(nil)

	h := http.Header{}
	h.Set("x-ratelimit-limit-five-hour", "100")
	h.Set("x-ratelimit-remaining-five-hour", "15")
	h.Set("x-ratelimit-reset-five-hour", "2026-01-01T05:00:00Z")

	tracker.ParseHeaders(h)

	info := tracker.GetInfo()
	if info.Status != RateLimitAllowedWarning {
		t.Errorf("expected allowed_warning, got %s", info.Status)
	}
}

func TestParseHeaders_Rejected(t *testing.T) {
	tracker := NewTracker(nil)

	h := http.Header{}
	h.Set("x-ratelimit-limit-five-hour", "100")
	h.Set("x-ratelimit-remaining-five-hour", "0")
	h.Set("x-ratelimit-reset-five-hour", "2026-01-01T05:00:00Z")

	tracker.ParseHeaders(h)

	if !tracker.IsRejected() {
		t.Error("expected IsRejected to be true")
	}

	info := tracker.GetInfo()
	if info.Status != RateLimitRejected {
		t.Errorf("expected rejected, got %s", info.Status)
	}
}

func TestParseHeaders_MostConstrainedWins(t *testing.T) {
	tracker := NewTracker(nil)

	h := http.Header{}
	// Five-hour: plenty of room
	h.Set("x-ratelimit-limit-five-hour", "100")
	h.Set("x-ratelimit-remaining-five-hour", "90")
	h.Set("x-ratelimit-reset-five-hour", "2026-01-01T05:00:00Z")
	// Seven-day: almost exhausted
	h.Set("x-ratelimit-limit-seven-day", "1000")
	h.Set("x-ratelimit-remaining-seven-day", "50")
	h.Set("x-ratelimit-reset-seven-day", "2026-01-07T00:00:00Z")

	tracker.ParseHeaders(h)

	info := tracker.GetInfo()
	if info.RateLimitType != RateLimitSevenDay {
		t.Errorf("expected seven_day (more constrained), got %s", info.RateLimitType)
	}
}

func TestParseHeaders_UnixTimestamp(t *testing.T) {
	tracker := NewTracker(nil)

	h := http.Header{}
	h.Set("x-ratelimit-limit-five-hour", "100")
	h.Set("x-ratelimit-remaining-five-hour", "50")
	h.Set("x-ratelimit-reset-five-hour", "1767225600") // 2026-01-01 00:00:00 UTC

	tracker.ParseHeaders(h)

	info := tracker.GetInfo()
	if info.ResetsAt == nil {
		t.Fatal("expected non-nil ResetsAt")
	}
	expected := time.Unix(1767225600, 0)
	if !info.ResetsAt.Equal(expected) {
		t.Errorf("expected %v, got %v", expected, info.ResetsAt)
	}
}

func TestParseHeaders_MissingHeaders(t *testing.T) {
	tracker := NewTracker(nil)
	initial := tracker.GetInfo()

	// Empty headers should not change state.
	tracker.ParseHeaders(http.Header{})

	after := tracker.GetInfo()
	if initial.Status != after.Status {
		t.Error("empty headers should not change status")
	}
}

func TestOnChangeCallback(t *testing.T) {
	var events []RateLimitEvent
	tracker := NewTracker(func(e RateLimitEvent) {
		events = append(events, e)
	})

	h := http.Header{}
	h.Set("x-ratelimit-limit-five-hour", "100")
	h.Set("x-ratelimit-remaining-five-hour", "0")
	h.Set("x-ratelimit-reset-five-hour", "2026-01-01T05:00:00Z")

	tracker.ParseHeaders(h)

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "rate_limit" {
		t.Errorf("expected type rate_limit, got %s", events[0].Type)
	}
	if events[0].Info.Status != RateLimitRejected {
		t.Errorf("expected rejected in event, got %s", events[0].Info.Status)
	}
}

func TestOnChange_NotCalledWhenSameStatus(t *testing.T) {
	callCount := 0
	tracker := NewTracker(func(e RateLimitEvent) {
		callCount++
	})

	h := http.Header{}
	h.Set("x-ratelimit-limit-five-hour", "100")
	h.Set("x-ratelimit-remaining-five-hour", "80")
	h.Set("x-ratelimit-reset-five-hour", "2026-01-01T05:00:00Z")

	// First call: status changes from default allowed to allowed (same).
	// But since initial is "allowed" and new is "allowed", no change event.
	tracker.ParseHeaders(h)
	tracker.ParseHeaders(h)

	if callCount != 0 {
		t.Errorf("expected 0 events for same status, got %d", callCount)
	}
}

func TestOverageStatus(t *testing.T) {
	tracker := NewTracker(nil)

	h := http.Header{}
	h.Set("x-ratelimit-limit-overage", "500")
	h.Set("x-ratelimit-remaining-overage", "100")
	h.Set("x-ratelimit-reset-overage", "2026-01-01T00:00:00Z")
	h.Set("x-ratelimit-overage-status", "active")

	tracker.ParseHeaders(h)

	info := tracker.GetInfo()
	if info.OverageStatus != "active" {
		t.Errorf("expected overage_status 'active', got %q", info.OverageStatus)
	}
}

func TestIsRejected_InitiallyFalse(t *testing.T) {
	tracker := NewTracker(nil)
	if tracker.IsRejected() {
		t.Error("new tracker should not be rejected")
	}
}
