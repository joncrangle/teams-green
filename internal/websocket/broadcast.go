package websocket

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

var (
	bufferPool = sync.Pool{
		New: func() interface{} {
			return bytes.NewBuffer(make([]byte, 0, 512))
		},
	}
	encoderPool = sync.Pool{
		New: func() interface{} {
			buf := bufferPool.Get().(*bytes.Buffer)
			buf.Reset()
			return json.NewEncoder(buf)
		},
	}
)

func Broadcast(e *Event, state *ServiceState) {
	state.Mutex.Lock()
	defer state.Mutex.Unlock()

	e.Timestamp = time.Now()

	// Get buffer and encoder from pool
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufferPool.Put(buf)

	encoder := encoderPool.Get().(*json.Encoder)
	defer encoderPool.Put(encoder)

	// Reset encoder with the buffer
	encoder = json.NewEncoder(buf)

	if err := encoder.Encode(e); err != nil {
		state.Logger.Error("Error marshaling event", slog.String("error", err.Error()))
		return
	}

	msg := buf.String()

	// Clean up disconnected clients and broadcast to active ones
	disconnectedCount := 0
	for conn := range state.Clients {
		if err := websocket.Message.Send(conn, msg); err != nil {
			state.Logger.Debug("WebSocket client disconnected during broadcast",
				slog.String("error", err.Error()))
			conn.Close()
			delete(state.Clients, conn)
			disconnectedCount++
		}
	}

	if disconnectedCount > 0 {
		state.Logger.Debug("Cleaned up disconnected clients",
			slog.Int("disconnected_count", disconnectedCount),
			slog.Int("active_clients", len(state.Clients)))
	}
}
