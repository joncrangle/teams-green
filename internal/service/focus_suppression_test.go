package service

import (
	"testing"
	"time"

	"github.com/lxn/win"
)

// mockConfig implements ConfigProvider with minimal methods used in tests.
type mockConfig struct{}

func (m *mockConfig) GetFocusDelay() time.Duration      { return 0 }
func (m *mockConfig) GetRestoreDelay() time.Duration    { return 0 }
func (m *mockConfig) GetKeyProcessDelay() time.Duration { return 0 }
func (m *mockConfig) GetInputThreshold() time.Duration  { return 500 * time.Millisecond }
func (m *mockConfig) IsDebugEnabled() bool              { return false }
func (m *mockConfig) GetActivityMode() string           { return "" }

func newTestTeamsManager(nowFn func() time.Time) *TeamsManager {
	return &TeamsManager{
		config:        &mockConfig{},
		focusFailures: make(map[win.HWND]focusFailInfo),
		now:           nowFn,
	}
}

func TestFocusSuppressionThresholdAndWindow(t *testing.T) {
	base := time.Now()
	now := base
	nowFn := func() time.Time { return now }
	tm := newTestTeamsManager(nowFn)
	var hwnd win.HWND = 0x1234
	fgPID := uint32(111)

	// Should not suppress initially
	if tm.shouldSuppressFocus(hwnd, fgPID) {
		// map empty, should be false
		t.Fatalf("expected no suppression initially")
	}

	// Record failures below threshold
	for range focusFailureThreshold - 1 {
		tm.recordFocusFailure(hwnd, fgPID)
	}
	if tm.shouldSuppressFocus(hwnd, fgPID) {
		// Not yet threshold
		if tm.focusFailures[hwnd].failCount != focusFailureThreshold-1 {
			t.Fatalf("unexpected failCount: %d", tm.focusFailures[hwnd].failCount)
		}
		// Should not suppress yet
		t.Fatalf("suppression triggered before threshold")
	}

	// Hit threshold
	mm := tm.focusFailures[hwnd].failCount
	for i := mm; i < focusFailureThreshold; i++ {
		tm.recordFocusFailure(hwnd, fgPID)
	}
	if !tm.shouldSuppressFocus(hwnd, fgPID) {
		// At threshold within window
		if tm.focusFailures[hwnd].failCount != focusFailureThreshold {
			t.Fatalf("expected failCount == %d got %d", focusFailureThreshold, tm.focusFailures[hwnd].failCount)
		}
		t.Fatalf("expected suppression at threshold")
	}

	// Advance time beyond window but within expiry -> suppression clears
	now = now.Add(focusFailureWindow + time.Second)
	if tm.shouldSuppressFocus(hwnd, fgPID) {
		// After window suppression should lift
		t.Fatalf("expected suppression cleared after window duration")
	}

	// Advance beyond expiry to trigger record removal
	now = now.Add(focusFailureRecordExpiry + time.Second)
	_ = tm.shouldSuppressFocus(hwnd, fgPID)
	if _, exists := tm.focusFailures[hwnd]; exists {
		// Should be expired and removed
		t.Fatalf("expected failure record expired and removed")
	}
}

func TestFocusSuppressionForegroundPIDChangeResets(t *testing.T) {
	base := time.Now()
	now := base
	nowFn := func() time.Time { return now }
	tm := newTestTeamsManager(nowFn)
	var hwnd win.HWND = 0x2222
	pidA := uint32(10)
	pidB := uint32(20)

	// Build failures with pidA
	for range focusFailureThreshold {
		tm.recordFocusFailure(hwnd, pidA)
	}
	if !tm.shouldSuppressFocus(hwnd, pidA) {
		// Should suppress at threshold
		t.Fatalf("expected suppression with consistent foreground PID")
	}

	// Switch foreground PID -> resets streak
	tm.recordFocusFailure(hwnd, pidB)
	if tm.focusFailures[hwnd].failCount >= focusFailureThreshold && tm.focusFailures[hwnd].lastForegroundPID == pidB {
		// After PID change, failCount should have restarted from 1
		t.Fatalf("expected failCount reset after foreground PID change; got %d", tm.focusFailures[hwnd].failCount)
	}
	if tm.shouldSuppressFocus(hwnd, pidB) {
		// Should not suppress because streak context changed
		t.Fatalf("did not expect suppression after PID change")
	}
}

func TestResetFocusFailure(t *testing.T) {
	base := time.Now()
	now := base
	nowFn := func() time.Time { return now }
	tm := newTestTeamsManager(nowFn)
	var hwnd win.HWND = 0x3333
	pid := uint32(77)
	for range focusFailureThreshold {
		tm.recordFocusFailure(hwnd, pid)
	}
	if len(tm.focusFailures) == 0 {
		// Should have entry
		t.Fatalf("expected focusFailures entry after recording failures")
	}
	tm.resetFocusFailure(hwnd)
	if _, ok := tm.focusFailures[hwnd]; ok {
		// Entry should be removed
		t.Fatalf("expected focusFailures entry removed after reset")
	}
}
