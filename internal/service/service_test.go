package service

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/joncrangle/teams-green/internal/config"
	"github.com/joncrangle/teams-green/internal/websocket"
	"github.com/lxn/win"
)

func TestNewService(t *testing.T) {
	cfg := &config.Config{
		Debug:     false,
		Interval:  180,
		WebSocket: false,
		LogFormat: "text",
	}

	svc := NewService(cfg)
	if svc == nil {
		t.Fatal("expected service but got nil")
	}

	if svc.logger == nil {
		t.Error("service logger should not be nil")
	}

	if svc.config == nil {
		t.Error("service config should not be nil")
	}

	if svc.state == nil {
		t.Error("service state should not be nil")
	}

	if svc.teamsMgr == nil {
		t.Error("service teams manager should not be nil")
	}

	if svc.state.State != "stopped" {
		t.Errorf("initial state should be 'stopped', got '%s'", svc.state.State)
	}

	if svc.state.PID != os.Getpid() {
		t.Errorf("expected PID %d, got %d", os.Getpid(), svc.state.PID)
	}
}

func TestServiceSetState(t *testing.T) {
	cfg := &config.Config{
		Debug:     false,
		Interval:  180,
		WebSocket: false,
		LogFormat: "text",
	}

	svc := NewService(cfg)

	// Test setting state without emitting
	svc.SetState("running", false)
	if svc.state.State != "running" {
		t.Errorf("expected state 'running', got '%s'", svc.state.State)
	}

	// Test setting state with emitting (should not panic when websocket disabled)
	svc.SetState("stopped", true)
	if svc.state.State != "stopped" {
		t.Errorf("expected state 'stopped', got '%s'", svc.state.State)
	}
}

func TestServiceSetStateWithWebSocket(t *testing.T) {
	cfg := &config.Config{
		Debug:     false,
		Interval:  180,
		WebSocket: true,
		Port:      8766,
		LogFormat: "text",
	}

	svc := NewService(cfg)

	// Start websocket server for testing
	err := websocket.StartServer(cfg.Port, svc.state)
	if err != nil {
		t.Fatalf("failed to start websocket server: %v", err)
	}
	defer websocket.StopServer()

	// Test setting state with emitting
	svc.SetState("running", true)
	if svc.state.State != "running" {
		t.Errorf("expected state 'running', got '%s'", svc.state.State)
	}
}

func TestServiceMainLoop(t *testing.T) {
	cfg := &config.Config{
		Debug:     false,
		Interval:  180,
		WebSocket: false,
		LogFormat: "text",
	}

	svc := NewService(cfg)

	// Create a context that will cancel quickly
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Run main loop for a short time
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.MainLoop(ctx, 50*time.Millisecond)
	}()

	// Wait for completion or timeout
	select {
	case <-done:
		// Success
	case <-time.After(200 * time.Millisecond):
		t.Error("main loop did not exit in time")
	}
}

func TestTeamsManager(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	tm := &TeamsManager{
		windowCache: &WindowCache{
			Windows:       make([]TeamsWindow, 0),
			CacheDuration: 30 * time.Second,
		},
		retryState: &RetryState{
			MaxBackoff: 300,
		},
		logger: logger,
	}

	if tm.windowCache == nil {
		t.Error("window cache should not be nil")
	}

	if tm.retryState == nil {
		t.Error("retry state should not be nil")
	}

	if tm.logger == nil {
		t.Error("logger should not be nil")
	}
}

func TestWindowCache(t *testing.T) {
	cache := &WindowCache{
		Windows:       make([]TeamsWindow, 0),
		CacheDuration: 30 * time.Second,
		Mutex:         sync.RWMutex{},
	}

	// Test initial state
	if len(cache.Windows) != 0 {
		t.Error("initial window cache should be empty")
	}

	// Add a test window
	testWindow := TeamsWindow{
		HWND:           win.HWND(12345),
		ProcessID:      1000,
		ExecutablePath: "c:\\test\\teams.exe",
		WindowTitle:    "Microsoft Teams",
		LastSeen:       time.Now(),
		IsValid:        true,
	}

	cache.Mutex.Lock()
	cache.Windows = append(cache.Windows, testWindow)
	cache.LastUpdate = time.Now()
	cache.Mutex.Unlock()

	cache.Mutex.RLock()
	if len(cache.Windows) != 1 {
		t.Errorf("expected 1 window in cache, got %d", len(cache.Windows))
	}

	window := cache.Windows[0]
	if window.ProcessID != 1000 {
		t.Errorf("expected process ID 1000, got %d", window.ProcessID)
	}
	cache.Mutex.RUnlock()
}

func TestRetryState(t *testing.T) {
	retry := &RetryState{
		MaxBackoff: 300,
	}

	// Test initial state
	if retry.FailureCount != 0 {
		t.Error("initial failure count should be 0")
	}

	if retry.BackoffSeconds != 0 {
		t.Error("initial backoff should be 0")
	}

	// Simulate failure
	retry.FailureCount++
	retry.ConsecutiveFailures++
	retry.LastFailure = time.Now()

	if retry.FailureCount != 1 {
		t.Errorf("expected failure count 1, got %d", retry.FailureCount)
	}

	if retry.ConsecutiveFailures != 1 {
		t.Errorf("expected consecutive failures 1, got %d", retry.ConsecutiveFailures)
	}
}

func TestTeamsWindow(t *testing.T) {
	window := TeamsWindow{
		HWND:           win.HWND(12345),
		ProcessID:      1000,
		ExecutablePath: "c:\\test\\teams.exe",
		WindowTitle:    "Microsoft Teams",
		LastSeen:       time.Now(),
		IsValid:        true,
	}

	if window.HWND != win.HWND(12345) {
		t.Errorf("expected HWND 12345, got %d", window.HWND)
	}

	if window.ProcessID != 1000 {
		t.Errorf("expected process ID 1000, got %d", window.ProcessID)
	}

	if !window.IsValid {
		t.Error("window should be valid")
	}

	if window.ExecutablePath != "c:\\test\\teams.exe" {
		t.Errorf("expected executable path 'c:\\test\\teams.exe', got '%s'", window.ExecutablePath)
	}
}

func TestDiscoverTeamsExecutables(t *testing.T) {
	executables := getTeamsExecutables()

	if len(executables) == 0 {
		t.Error("should discover at least some teams executables")
	}

	// Check for basic expected executables
	expectedExes := []string{"ms-teams.exe", "teams.exe", "msteams.exe"}
	for _, expected := range expectedExes {
		found := false
		for _, exe := range executables {
			if exe == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected to find executable '%s' in discovered list", expected)
		}
	}
}
