package websocket

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
	"golang.org/x/time/rate"
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
	maxConnections = 50        // Limit concurrent connections
	maxMessageSize = 10 * 1024 // 10KB maximum message size (DoS protection)
)

const (
	messagesPerSecond = 10
	rateBurstSize     = 20
)

var (
	activeConnections int32 // Atomic counter for active connections

	// Rate limiter for WebSocket message processing
	limiter = rate.NewLimiter(rate.Limit(messagesPerSecond), rateBurstSize)
)

// HandleConnection manages a WebSocket client connection with proper lifecycle management,
// connection limits, keep-alive handling, and message processing.
func HandleConnection(ws *websocket.Conn, state *ServiceState, config ConfigProvider) {
	// Check and enforce connection limits
	if !checkConnectionLimit(ws, state) {
		return
	}

	// Register client and send initial state
	setupClientConnection(ws, state, config)

	// Set up cleanup handler
	defer cleanupClientConnection(ws, state)

	// Handle incoming messages
	handleClientMessages(ws, state, config)
}

// checkConnectionLimit enforces the maximum number of concurrent connections.
// Uses atomic compare-and-swap to prevent race conditions.
func checkConnectionLimit(ws *websocket.Conn, _ *ServiceState) bool {
	for {
		current := atomic.LoadInt32(&activeConnections)
		if current >= maxConnections {
			slog.Warn("Connection rejected - too many active connections",
				slog.Int("active", int(current)),
				slog.Int("max", maxConnections))
			ws.Close()
			return false
		}
		// Try to increment atomically
		if atomic.CompareAndSwapInt32(&activeConnections, current, current+1) {
			return true
		}
		// CAS failed, retry loop
	}
}

// setupClientConnection registers the client and sends the current service state.
func setupClientConnection(ws *websocket.Conn, state *ServiceState, config ConfigProvider) {
	// Register client
	state.Mutex.Lock()
	state.Clients[ws] = true
	clientCount := len(state.Clients)
	state.Mutex.Unlock()

	slog.Info("WebSocket client connected",
		slog.String("remote_addr", ws.Request().RemoteAddr),
		slog.Int("total_clients", clientCount),
		slog.Int("active_connections", int(atomic.LoadInt32(&activeConnections))))

	// Send current service state to new client
	sendInitialState(ws, state, config)
}

// sendInitialState sends the current service status to a newly connected client.
func sendInitialState(ws *websocket.Conn, state *ServiceState, config ConfigProvider) {
	currentEvent := Event{
		Service:   "teams-green",
		Status:    state.State,
		PID:       state.PID,
		Type:      "status",
		Timestamp: time.Now(),
	}
	if msg, err := json.Marshal(currentEvent); err == nil {
		_ = sendMessageWithTimeout(ws, string(msg), config.GetWebSocketWriteTimeout())
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
	slog.Info("WebSocket client disconnected",
		slog.String("remote_addr", ws.Request().RemoteAddr),
		slog.Int("remaining_clients", remainingClients),
		slog.Int("active_connections", int(atomic.LoadInt32(&activeConnections))))
}

// handleClientMessages processes incoming WebSocket messages in a loop.
func handleClientMessages(ws *websocket.Conn, state *ServiceState, config ConfigProvider) {
	for {
		if err := ws.SetReadDeadline(time.Now().Add(config.GetWebSocketReadTimeout())); err != nil {
			slog.Debug("Failed to set read deadline", slog.String("error", err.Error()))
			return
		}

		var msg string
		err := websocket.Message.Receive(ws, &msg)
		if err != nil {
			handleReceiveError(err, ws, state)
			return
		}

		// Check message size
		if len(msg) >= maxMessageSize {
			slog.Warn("Message too large, closing connection",
				slog.Int("size", len(msg)),
				slog.Int("max", maxMessageSize),
				slog.String("remote_addr", ws.Request().RemoteAddr))
			ws.Close()
			return
		}

		// Rate limit check before processing
		if !limiter.Allow() {
			slog.Debug("Rate limit exceeded, dropping message",
				slog.String("remote_addr", ws.Request().RemoteAddr))
			continue
		}

		// Process message
		if err := handleMessage(ws, msg, state, config); err != nil {
			slog.Debug("Error handling message",
				slog.String("error", err.Error()),
				slog.String("remote_addr", ws.Request().RemoteAddr))
		}
	}
}

// handleReceiveError processes WebSocket receive errors appropriately.
func handleReceiveError(err error, ws *websocket.Conn, _ *ServiceState) {
	if err == io.EOF {
		return // Clean disconnect
	}

	if isTimeoutError(err) {
		slog.Debug("WebSocket read timeout (client may be idle)",
			slog.String("remote_addr", ws.Request().RemoteAddr))
	} else {
		slog.Debug("WebSocket read error",
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
func handleMessage(ws *websocket.Conn, msg string, state *ServiceState, config ConfigProvider) error {
	// Try to parse as JSON event
	var event Event
	if err := json.Unmarshal([]byte(msg), &event); err != nil {
		// Log malformed JSON at debug level for troubleshooting
		slog.Debug("Malformed WebSocket message",
			slog.String("error", err.Error()),
			slog.String("remote_addr", ws.Request().RemoteAddr))
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
			return sendMessageWithTimeout(ws, string(pongMsg), config.GetWebSocketWriteTimeout())
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
