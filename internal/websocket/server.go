package websocket

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
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
	serverRunning int32 // Use atomic int32 instead of bool
)

func StartServer(port int, state *ServiceState) error {
	serverMux.Lock()
	defer serverMux.Unlock()

	if atomic.LoadInt32(&serverRunning) == 1 {
		return fmt.Errorf("websocket server is already running")
	}

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

	// Create listener first to ensure port is available and bound
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("failed to bind to port %d: %w", port, err)
	}

	// Use a channel to signal actual server startup completion
	serverReady := make(chan error, 1)

	go func() {
		defer close(serverReady)

		state.Logger.Info("WebSocket server starting",
			slog.String("address", fmt.Sprintf("ws://127.0.0.1:%d/ws", port)))

		if err := server.Serve(listener); err != http.ErrServerClosed {
			state.Logger.Error("WebSocket server error", slog.String("error", err.Error()))
			atomic.StoreInt32(&serverRunning, 0)
			serverReady <- err
			return
		}
		atomic.StoreInt32(&serverRunning, 0)
	}()

	// Give server a moment to actually start serving
	select {
	case err := <-serverReady:
		if err != nil {
			listener.Close()
			return fmt.Errorf("server failed to start: %w", err)
		}
	case <-time.After(100 * time.Millisecond):
		// Server is likely running successfully
	}

	atomic.StoreInt32(&serverRunning, 1)
	return nil
}

func StopServer() {
	serverMux.Lock()
	defer serverMux.Unlock()

	if server != nil && atomic.LoadInt32(&serverRunning) == 1 {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		atomic.StoreInt32(&serverRunning, 0)
	}
}
