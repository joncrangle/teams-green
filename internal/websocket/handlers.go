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

type Event struct {
	Service   string    `json:"service"`
	Status    string    `json:"status"`
	PID       int       `json:"pid"`
	Timestamp time.Time `json:"timestamp"`
	Message   string    `json:"message,omitempty"`
	Type      string    `json:"type,omitempty"` // ping, pong, etc.
}

const (
	maxConnections    = 50                // Limit concurrent connections
	maxMessageSize    = 1024              // 1KB message limit
	readTimeout       = 120 * time.Second // 2 minutes - increased for stability
	writeTimeout      = 30 * time.Second  // 30 seconds for writes
	pingInterval      = 30 * time.Second  // Send ping every 30 seconds
	keepAliveInterval = 45 * time.Second  // Send keep-alive message every 45 seconds
)

var activeConnections int32 // Atomic counter for active connections

func HandleConnection(ws *websocket.Conn, state *ServiceState) {
	// Check connection limits
	if atomic.LoadInt32(&activeConnections) >= maxConnections {
		state.Logger.Warn("Connection rejected - too many active connections",
			slog.Int("active", int(atomic.LoadInt32(&activeConnections))),
			slog.Int("max", maxConnections))
		ws.Close()
		return
	}

	atomic.AddInt32(&activeConnections, 1)
	defer atomic.AddInt32(&activeConnections, -1)

	// Set max message size
	ws.MaxPayloadBytes = maxMessageSize

	state.Mutex.Lock()
	state.Clients[ws] = true
	clientCount := len(state.Clients)
	state.Mutex.Unlock()

	state.Logger.Info("WebSocket client connected",
		slog.String("remote_addr", ws.Request().RemoteAddr),
		slog.Int("total_clients", clientCount),
		slog.Int("active_connections", int(atomic.LoadInt32(&activeConnections))))

	// Send current state immediately
	currentEvent := Event{
		Service: "teams-green",
		Status:  state.State,
		PID:     state.PID,
		Message: "Connected to service",
		Type:    "status",
	}
	if msg, err := json.Marshal(currentEvent); err == nil {
		_ = sendMessageWithTimeout(ws, string(msg), writeTimeout)
	}

	// Cleanup on disconnect
	defer func() {
		state.Mutex.Lock()
		delete(state.Clients, ws)
		remainingClients := len(state.Clients)
		state.Mutex.Unlock()

		ws.Close()
		state.Logger.Info("WebSocket client disconnected",
			slog.String("remote_addr", ws.Request().RemoteAddr),
			slog.Int("remaining_clients", remainingClients),
			slog.Int("active_connections", int(atomic.LoadInt32(&activeConnections))))
	}()

	// Create channels for coordinating goroutines
	done := make(chan struct{})
	defer close(done)

	// Start keep-alive goroutine
	go keepAliveHandler(ws, state, done)

	// Message reading loop with improved error handling
	for {
		if err := ws.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			state.Logger.Debug("Failed to set read deadline", slog.String("error", err.Error()))
			return
		}

		var msg string
		err := websocket.Message.Receive(ws, &msg)
		if err != nil {
			if err != io.EOF {
				// Only log timeout errors at debug level to reduce noise
				if isTimeoutError(err) {
					state.Logger.Debug("WebSocket read timeout (client may be idle)",
						slog.String("remote_addr", ws.Request().RemoteAddr))
				} else {
					state.Logger.Debug("WebSocket read error",
						slog.String("error", err.Error()),
						slog.String("remote_addr", ws.Request().RemoteAddr))
				}
			}
			return
		}

		if len(msg) > maxMessageSize {
			state.Logger.Warn("WebSocket message too large",
				slog.Int("size", len(msg)),
				slog.Int("max", maxMessageSize),
				slog.String("remote_addr", ws.Request().RemoteAddr))
			continue
		}

		// Handle ping/pong messages
		if err := handleMessage(ws, msg, state); err != nil {
			state.Logger.Debug("Error handling message",
				slog.String("error", err.Error()),
				slog.String("remote_addr", ws.Request().RemoteAddr))
			return
		}
	}
}

func sendMessageWithTimeout(ws *websocket.Conn, msg string, timeout time.Duration) error {
	if err := ws.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	return websocket.Message.Send(ws, msg)
}

// keepAliveHandler sends periodic keep-alive messages to maintain connection
func keepAliveHandler(ws *websocket.Conn, state *ServiceState, done <-chan struct{}) {
	keepAliveTicker := time.NewTicker(keepAliveInterval)
	defer keepAliveTicker.Stop()

	for {
		select {
		case <-done:
			return
		case <-keepAliveTicker.C:
			keepAliveEvent := Event{
				Service:   "teams-green",
				Status:    "alive",
				PID:       state.PID,
				Message:   "Keep-alive",
				Type:      "keepalive",
				Timestamp: time.Now(),
			}

			if msg, err := json.Marshal(keepAliveEvent); err == nil {
				if err := sendMessageWithTimeout(ws, string(msg), writeTimeout); err != nil {
					state.Logger.Debug("Failed to send keep-alive",
						slog.String("error", err.Error()),
						slog.String("remote_addr", ws.Request().RemoteAddr))
					return
				}
			}
		}
	}
}

// handleMessage processes incoming WebSocket messages
func handleMessage(ws *websocket.Conn, msg string, state *ServiceState) error {
	// Try to parse as JSON event
	var event Event
	if err := json.Unmarshal([]byte(msg), &event); err != nil {
		// Not JSON, treat as simple string message
		return nil
	}

	// Handle different message types
	switch event.Type {
	case "ping":
		// Respond with pong
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
	case "pong":
		// Client responded to our ping, connection is healthy
		state.Logger.Debug("Received pong from client",
			slog.String("remote_addr", ws.Request().RemoteAddr))
	}

	return nil
}

// isTimeoutError checks if the error is a timeout error
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "i/o timeout")
}

// GetConnectionStats returns current connection statistics
func GetConnectionStats() map[string]any {
	return map[string]any{
		"active_connections":         atomic.LoadInt32(&activeConnections),
		"max_connections":            maxConnections,
		"max_message_size":           maxMessageSize,
		"ping_interval_seconds":      int(pingInterval.Seconds()),
		"keepalive_interval_seconds": int(keepAliveInterval.Seconds()),
		"read_timeout_seconds":       int(readTimeout.Seconds()),
		"write_timeout_seconds":      int(writeTimeout.Seconds()),
	}
}
