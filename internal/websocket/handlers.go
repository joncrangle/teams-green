package websocket

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
)

// Event represents a WebSocket message exchanged between the service and clients.
// It contains service status information and control messages like ping/pong.
type Event struct {
	Service   string    `json:"service"`           // Service name (e.g., "teams-green")
	Status    string    `json:"status"`            // Current service status
	PID       int       `json:"pid"`               // Process ID of the service
	Timestamp time.Time `json:"timestamp"`         // When the event was created
	Message   string    `json:"message,omitempty"` // Optional human-readable message
	Type      string    `json:"type,omitempty"`    // Message type: "ping", "pong", "status", "keepalive"
}

const (
	maxConnections = 50               // Limit concurrent connections
	readTimeout    = 30 * time.Second // 30 seconds - client reconnects every 5s on failure
	writeTimeout   = 30 * time.Second // 30 seconds for writes
)

var activeConnections int32 // Atomic counter for active connections

// HandleConnection manages a WebSocket client connection with proper lifecycle management,
// connection limits, keep-alive handling, and message processing.
func HandleConnection(ws *websocket.Conn, state *ServiceState) {
	// Check and enforce connection limits
	if !checkConnectionLimit(ws, state) {
		return
	}

	// Register client and send initial state
	setupClientConnection(ws, state)

	// Set up cleanup handler
	defer cleanupClientConnection(ws, state)

	// Handle incoming messages
	handleClientMessages(ws, state)
}

// checkConnectionLimit enforces the maximum number of concurrent connections.
func checkConnectionLimit(ws *websocket.Conn, state *ServiceState) bool {
	if atomic.LoadInt32(&activeConnections) >= maxConnections {
		state.Logger.Warn("Connection rejected - too many active connections",
			slog.Int("active", int(atomic.LoadInt32(&activeConnections))),
			slog.Int("max", maxConnections))
		ws.Close()
		return false
	}

	atomic.AddInt32(&activeConnections, 1)
	return true
}

// setupClientConnection registers the client and sends the current service state.
func setupClientConnection(ws *websocket.Conn, state *ServiceState) {
	// Register client
	state.Mutex.Lock()
	state.Clients[ws] = true
	clientCount := len(state.Clients)
	state.Mutex.Unlock()

	state.Logger.Info("WebSocket client connected",
		slog.String("remote_addr", ws.Request().RemoteAddr),
		slog.Int("total_clients", clientCount),
		slog.Int("active_connections", int(atomic.LoadInt32(&activeConnections))))

	// Send current service state to new client
	sendInitialState(ws, state)
}

// sendInitialState sends the current service status to a newly connected client.
func sendInitialState(ws *websocket.Conn, state *ServiceState) {
	currentEvent := Event{
		Service:   "teams-green",
		Status:    state.State,
		PID:       state.PID,
		Type:      "status",
		Timestamp: time.Now(),
	}
	if msg, err := json.Marshal(currentEvent); err == nil {
		_ = sendMessageWithTimeout(ws, string(msg), writeTimeout)
	}
}

// cleanupClientConnection removes the client from the registry and closes the connection.
func cleanupClientConnection(ws *websocket.Conn, state *ServiceState) {
	atomic.AddInt32(&activeConnections, -1)

	state.Mutex.Lock()
	delete(state.Clients, ws)
	remainingClients := len(state.Clients)
	state.Mutex.Unlock()

	ws.Close()
	state.Logger.Info("WebSocket client disconnected",
		slog.String("remote_addr", ws.Request().RemoteAddr),
		slog.Int("remaining_clients", remainingClients),
		slog.Int("active_connections", int(atomic.LoadInt32(&activeConnections))))
}

// handleClientMessages processes incoming WebSocket messages in a loop.
func handleClientMessages(ws *websocket.Conn, state *ServiceState) {
	for {
		if err := ws.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			state.Logger.Debug("Failed to set read deadline", slog.String("error", err.Error()))
			return
		}

		var msg string
		err := websocket.Message.Receive(ws, &msg)
		if err != nil {
			handleReceiveError(err, ws, state)
			return
		}

		// Process the message
		if err := handleMessage(ws, msg, state); err != nil {
			state.Logger.Debug("Error handling message",
				slog.String("error", err.Error()),
				slog.String("remote_addr", ws.Request().RemoteAddr))
			return
		}
	}
}

// handleReceiveError processes WebSocket receive errors appropriately.
func handleReceiveError(err error, ws *websocket.Conn, state *ServiceState) {
	if err == io.EOF {
		return // Clean disconnect
	}

	if isTimeoutError(err) {
		state.Logger.Debug("WebSocket read timeout (client may be idle)",
			slog.String("remote_addr", ws.Request().RemoteAddr))
	} else {
		state.Logger.Debug("WebSocket read error",
			slog.String("error", err.Error()),
			slog.String("remote_addr", ws.Request().RemoteAddr))
	}
}

// sendMessageWithTimeout sends a WebSocket message with a write deadline to prevent hanging.
func sendMessageWithTimeout(ws *websocket.Conn, msg string, timeout time.Duration) error {
	if err := ws.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	return websocket.Message.Send(ws, msg)
}

// handleMessage processes incoming WebSocket messages.
func handleMessage(ws *websocket.Conn, msg string, state *ServiceState) error {
	// Try to parse as JSON event
	var event Event
	if err := json.Unmarshal([]byte(msg), &event); err != nil {
		// Not JSON, treat as simple string message
		return nil
	}

	// Handle ping messages by sending a pong response (for client compatibility)
	if event.Type == "ping" {
		pongEvent := Event{
			Service:   "teams-green",
			Status:    state.State,
			PID:       state.PID,
			Message:   "pong",
			Type:      "pong",
			Timestamp: time.Now(),
		}
		if pongMsg, err := json.Marshal(pongEvent); err == nil {
			return sendMessageWithTimeout(ws, string(pongMsg), writeTimeout)
		}
	}

	return nil
}

// isTimeoutError checks if the error is a timeout-related error by examining the error message.
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "i/o timeout")
}
