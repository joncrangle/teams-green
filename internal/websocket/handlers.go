package websocket

import (
	"encoding/json"
	"io"
	"log/slog"
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
	maxConnections = 50               // Limit concurrent connections
	maxMessageSize = 1024             // 1KB message limit
	readTimeout    = 30 * time.Second // 30 seconds
	writeTimeout   = 10 * time.Second // 10 seconds for writes
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

	// Set connection timeouts
	if err := ws.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		state.Logger.Debug("Failed to set read deadline", slog.String("error", err.Error()))
	}
	if err := ws.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		state.Logger.Debug("Failed to set write deadline", slog.String("error", err.Error()))
	}

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

	// Simple message reading loop
	for {
		if err := ws.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			state.Logger.Debug("Failed to set read deadline", slog.String("error", err.Error()))
			return
		}

		var msg string
		err := websocket.Message.Receive(ws, &msg)
		if err != nil {
			if err != io.EOF {
				state.Logger.Debug("WebSocket read error",
					slog.String("error", err.Error()),
					slog.String("remote_addr", ws.Request().RemoteAddr))
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
	}
}

func sendMessageWithTimeout(ws *websocket.Conn, msg string, timeout time.Duration) error {
	if err := ws.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	return websocket.Message.Send(ws, msg)
}

// GetConnectionStats returns current connection statistics
func GetConnectionStats() map[string]interface{} {
	return map[string]interface{}{
		"active_connections": atomic.LoadInt32(&activeConnections),
		"max_connections":    maxConnections,
		"max_message_size":   maxMessageSize,
	}
}
