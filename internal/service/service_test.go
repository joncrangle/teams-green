package service

import (
	"context"
	"log/slog"
	"net"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/joncrangle/teams-green/internal/config"
	"github.com/joncrangle/teams-green/internal/websocket"
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
	// Find an available port
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("failed to find available port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	cfg := &config.Config{
		Debug:     false,
		Interval:  180,
		WebSocket: true,
		Port:      port,
		LogFormat: "text",
	}

	svc := NewService(cfg)

	// Start websocket server for testing
	err = websocket.StartServer(cfg.Port, svc.state)
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

	// Create a context that cancels immediately for predictable behavior
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// Run main loop with canceled context
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.MainLoop(ctx, 100*time.Millisecond)
	}()

	// Wait for completion with generous timeout since context is already canceled
	select {
	case <-done:
		// Success - main loop should exit immediately due to canceled context
	case <-time.After(500 * time.Millisecond):
		t.Error("main loop did not exit in time with canceled context")
	}
}

func TestTeamsManager(t *testing.T) {
	cfg := &config.Config{
		Debug:     false,
		Interval:  180,
		WebSocket: false,
		LogFormat: "text",
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	tm := &TeamsManager{
		logger: logger,
		config: cfg,
	}

	if tm.logger == nil {
		t.Error("logger should not be nil")
	}

	if tm.config == nil {
		t.Error("config should not be nil")
	}
}

func TestDiscoverTeamsExecutables(t *testing.T) {
	executables := teamsExecutables

	if len(executables) == 0 {
		t.Error("should discover at least some teams executables")
	}

	// Check for basic expected executables
	expectedExes := []string{"ms-teams.exe", "teams.exe", "msteams.exe"}
	for _, expected := range expectedExes {
		found := slices.Contains(executables, expected)
		if !found {
			t.Errorf("expected to find executable '%s' in discovered list", expected)
		}
	}
}
