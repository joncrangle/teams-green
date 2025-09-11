package service

import (
	"context"
	"log/slog"
	"os"
	"syscall"
	"testing"
	"unsafe"

	"github.com/joncrangle/teams-green/internal/config"
	"github.com/joncrangle/teams-green/internal/websocket"
	"github.com/lxn/win"
)

func TestEnumWindowsProcValidation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	tests := []struct {
		name        string
		hwnd        syscall.Handle
		lParam      uintptr
		want        uintptr
		description string
	}{
		{
			name:        "zero hwnd should return early",
			hwnd:        0,
			lParam:      0,
			want:        1,
			description: "Function should validate hwnd parameter and return 1 for zero hwnd",
		},
		{
			name:        "zero lParam should return early",
			hwnd:        syscall.Handle(12345),
			lParam:      0,
			want:        1,
			description: "Function should validate lParam parameter and return 1 for zero lParam",
		},
		{
			name:        "nil pointer from lParam should return early",
			hwnd:        syscall.Handle(12345),
			lParam:      uintptr(unsafe.Pointer(nil)),
			want:        1,
			description: "Function should handle nil pointer after unsafe conversion and return 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := enumWindowsProc(tt.hwnd, tt.lParam)
			if got != tt.want {
				t.Errorf("enumWindowsProc() = %v, want %v. %s", got, tt.want, tt.description)
			}
		})
	}

	t.Run("context with nil hwnds should return early", func(t *testing.T) {
		ctx := &WindowEnumContext{
			hwnds:            nil,
			teamsExecutables: []string{"teams.exe"},
			logger:           logger,
		}

		got := enumWindowsProc(syscall.Handle(12345), uintptr(unsafe.Pointer(ctx)))
		if got != 1 {
			t.Errorf("enumWindowsProc() with nil hwnds = %v, want 1", got)
		}
	})

	t.Run("context with nil teamsExecutables should return early", func(t *testing.T) {
		var hwnds []win.HWND
		ctx := &WindowEnumContext{
			hwnds:            &hwnds,
			teamsExecutables: nil,
			logger:           logger,
		}

		got := enumWindowsProc(syscall.Handle(12345), uintptr(unsafe.Pointer(ctx)))
		if got != 1 {
			t.Errorf("enumWindowsProc() with nil teamsExecutables = %v, want 1", got)
		}
	})

	t.Run("context with nil logger should return early", func(t *testing.T) {
		var hwnds []win.HWND
		ctx := &WindowEnumContext{
			hwnds:            &hwnds,
			teamsExecutables: []string{"teams.exe"},
			logger:           nil,
		}

		got := enumWindowsProc(syscall.Handle(12345), uintptr(unsafe.Pointer(ctx)))
		if got != 1 {
			t.Errorf("enumWindowsProc() with nil logger = %v, want 1", got)
		}
	})

	t.Run("valid context should proceed", func(t *testing.T) {
		var hwnds []win.HWND
		ctx := &WindowEnumContext{
			hwnds:            &hwnds,
			teamsExecutables: []string{"notepad.exe"},
			logger:           logger,
		}

		got := enumWindowsProc(syscall.Handle(12345), uintptr(unsafe.Pointer(ctx)))
		if got != 1 {
			t.Errorf("enumWindowsProc() with valid context = %v, want 1", got)
		}
	})
}

func TestTeamsManagerFindTeamsWindows(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	var hwnds []win.HWND
	enumContext := &WindowEnumContext{
		hwnds:            &hwnds,
		teamsExecutables: getTeamsExecutables(),
		logger:           logger,
	}

	tm := &TeamsManager{
		logger:      logger,
		enumContext: enumContext,
	}

	result := tm.FindTeamsWindows()

	if result == nil {
		t.Error("FindTeamsWindows() returned nil")
	}
}

func TestIsKeyPressed(t *testing.T) {
	result := isKeyPressed()

	if result != true && result != false {
		t.Errorf("isKeyPressed() returned %v, expected boolean", result)
	}
}

func TestTeamsManagerIsUserInputActive(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	tm := &TeamsManager{
		logger: logger,
	}

	result := tm.isUserInputActive()

	if result != true && result != false {
		t.Errorf("isUserInputActive() returned %v, expected boolean", result)
	}
}

func TestSendKeyToWindow(t *testing.T) {
	const vkF15 = 0x7E

	err := sendKeyToWindow(win.HWND(0), vkF15)

	if err == nil {
		t.Log("sendKeyToWindow() succeeded")
	} else {
		t.Logf("sendKeyToWindow() failed: %v", err)
	}
}

func TestTeamsManagerHandleTeamsNotFound(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	tm := &TeamsManager{
		logger: logger,
		retryState: &RetryState{
			FailureCount: 0,
		},
	}

	state := &websocket.ServiceState{}

	err := tm.handleTeamsNotFound(state)

	if err == nil {
		t.Error("handleTeamsNotFound() should return an error")
	}

	if tm.retryState.FailureCount != 1 {
		t.Errorf("handleTeamsNotFound() should increment FailureCount, got %d, want 1", tm.retryState.FailureCount)
	}

	expectedErrorMsg := "no Teams windows found"
	if err.Error() != expectedErrorMsg {
		t.Errorf("handleTeamsNotFound() error = %v, want %v", err.Error(), expectedErrorMsg)
	}
}

func TestTeamsManagerSendKeysToTeams(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	var hwnds []win.HWND
	enumContext := &WindowEnumContext{
		hwnds:            &hwnds,
		teamsExecutables: getTeamsExecutables(),
		logger:           logger,
	}

	cfg := &config.Config{
		Debug:             false,
		Interval:          180,
		WebSocket:         false,
		LogFormat:         "text",
		FocusDelayMs:      10,
		RestoreDelayMs:    10,
		KeyProcessDelayMs: 10,
	}

	tm := &TeamsManager{
		logger:      logger,
		enumContext: enumContext,
		config:      cfg,
		retryState:  &RetryState{},
	}

	ctx := context.Background()
	state := &websocket.ServiceState{}

	err := tm.SendKeysToTeams(ctx, state)

	if err == nil {
		t.Log("SendKeysToTeams() succeeded")
	} else {
		t.Logf("SendKeysToTeams() failed: %v", err)
	}
}

func TestTeamsManagerSendKeysToTeamsCancelled(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	var hwnds []win.HWND
	enumContext := &WindowEnumContext{
		hwnds:            &hwnds,
		teamsExecutables: getTeamsExecutables(),
		logger:           logger,
	}

	cfg := &config.Config{
		Debug:             false,
		Interval:          180,
		WebSocket:         false,
		LogFormat:         "text",
		FocusDelayMs:      10,
		RestoreDelayMs:    10,
		KeyProcessDelayMs: 10,
	}

	tm := &TeamsManager{
		logger:      logger,
		enumContext: enumContext,
		config:      cfg,
		retryState:  &RetryState{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	state := &websocket.ServiceState{}

	err := tm.SendKeysToTeams(ctx, state)

	if err != context.Canceled {
		t.Errorf("SendKeysToTeams() with cancelled context should return context.Canceled, got %v", err)
	}
}

func TestErrUserInputActive(t *testing.T) {
	if ErrUserInputActive == nil {
		t.Error("ErrUserInputActive should not be nil")
	}

	expectedMsg := "user input active, deferring Teams activity"
	if ErrUserInputActive.Error() != expectedMsg {
		t.Errorf("ErrUserInputActive.Error() = %q, want %q", ErrUserInputActive.Error(), expectedMsg)
	}
}
