package websocket

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
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
	listener      net.Listener
	serverMux     sync.Mutex
	serverRunning int32
	serverCancel  context.CancelFunc
)

func StartServer(port int, state *ServiceState) error {
	serverMux.Lock()
	defer serverMux.Unlock()

	if atomic.LoadInt32(&serverRunning) == 1 {
		return fmt.Errorf("websocket server is already running")
	}

	var err error
	listener, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("failed to bind to port %d: %w", port, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serverCancel = cancel

	serverReady := make(chan error, 1)

	go func() {
		defer close(serverReady)
		defer func() {
			atomic.StoreInt32(&serverRunning, 0)
			if listener != nil {
				listener.Close()
			}
		}()

		atomic.StoreInt32(&serverRunning, 1)

		state.Logger.Info("WebSocket server starting",
			slog.String("address", fmt.Sprintf("ws://127.0.0.1:%d", port)))

		wsHandler := websocket.Handler(func(ws *websocket.Conn) {
			if err := validateConnection(ws); err != nil {
				state.Logger.Warn("Connection validation failed",
					slog.String("reason", err.Error()),
					slog.String("remote_addr", ws.Request().RemoteAddr))
				ws.Close()
				return
			}
			HandleConnection(ws, state)
		})

		server := &http.Server{
			Handler:      wsHandler,
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 15 * time.Second,
			IdleTimeout:  60 * time.Second,
		}

		serverReady <- nil

		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			if ctx.Err() == nil {
				state.Logger.Error("WebSocket server error", slog.String("error", err.Error()))
			}
		}
	}()

	select {
	case err := <-serverReady:
		if err != nil {
			listener.Close()
			cancel()
			return fmt.Errorf("server failed to start: %w", err)
		}
	case <-time.After(100 * time.Millisecond):
	}

	return nil
}

func validateConnection(ws *websocket.Conn) error {
	if ws.Request() != nil {
		origin := ws.Request().Header.Get("Origin")
		if origin != "" && !isValidOrigin(origin) {
			return fmt.Errorf("invalid origin: %s", origin)
		}

		remoteAddr := ws.Request().RemoteAddr
		if !isLocalhost(remoteAddr) {
			return fmt.Errorf("non-localhost connection: %s", remoteAddr)
		}
	}

	return nil
}

func isLocalhost(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return host == "localhost"
	}

	return ip.IsLoopback()
}

func isValidOrigin(origin string) bool {
	return strings.HasPrefix(origin, "ws://localhost:") ||
		strings.HasPrefix(origin, "ws://127.0.0.1:") ||
		strings.HasPrefix(origin, "wss://localhost:") ||
		strings.HasPrefix(origin, "wss://127.0.0.1:") ||
		strings.HasPrefix(origin, "http://localhost:") ||
		strings.HasPrefix(origin, "http://127.0.0.1:") ||
		strings.HasPrefix(origin, "https://localhost:") ||
		strings.HasPrefix(origin, "https://127.0.0.1:")
}

func StopServer() {
	serverMux.Lock()
	defer serverMux.Unlock()

	if atomic.LoadInt32(&serverRunning) == 1 {
		atomic.StoreInt32(&serverRunning, 0)
		if serverCancel != nil {
			serverCancel()
		}
		if listener != nil {
			listener.Close()
		}
	}
}
