package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/joncrangle/teams-green/internal/config"
	"github.com/joncrangle/teams-green/internal/service"
	"github.com/joncrangle/teams-green/internal/websocket"
	ws "golang.org/x/net/websocket"
)

func TestServiceIntegration(t *testing.T) {
	cfg := &config.Config{
		Debug:     false,
		Interval:  10, // Changed to 10 to meet minimum requirement
		WebSocket: false,
		LogFormat: "text",
	}

	// Test config validation
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config should be valid: %v", err)
	}

	// Create service
	svc := service.NewService(cfg)
	if svc == nil {
		t.Fatal("service should not be nil")
	}

	// Test service state management
	svc.SetState("running", false)

	// Test short main loop run
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.MainLoop(ctx, 50*time.Millisecond)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(200 * time.Millisecond):
		t.Error("main loop should have exited")
	}
}

func TestWebSocketServiceIntegration(t *testing.T) {
	cfg := &config.Config{
		Debug:     false,
		Interval:  180,
		WebSocket: true,
		Port:      8768, // Use different port to avoid conflicts
		LogFormat: "text",
	}

	// Test config validation with websocket
	if err := cfg.Validate(); err != nil {
		t.Fatalf("websocket config should be valid: %v", err)
	}

	// Create service
	svc := service.NewService(cfg)
	if svc == nil {
		t.Fatal("service should not be nil")
	}

	// Start websocket server
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	state := &websocket.ServiceState{
		State:            "running",
		PID:              os.Getpid(),
		Clients:          make(map[*ws.Conn]bool),
		Mutex:            sync.RWMutex{},
		Logger:           logger,
		LastActivity:     time.Now(),
		TeamsWindowCount: 1,
		FailureStreak:    0,
	}

	err := websocket.StartServer(cfg.Port, state)
	if err != nil {
		t.Fatalf("failed to start websocket server: %v", err)
	}
	defer websocket.StopServer()

	// Test broadcasting events
	event := &websocket.Event{
		Service: "teams-green",
		Status:  "running",
		PID:     os.Getpid(),
		Message: "Integration test",
	}

	websocket.Broadcast(event, state)

	if event.Timestamp.IsZero() {
		t.Error("event timestamp should be set during broadcast")
	}

	// Test state changes with websocket enabled
	svc.SetState("stopped", true)
	time.Sleep(50 * time.Millisecond) // Allow time for broadcast
}

func TestConfigLogRotationIntegration(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "integration.log")

	cfg := &config.Config{
		Debug:      false,
		Interval:   180,
		WebSocket:  false,
		LogFormat:  "text",
		LogFile:    logFile,
		LogRotate:  true,
		MaxLogSize: 1, // 1 MB
		MaxLogAge:  1, // 1 day
	}

	// Test config validation with log rotation
	if err := cfg.Validate(); err != nil {
		t.Fatalf("log rotation config should be valid: %v", err)
	}

	// Initialize logger with rotation
	logger := config.InitLogger(cfg)
	if logger == nil {
		t.Fatal("logger should not be nil")
	}

	// Create service with logging
	svc := service.NewService(cfg)
	if svc == nil {
		t.Fatal("service should not be nil")
	}

	// Test that log file is created
	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		t.Error("log file should have been created")
	}

	// Force garbage collection to help close any open file handles
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
}

func TestPIDFileIntegration(t *testing.T) {
	// Test PID file operations integration

	// Ensure no existing PID file
	if _, err := os.Stat(config.PidFile); !os.IsNotExist(err) {
		os.Remove(config.PidFile)
	}

	// Test status when no service running
	running, pid, info, err := service.GetEnhancedStatus()
	if err != nil {
		t.Errorf("should not error when no service running: %v", err)
	}

	if running {
		t.Error("service should not be running")
	}

	if pid != 0 {
		t.Errorf("expected PID 0 when not running, got %d", pid)
	}

	if info != nil {
		t.Error("info should be nil when not running")
	}
}

func TestServiceStartStopIntegration(t *testing.T) {
	// This test focuses on the integration between config validation,
	// service creation, and basic operations without actually starting processes

	cfg := &config.Config{
		Debug:    false,
		Interval: 180,
	}

	// Test config validation
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config should be valid: %v", err)
	}

	// Create service
	svc := service.NewService(cfg)
	if svc == nil {
		t.Fatal("service should not be nil")
	}

	// Test state transitions
	svc.SetState("starting", false)
	svc.SetState("running", false)
	svc.SetState("stopping", false)
	svc.SetState("stopped", false)

	// Test short context cancellation
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.MainLoop(ctx, 100*time.Millisecond)
	}()

	select {
	case <-done:
		// Success - loop exited due to context cancellation
	case <-time.After(100 * time.Millisecond):
		t.Error("main loop should have exited immediately due to cancelled context")
	}
}

func TestMultipleServiceInstancesPrevention(t *testing.T) {
	// Test that multiple service instances cannot be created simultaneously
	// by testing the PID file locking mechanism

	// Clean up any existing PID file
	os.Remove(config.PidFile)

	// First instance check - should succeed
	running, _, _, err := service.GetEnhancedStatus()
	if err != nil {
		t.Errorf("first status check should not error: %v", err)
	}

	if running {
		t.Error("no service should be running initially")
	}

	// The actual prevention happens in the Start() function which would
	// write a PID file and check for existing processes
	// We test the status checking logic here
}

func TestTeamsWindowDiscoveryIntegration(t *testing.T) {
	// Test the Teams window discovery integration
	cfg := &config.Config{
		Debug:     false,
		Interval:  180,
		WebSocket: false,
		LogFormat: "text",
	}

	svc := service.NewService(cfg)
	if svc == nil {
		t.Fatal("service should not be nil")
	}

	// This test mainly verifies that the Teams discovery functions
	// don't panic and return reasonable results even when Teams isn't running

	// The actual Teams window finding is tested in service_test.go
	// Here we test that it integrates properly with the service

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Run a very short main loop to test Teams finding integration
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.MainLoop(ctx, 25*time.Millisecond)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(100 * time.Millisecond):
		t.Error("main loop should have completed")
	}
}
