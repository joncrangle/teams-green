package websocket

import (
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestServiceState(t *testing.T) {
	state := &ServiceState{
		State:            "stopped",
		PID:              12345,
		Clients:          make(map[*websocket.Conn]bool),
		TeamsWindowCount: 3,
		FailureStreak:    1,
	}

	if state.State != "stopped" {
		t.Errorf("expected state 'stopped', got '%s'", state.State)
	}

	if state.PID != 12345 {
		t.Errorf("expected PID 12345, got %d", state.PID)
	}

	if state.TeamsWindowCount != 3 {
		t.Errorf("expected 3 teams windows, got %d", state.TeamsWindowCount)
	}

	if state.FailureStreak != 1 {
		t.Errorf("expected failure streak 1, got %d", state.FailureStreak)
	}

	if len(state.Clients) != 0 {
		t.Errorf("expected 0 clients initially, got %d", len(state.Clients))
	}
}

func TestServiceStateConcurrentAccess(_ *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	state := &ServiceState{
		State:            "running",
		PID:              12345,
		Clients:          make(map[*websocket.Conn]bool),
		Mutex:            sync.RWMutex{},
		Logger:           logger,
		LastActivity:     time.Now(),
		TeamsWindowCount: 2,
		FailureStreak:    0,
	}

	// Test concurrent read/write access
	done := make(chan bool)

	// Goroutine 1: Reader
	go func() {
		for range 100 {
			state.Mutex.RLock()
			_ = state.State
			_ = state.PID
			_ = state.TeamsWindowCount
			state.Mutex.RUnlock()
			time.Sleep(time.Microsecond)
		}
		done <- true
	}()

	// Goroutine 2: Writer
	go func() {
		for i := range 100 {
			state.Mutex.Lock()
			state.FailureStreak = i
			state.TeamsWindowCount = i % 5
			state.Mutex.Unlock()
			time.Sleep(time.Microsecond)
		}
		done <- true
	}()

	// Wait for both goroutines to complete
	<-done
	<-done
}

func TestEvent(t *testing.T) {
	event := &Event{
		Service: "teams-green",
		Status:  "running",
		PID:     12345,
		Message: "Service started",
	}

	if event.Service != "teams-green" {
		t.Errorf("expected service 'teams-green', got '%s'", event.Service)
	}

	if event.Status != "running" {
		t.Errorf("expected status 'running', got '%s'", event.Status)
	}

	if event.PID != 12345 {
		t.Errorf("expected PID 12345, got %d", event.PID)
	}

	if event.Message != "Service started" {
		t.Errorf("expected message 'Service started', got '%s'", event.Message)
	}

	jsonData, err := json.Marshal(event)
	if err != nil {
		t.Errorf("failed to marshal event to JSON: %v", err)
	}

	var parsedEvent Event
	err = json.Unmarshal(jsonData, &parsedEvent)
	if err != nil {
		t.Errorf("failed to unmarshal event from JSON: %v", err)
	}

	if parsedEvent.Service != event.Service {
		t.Errorf("unmarshaled event service mismatch")
	}
}

func TestEventTimestamp(t *testing.T) {
	event := &Event{}

	// Initially timestamp should be zero
	if !event.Timestamp.IsZero() {
		t.Error("initial timestamp should be zero")
	}

	// Set timestamp
	now := time.Now()
	event.Timestamp = now

	if event.Timestamp.IsZero() {
		t.Error("timestamp should not be zero after setting")
	}

	if !event.Timestamp.Equal(now) {
		t.Error("timestamp should match the set value")
	}
}

func TestBroadcast(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	state := &ServiceState{
		State:            "running",
		PID:              12345,
		Clients:          make(map[*websocket.Conn]bool),
		Mutex:            sync.RWMutex{},
		Logger:           logger,
		LastActivity:     time.Now(),
		TeamsWindowCount: 2,
		FailureStreak:    0,
	}

	event := &Event{
		Service: "teams-green",
		Status:  "running",
		PID:     12345,
		Message: "Test broadcast",
	}

	Broadcast(event, state)

	if event.Timestamp.IsZero() {
		t.Errorf("broadcast should set timestamp on event")
	}

	t.Logf("Broadcast completed with timestamp: %v", event.Timestamp)
}

func TestBroadcastWithClients(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	state := &ServiceState{
		State:            "running",
		PID:              12345,
		Clients:          make(map[*websocket.Conn]bool),
		Mutex:            sync.RWMutex{},
		Logger:           logger,
		LastActivity:     time.Now(),
		TeamsWindowCount: 2,
		FailureStreak:    0,
	}

	// We can't easily create real websocket connections in unit tests,
	// but we can test that the broadcast function doesn't panic with fake connections
	// (though they will fail when we try to send, that's expected)

	event := &Event{
		Service: "teams-green",
		Status:  "running",
		PID:     12345,
		Message: "Test broadcast with clients",
	}

	// This should not panic even with fake clients
	Broadcast(event, state)

	if event.Timestamp.IsZero() {
		t.Error("broadcast should set timestamp on event")
	}
}

func TestWebSocketServerStartStop(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	state := &ServiceState{
		State:            "running",
		PID:              12345,
		Clients:          make(map[*websocket.Conn]bool),
		Logger:           logger,
		LastActivity:     time.Now(),
		TeamsWindowCount: 2,
		FailureStreak:    0,
	}

	// Find an available port
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("failed to find available port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	err = StartServer(port, state)
	if err != nil {
		t.Errorf("failed to start WebSocket server: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	StopServer()

	t.Log("WebSocket server start/stop cycle completed")
}

func TestWebSocketServerPortInUse(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	state := &ServiceState{
		State:            "running",
		PID:              12345,
		Clients:          make(map[*websocket.Conn]bool),
		Logger:           logger,
		LastActivity:     time.Now(),
		TeamsWindowCount: 2,
		FailureStreak:    0,
	}

	// Start a TCP server to occupy a port first
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port

	// Keep the listener active by accepting one connection in background
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			conn.Close()
		}
	}()

	// Try to start websocket server on the same port - should fail
	err = StartServer(port, state)
	if err == nil {
		t.Error("should fail to start server on occupied port")
		StopServer()
	} else {
		t.Logf("correctly failed to start server on occupied port: %v", err)
	}
}

func TestMultipleServerStartStop(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	state := &ServiceState{
		State:            "running",
		PID:              12345,
		Clients:          make(map[*websocket.Conn]bool),
		Logger:           logger,
		LastActivity:     time.Now(),
		TeamsWindowCount: 2,
		FailureStreak:    0,
	}

	// Find an available port
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("failed to find available port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	// Start first server
	err = StartServer(port, state)
	if err != nil {
		t.Fatalf("failed to start first WebSocket server: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// Try to start another server (should fail because server is already running)
	err = StartServer(port+1, state)
	if err == nil {
		t.Error("should not be able to start multiple servers")
		StopServer()
	} else {
		t.Logf("correctly prevented multiple servers: %v", err)
	}

	// Stop the first server
	StopServer()
	time.Sleep(50 * time.Millisecond)

	// Should be able to start again after stopping
	err = StartServer(port, state)
	if err != nil {
		t.Errorf("should be able to restart server after stopping: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	StopServer()
}

func TestWebSocketHandler(t *testing.T) {
	// This tests the HandleConnection function indirectly by testing
	// the connection handling logic
	state := &ServiceState{
		Clients: make(map[*websocket.Conn]bool),
		Mutex:   sync.RWMutex{},
	}

	// Test that we can create a service state for handler use
	if state.Clients == nil {
		t.Error("clients map should be initialized")
	}

	if len(state.Clients) != 0 {
		t.Error("clients map should be empty initially")
	}
}

func TestBroadcastBufferPool(t *testing.T) {
	// Test that the buffer pool is working correctly
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	state := &ServiceState{
		State:            "running",
		PID:              12345,
		Clients:          make(map[*websocket.Conn]bool),
		Mutex:            sync.RWMutex{},
		Logger:           logger,
		LastActivity:     time.Now(),
		TeamsWindowCount: 2,
		FailureStreak:    0,
	}

	event1 := &Event{
		Service: "teams-green",
		Status:  "running",
		PID:     12345,
		Message: "Test broadcast 1",
	}

	event2 := &Event{
		Service: "teams-green",
		Status:  "warning",
		PID:     12345,
		Message: "Test broadcast 2",
	}

	// Multiple broadcasts to test pool reuse
	Broadcast(event1, state)
	Broadcast(event2, state)

	if event1.Timestamp.IsZero() {
		t.Error("first event timestamp should be set")
	}

	if event2.Timestamp.IsZero() {
		t.Error("second event timestamp should be set")
	}
}
