package service

import (
	"testing"
	"time"

	"github.com/lxn/win"
)

// Tests for stage 3 escalation cooldown logic via pure helper canEscalateStage3.
// We avoid invoking enhancedAttachFocus directly to prevent dependence on real Win32 focus behavior.

func TestStage3CooldownSkip(t *testing.T) {
	base := time.Now()
	now := base
	nowFn := func() time.Time { return now }
	tm := newTestTeamsManager(nowFn)
	// initialize escalation tracking like constructor
	tm.lastEscalation = make(map[win.HWND]time.Time)
	tm.escalationCooldown = 5 * time.Minute

	var hwnd win.HWND = 0x4444
	// Seed last escalation as now
	tm.lastEscalation[hwnd] = now

	// Advance a small amount < cooldown
	now = now.Add(30 * time.Second)
	allowed, reason := tm.canEscalateStage3(hwnd)
	if allowed || reason != "cooldown" {
		//nolint:revive // test error style
		t.Fatalf("expected cooldown skip (allowed=false, reason=cooldown); got allowed=%v reason=%s", allowed, reason)
	}
}

func TestStage3AllowedAfterExpiry(t *testing.T) {
	base := time.Now()
	now := base
	nowFn := func() time.Time { return now }
	tm := newTestTeamsManager(nowFn)
	tm.lastEscalation = make(map[win.HWND]time.Time)
	tm.escalationCooldown = 1 * time.Minute
	var hwnd win.HWND = 0x5555

	// Seed last escalation well in the past beyond cooldown
	tm.lastEscalation[hwnd] = now.Add(-2 * time.Minute)

	allowed, reason := tm.canEscalateStage3(hwnd)
	if !allowed || reason != "ok" {
		//nolint:revive // test error style
		t.Fatalf("expected escalation allowed after cooldown expiry; got allowed=%v reason=%s", allowed, reason)
	}
}
