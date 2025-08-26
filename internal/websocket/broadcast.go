package websocket

import (
	"encoding/json"
	"log/slog"
	"time"

	"golang.org/x/net/websocket"
)

func Broadcast(e *Event, state *ServiceState) {
	state.Mutex.Lock()
	defer state.Mutex.Unlock()

	e.Timestamp = time.Now()
	msg, err := json.Marshal(e)
	if err != nil {
		state.Logger.Error("Error marshaling event", slog.String("error", err.Error()))
		return
	}

	// Clean up disconnected clients and broadcast to active ones
	disconnectedCount := 0
	for conn := range state.Clients {
		if err := websocket.Message.Send(conn, string(msg)); err != nil {
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
