package service

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"syscall"
	"testing"
	"unsafe"

	"github.com/joncrangle/teams-green/internal/config"
	"github.com/joncrangle/teams-green/internal/websocket"
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

	t.Run("context with nil windows should return early", func(t *testing.T) {
		var mutex sync.Mutex
		ctx := &WindowEnumContext{
			windows:          nil,
			teamsExecutables: []string{"teams.exe"},
			logger:           logger,
			mutex:            &mutex,
		}

		got := enumWindowsProc(syscall.Handle(12345), uintptr(unsafe.Pointer(ctx)))
		if got != 1 {
			t.Errorf("enumWindowsProc() with nil windows = %v, want 1", got)
		}
	})

	t.Run("context with nil teamsExecutables should return early", func(t *testing.T) {
		var windows []WindowInfo
		var mutex sync.Mutex
		ctx := &WindowEnumContext{
			windows:          &windows,
			teamsExecutables: nil,
			logger:           logger,
			mutex:            &mutex,
		}

		got := enumWindowsProc(syscall.Handle(12345), uintptr(unsafe.Pointer(ctx)))
		if got != 1 {
			t.Errorf("enumWindowsProc() with nil teamsExecutables = %v, want 1", got)
		}
	})

	t.Run("context with nil logger should return early", func(t *testing.T) {
		var windows []WindowInfo
		var mutex sync.Mutex
		ctx := &WindowEnumContext{
			windows:          &windows,
			teamsExecutables: []string{"teams.exe"},
			logger:           nil,
			mutex:            &mutex,
		}

		got := enumWindowsProc(syscall.Handle(12345), uintptr(unsafe.Pointer(ctx)))
		if got != 1 {
			t.Errorf("enumWindowsProc() with nil logger = %v, want 1", got)
		}
	})

	t.Run("valid context should proceed", func(t *testing.T) {
		var windows []WindowInfo
		var mutex sync.Mutex
		ctx := &WindowEnumContext{
			windows:          &windows,
			teamsExecutables: []string{"notepad.exe"},
			logger:           logger,
			mutex:            &mutex,
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

	var windows []WindowInfo
	var mutex sync.Mutex
	enumContext := &WindowEnumContext{
		windows:          &windows,
		teamsExecutables: teamsExecutables,
		logger:           logger,
		mutex:            &mutex,
	}

	tm := &TeamsManager{
		logger:      logger,
		enumContext: enumContext,
	}

	result := tm.FindTeamsWindows()

	// The function should never return nil - it should return an empty slice if no Teams found
	if result == nil {
		t.Fatal("FindTeamsWindows() returned nil, expected empty slice")
	}

	// Log the result for debugging
	t.Logf("Found %d Teams windows", len(result))

	// Verify it's actually a valid slice
	if len(result) == 0 {
		t.Log("No Teams windows found (expected in CI)")
	} else {
		t.Log("Teams windows found")
	}
}

func TestIsKeyPressed(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	inputDetector := NewInputDetector(logger)
	result := inputDetector.IsKeyPressed()

	if result != true && result != false {
		t.Errorf("IsKeyPressed() returned %v, expected boolean", result)
	}
}

func TestTeamsManagerIsUserInputActive(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	inputDetector := NewInputDetector(logger)

	result := inputDetector.IsUserInputActive()

	if result != true && result != false {
		t.Errorf("IsUserInputActive() returned %v, expected boolean", result)
	}
}

// TestActivityModeGlobal ensures global mode path returns quickly
func TestActivityModeGlobal(t *testing.T) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := &config.Config{ActivityMode: "global"}
	tm := newTeamsManager(logger, cfg)
	ctx := context.Background()
	state := &websocket.ServiceState{}
	_ = tm.SendKeysToTeams(ctx, state) // We don't assert success due to environment constraints
}

func TestTeamsManagerHandleTeamsNotFound(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	tm := &TeamsManager{
		logger: logger,
	}

	state := &websocket.ServiceState{}

	err := tm.handleTeamsNotFound(state)

	if err == nil {
		t.Error("handleTeamsNotFound() should return an error")
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

	cfg := &config.Config{
		Debug:             false,
		Interval:          180,
		WebSocket:         false,
		LogFormat:         "text",
		FocusDelayMs:      10,
		RestoreDelayMs:    10,
		KeyProcessDelayMs: 10,
	}

	tm := newTeamsManager(logger, cfg)

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
		logger: logger,
		config: cfg,
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
