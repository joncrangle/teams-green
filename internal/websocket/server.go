package websocket

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

type ServiceState struct {
	State            string
	PID              int
	Clients          map[*websocket.Conn]bool
	Mutex            sync.RWMutex
	Logger           *slog.Logger
	LastActivity     time.Time
	TeamsWindowCount int
	FailureStreak    int
}

var server *http.Server

func StartServer(port int, state *ServiceState) error {
	mux := http.NewServeMux()
	mux.Handle("/ws", websocket.Handler(func(ws *websocket.Conn) {
		HandleConnection(ws, state)
	}))

	server = &http.Server{
		Addr:         fmt.Sprintf("127.0.0.1:%d", port),
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		state.Logger.Info("WebSocket server starting",
			slog.String("address", fmt.Sprintf("ws://127.0.0.1:%d/ws", port)))
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			state.Logger.Error("WebSocket server error", slog.String("error", err.Error()))
		}
	}()

	return nil
}

func StopServer() {
	if server != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}
}
