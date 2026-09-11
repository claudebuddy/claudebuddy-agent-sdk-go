package ratelimit

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// RateLimitStatus represents the current rate limit state.
type RateLimitStatus string

const (
	RateLimitAllowed        RateLimitStatus = "allowed"
	RateLimitAllowedWarning RateLimitStatus = "allowed_warning"
	RateLimitRejected       RateLimitStatus = "rejected"
)

// RateLimitType represents which rate limit window applies.
type RateLimitType string

const (
	RateLimitFiveHour      RateLimitType = "five_hour"
	RateLimitSevenDay      RateLimitType = "seven_day"
	RateLimitSevenDayOpus  RateLimitType = "seven_day_opus"
	RateLimitSevenDaySonnet RateLimitType = "seven_day_sonnet"
	RateLimitOverage       RateLimitType = "overage"
)

// RateLimitInfo holds the current rate limit state.
type RateLimitInfo struct {
	Status        RateLimitStatus `json:"status"`
	ResetsAt      *time.Time      `json:"resets_at,omitempty"`
	RateLimitType RateLimitType   `json:"rate_limit_type"`
	Utilization   float64         `json:"utilization"`
	OverageStatus string          `json:"overage_status,omitempty"`
}

// RateLimitEvent is emitted when rate limit status changes.
type RateLimitEvent struct {
	Type string         `json:"type"` // always "rate_limit"
	Info *RateLimitInfo `json:"info"`
}

// warningThreshold is the utilization ratio above which we emit a warning.
const warningThreshold = 0.8

// headerWindow groups rate limit header info for one window.
type headerWindow struct {
	limit     int
	remaining int
	resetAt   time.Time
}

// Tracker parses rate limit headers and tracks state.
type Tracker struct {
	mu       sync.RWMutex
	current  *RateLimitInfo
	onChange func(RateLimitEvent)
}

// NewTracker creates a new rate limit tracker.
// onChange is called whenever the rate limit status changes; it may be nil.
func NewTracker(onChange func(RateLimitEvent)) *Tracker {
	return &Tracker{
		onChange: onChange,
		current: &RateLimitInfo{
			Status:        RateLimitAllowed,
			RateLimitType: RateLimitFiveHour,
			Utilization:   0,
		},
	}
}

// ParseHeaders extracts rate limit information from API response headers
// and updates internal state. Header conventions:
//
//	x-ratelimit-limit-<window>       total allowed requests
//	x-ratelimit-remaining-<window>   remaining requests
//	x-ratelimit-reset-<window>       reset time (RFC3339 or Unix seconds)
func (t *Tracker) ParseHeaders(h http.Header) {
	windows := []struct {
		name      string
		limitType RateLimitType
	}{
		{"five-hour", RateLimitFiveHour},
		{"seven-day", RateLimitSevenDay},
		{"seven-day-opus", RateLimitSevenDayOpus},
		{"seven-day-sonnet", RateLimitSevenDaySonnet},
		{"overage", RateLimitOverage},
	}

	var mostConstrained *RateLimitInfo

	for _, w := range windows {
		hw := parseWindowHeaders(h, w.name)
		if hw == nil {
			continue
		}

		utilization := 0.0
		if hw.limit > 0 {
			utilization = 1.0 - float64(hw.remaining)/float64(hw.limit)
		}

		status := RateLimitAllowed
		if hw.remaining <= 0 {
			status = RateLimitRejected
		} else if utilization >= warningThreshold {
			status = RateLimitAllowedWarning
		}

		resetAt := hw.resetAt
		info := &RateLimitInfo{
			Status:        status,
			ResetsAt:      &resetAt,
			RateLimitType: w.limitType,
			Utilization:   utilization,
		}

		if w.limitType == RateLimitOverage {
			if ov := h.Get("x-ratelimit-overage-status"); ov != "" {
				info.OverageStatus = ov
			}
		}

		// Pick the most constrained window (highest utilization or rejected).
		if mostConstrained == nil || moreConstrained(info, mostConstrained) {
			mostConstrained = info
		}
	}

	if mostConstrained == nil {
		return
	}

	t.mu.Lock()
	prev := t.current
	t.current = mostConstrained
	t.mu.Unlock()

	if prev == nil || prev.Status != mostConstrained.Status {
		if t.onChange != nil {
			t.onChange(RateLimitEvent{
				Type: "rate_limit",
				Info: mostConstrained,
			})
		}
	}
}

// GetInfo returns the current rate limit info (copy-safe).
func (t *Tracker) GetInfo() *RateLimitInfo {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.current == nil {
		return nil
	}
	cp := *t.current
	return &cp
}

// IsRejected returns true if the current state is rejected.
func (t *Tracker) IsRejected() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.current != nil && t.current.Status == RateLimitRejected
}

// moreConstrained returns true if a is more constrained than b.
func moreConstrained(a, b *RateLimitInfo) bool {
	if a.Status == RateLimitRejected && b.Status != RateLimitRejected {
		return true
	}
	return a.Utilization > b.Utilization
}

// parseWindowHeaders extracts limit/remaining/reset for a named window.
func parseWindowHeaders(h http.Header, window string) *headerWindow {
	limitStr := h.Get("x-ratelimit-limit-" + window)
	remainStr := h.Get("x-ratelimit-remaining-" + window)
	resetStr := h.Get("x-ratelimit-reset-" + window)

	if limitStr == "" || remainStr == "" {
		return nil
	}

	limit, err := strconv.Atoi(limitStr)
	if err != nil {
		return nil
	}
	remaining, err := strconv.Atoi(remainStr)
	if err != nil {
		return nil
	}

	var resetAt time.Time
	if resetStr != "" {
		// Try RFC3339 first, then Unix timestamp.
		if t, err := time.Parse(time.RFC3339, resetStr); err == nil {
			resetAt = t
		} else if sec, err := strconv.ParseInt(resetStr, 10, 64); err == nil {
			resetAt = time.Unix(sec, 0)
		}
	}

	return &headerWindow{
		limit:     limit,
		remaining: remaining,
		resetAt:   resetAt,
	}
}
