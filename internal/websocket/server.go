package websocket

import (
	"context"
	"fmt"
	"log/slog"
	"net"
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

var (
	server        *http.Server
	serverMux     sync.Mutex
	serverRunning bool
)

func StartServer(port int, state *ServiceState) error {
	serverMux.Lock()
	defer serverMux.Unlock()

	if serverRunning {
		return fmt.Errorf("websocket server is already running")
	}

	// Test if port is available by trying to bind to it
	testListener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("port %d is not available: %w", port, err)
	}
	testListener.Close()

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
			serverMux.Lock()
			serverRunning = false
			serverMux.Unlock()
		}
	}()

	serverRunning = true
	return nil
}

func StopServer() {
	serverMux.Lock()
	defer serverMux.Unlock()

	if server != nil && serverRunning {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		serverRunning = false
	}
}
