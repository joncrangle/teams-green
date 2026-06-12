package websocket

import (
	"encoding/json"
	"log/slog"
	"time"

	"golang.org/x/net/websocket"
)

// Broadcast sends an event to all connected WebSocket clients.
// It handles JSON marshaling, client cleanup, and error recovery.
// The function is thread-safe and snapshots clients under a read lock
// before sending to avoid holding the lock during network I/O.
func Broadcast(e *Event, state *ServiceState) {
	// Set timestamp for the event
	e.Timestamp = time.Now()

	// Marshal the event to JSON
	data, err := json.Marshal(e)
	if err != nil {
		slog.Error("Failed to marshal event to JSON",
			slog.String("error", err.Error()),
			slog.String("event_type", e.Service))
		return
	}
	msg := string(data)

	// Snapshot clients under read lock to avoid holding the lock during network I/O
	state.Mutex.RLock()
	clients := make([]*websocket.Conn, 0, len(state.Clients))
	for conn := range state.Clients {
		clients = append(clients, conn)
	}
	state.Mutex.RUnlock()

	// Send to all clients outside the lock
	var disconnected []*websocket.Conn
	for _, conn := range clients {
		if err := websocket.Message.Send(conn, msg); err != nil {
			slog.Debug("WebSocket client disconnected during broadcast, cleaning up",
				slog.String("error", err.Error()))
			disconnected = append(disconnected, conn)
		}
	}

	// Clean up disconnected clients under write lock
	if len(disconnected) > 0 {
		state.Mutex.Lock()
		for _, conn := range disconnected {
			if _, ok := state.Clients[conn]; ok {
				conn.Close()
				delete(state.Clients, conn)
			}
		}
		state.Mutex.Unlock()

		slog.Debug("Cleaned up disconnected WebSocket clients",
			slog.Int("disconnected_count", len(disconnected)))
	}
}
