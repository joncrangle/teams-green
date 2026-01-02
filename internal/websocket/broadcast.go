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
	bufferSize = 2048
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
		slog.Error("Failed to marshal event to JSON",
			slog.String("error", err.Error()),
			slog.String("event_type", e.Service))
		return
	}

	msg := buf.String()

	// Collect disconnected connections first, then clean up
	var disconnected []*websocket.Conn
	for conn := range state.Clients {
		// Simple connection validation - try to send
		if err := websocket.Message.Send(conn, msg); err != nil {
			slog.Debug("WebSocket client disconnected during broadcast, cleaning up",
				slog.String("error", err.Error()))
			disconnected = append(disconnected, conn)
		}
	}

	// Clean up disconnected clients after iteration
	for _, conn := range disconnected {
		conn.Close()
		delete(state.Clients, conn)
	}

	// Log cleanup summary if any clients were disconnected
	if len(disconnected) > 0 {
		slog.Debug("Cleaned up disconnected WebSocket clients",
			slog.Int("disconnected_count", len(disconnected)),
			slog.Int("remaining_clients", len(state.Clients)))
	}
}
