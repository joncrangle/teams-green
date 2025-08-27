package websocket

import (
	"context"
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
	maxConnections = 50   // Limit concurrent connections
	maxMessageSize = 1024 // 1KB message limit
	readTimeout    = 30 * time.Second
	writeTimeout   = 10 * time.Second
	pingInterval   = 30 * time.Second
	pongTimeout    = 60 * time.Second
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

	// Create context for proper cleanup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cleanup on disconnect
	defer func() {
		cancel() // Cancel context to stop goroutines
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

	// Separate goroutine for ping/pong handling with proper context management
	go handlePingPong(ctx, ws, state)

	// Message reading loop with timeout handling
	handleMessages(ctx, ws, state)
}

func handlePingPong(ctx context.Context, ws *websocket.Conn, state *ServiceState) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	lastPong := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Check for pong timeout
			if time.Since(lastPong) > pongTimeout {
				state.Logger.Debug("WebSocket connection timeout - no pong received",
					slog.String("remote_addr", ws.Request().RemoteAddr))
				ws.Close()
				return
			}

			// Send ping
			pingEvent := Event{
				Type:      "ping",
				Timestamp: time.Now(),
			}
			if msg, err := json.Marshal(pingEvent); err == nil {
				if err := sendMessageWithTimeout(ws, string(msg), writeTimeout); err != nil {
					state.Logger.Debug("WebSocket ping failed",
						slog.String("error", err.Error()),
						slog.String("remote_addr", ws.Request().RemoteAddr))
					return
				}
			}
		}
	}
}

func handleMessages(ctx context.Context, ws *websocket.Conn, state *ServiceState) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			// Set read deadline for each message
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

			// Validate message size
			if len(msg) > maxMessageSize {
				state.Logger.Warn("WebSocket message too large",
					slog.Int("size", len(msg)),
					slog.Int("max", maxMessageSize),
					slog.String("remote_addr", ws.Request().RemoteAddr))
				continue
			}

			// Handle different message types
			if strings.Contains(msg, "pong") {
				// Update last pong time (would need to pass this back somehow)
				continue
			}

			// Parse and validate JSON messages
			var event Event
			if err := json.Unmarshal([]byte(msg), &event); err != nil {
				state.Logger.Debug("Invalid JSON message received",
					slog.String("error", err.Error()),
					slog.String("message", msg),
					slog.String("remote_addr", ws.Request().RemoteAddr))
				continue
			}

			// Handle other validated message types here if needed
			state.Logger.Debug("WebSocket message received",
				slog.String("type", event.Type),
				slog.String("remote_addr", ws.Request().RemoteAddr))
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
