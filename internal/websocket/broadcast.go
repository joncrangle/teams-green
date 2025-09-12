package websocket

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

const (
	// bufferSize is the initial capacity for JSON encoding buffers
	bufferSize = 512
)

var bufferPool = sync.Pool{
	New: func() any {
		return bytes.NewBuffer(make([]byte, 0, bufferSize))
	},
}

// Broadcast sends an event to all connected WebSocket clients.
// It handles JSON marshaling, client cleanup, and error recovery.
// The function is thread-safe and uses a buffer pool for efficient memory usage.
func Broadcast(e *Event, state *ServiceState) {
	state.Mutex.Lock()
	defer state.Mutex.Unlock()

	// Set timestamp for the event
	e.Timestamp = time.Now()

	// Get a buffer from the pool for efficient JSON encoding
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufferPool.Put(buf)

	// Create JSON encoder for this buffer (encoders are not pooled for thread safety)
	encoder := json.NewEncoder(buf)

	// Marshal the event to JSON
	if err := encoder.Encode(e); err != nil {
		state.Logger.Error("Failed to marshal event to JSON",
			slog.String("error", err.Error()),
			slog.String("event_type", e.Service))
		return
	}

	msg := buf.String()

	// Broadcast to all clients and clean up disconnected ones
	disconnectedCount := 0
	for conn := range state.Clients {
		if err := websocket.Message.Send(conn, msg); err != nil {
			state.Logger.Debug("WebSocket client disconnected during broadcast, cleaning up",
				slog.String("error", err.Error()))
			conn.Close()
			delete(state.Clients, conn)
			disconnectedCount++
		}
	}

	// Log cleanup summary if any clients were disconnected
	if disconnectedCount > 0 {
		state.Logger.Debug("Cleaned up disconnected WebSocket clients",
			slog.Int("disconnected_count", disconnectedCount),
			slog.Int("remaining_clients", len(state.Clients)))
	}
}
