package websocket

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestServiceState(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	state := &ServiceState{
		State:            "stopped",
		PID:              12345,
		Clients:          make(map[*websocket.Conn]bool),
		Logger:           logger,
		LastActivity:     time.Now(),
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

func TestWebSocketServerOperations(t *testing.T) {
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

	err := StartServer(8766, state)
	if err != nil {
		t.Errorf("failed to start WebSocket server: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	StopServer()

	t.Log("WebSocket server start/stop cycle completed")
}
