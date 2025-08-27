package websocket

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/net/websocket"
)

type Event struct {
	Service   string    `json:"service"`
	Status    string    `json:"status"`
	PID       int       `json:"pid"`
	Timestamp time.Time `json:"timestamp"`
	Message   string    `json:"message,omitempty"`
}

func HandleConnection(ws *websocket.Conn, state *ServiceState) {
	state.Mutex.Lock()
	state.Clients[ws] = true
	state.Mutex.Unlock()

	state.Logger.Info("WebSocket client connected",
		slog.String("remote_addr", ws.Request().RemoteAddr))

	// Send current state immediately
	currentEvent := Event{
		Service: "teams-green",
		Status:  state.State,
		PID:     state.PID,
		Message: "Connected to service",
	}
	if msg, err := json.Marshal(currentEvent); err == nil {
		_ = websocket.Message.Send(ws, string(msg))
	}

	// Cleanup on disconnect
	defer func() {
		state.Mutex.Lock()
		delete(state.Clients, ws)
		state.Mutex.Unlock()
		ws.Close()
		state.Logger.Info("WebSocket client disconnected",
			slog.String("remote_addr", ws.Request().RemoteAddr))
	}()

	// Separate goroutine for ping/pong handling
	pingDone := make(chan struct{})
	go func() {
		defer close(pingDone)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := websocket.Message.Send(ws, `{"type":"ping"}`); err != nil {
					state.Logger.Debug("WebSocket ping failed", slog.String("error", err.Error()))
					return
				}
			case <-pingDone:
				return
			}
		}
	}()

	// Message reading loop
	for {
		var msg string
		err := websocket.Message.Receive(ws, &msg)
		if err != nil {
			if err != io.EOF {
				state.Logger.Debug("WebSocket read error", slog.String("error", err.Error()))
			}
			return
		}
		// Handle pong or other messages
		if strings.Contains(msg, "pong") {
			continue
		}
		// Handle other message types here if needed
	}
}
