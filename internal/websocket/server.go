// Package websocket implements a WebSocket server for real-time communication.
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

const (
	// Server timeouts
	serverReadTimeout  = 60 * time.Second
	serverWriteTimeout = 60 * time.Second
	serverIdleTimeout  = 300 * time.Second // 5 minutes

	// Startup timeout
	serverStartTimeout = 100 * time.Millisecond
)

// ServiceState holds the current state of the teams-green service and manages WebSocket clients.
// It provides thread-safe access to service status and client connections.
type ServiceState struct {
	State            string                   // Current service state (e.g., "running", "stopped")
	PID              int                      // Process ID of the service
	Clients          map[*websocket.Conn]bool // Active WebSocket client connections
	Mutex            sync.RWMutex             // Protects concurrent access to state
	Logger           *slog.Logger             // Logger for service events
	LastActivity     time.Time                // Timestamp of last Teams activity
	TeamsWindowCount int                      // Number of detected Teams windows
	FailureStreak    int                      // Current failure streak count
}

var (
	listener      net.Listener
	serverMux     sync.Mutex
	serverRunning int32
	serverCancel  context.CancelFunc
	serverDone    chan struct{} // Channel to signal server goroutine completion
)

// StartServer starts a WebSocket server on the specified port with security restrictions.
// It only accepts connections from localhost and validates origins for security.
// The server runs in a separate goroutine and can be stopped with StopServer().
func StartServer(port int, state *ServiceState) error {
	serverMux.Lock()
	defer serverMux.Unlock()

	// Check if server is already running
	if err := checkServerRunning(); err != nil {
		return err
	}

	// Create and bind listener
	listener, err := createListener(port)
	if err != nil {
		return err
	}

	// Set up context for cancellation
	ctx, cancel := context.WithCancel(context.Background())
	serverCancel = cancel

	// Start server in background goroutine
	return startServerGoroutine(ctx, cancel, listener, port, state)
}

// checkServerRunning verifies that the server is not already running.
func checkServerRunning() error {
	if atomic.LoadInt32(&serverRunning) == 1 {
		return fmt.Errorf("websocket server is already running")
	}
	return nil
}

// createListener creates and returns a TCP listener on the specified port.
func createListener(port int) (net.Listener, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("failed to bind to port %d: %w", port, err)
	}
	return listener, nil
}

// startServerGoroutine starts the WebSocket server in a background goroutine.
func startServerGoroutine(ctx context.Context, cancel context.CancelFunc, listener net.Listener, port int, state *ServiceState) error {
	serverReady := make(chan error, 1)
	serverDone = make(chan struct{}) // Initialize done channel

	go func() {
		defer close(serverReady)
		defer cleanupServerResources()
		defer close(serverDone) // Signal completion

		atomic.StoreInt32(&serverRunning, 1)

		state.Logger.Info("WebSocket server starting",
			slog.String("address", fmt.Sprintf("ws://127.0.0.1:%d", port)))

		// Create WebSocket handler with connection validation
		wsHandler := createWebSocketHandler(state)

		// Create HTTP server with timeouts
		server := createHTTPServer(wsHandler)

		// Signal that server is ready
		serverReady <- nil

		// Start goroutine to handle context cancellation
		go func() {
			<-ctx.Done()
			// Force server shutdown when context is cancelled
			server.Close()
		}()

		// Serve requests until context is cancelled or error occurs
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			if ctx.Err() == nil {
				state.Logger.Error("WebSocket server error", slog.String("error", err.Error()))
			}
		}
	}()

	// Wait for server to be ready or timeout
	select {
	case err := <-serverReady:
		if err != nil {
			listener.Close()
			cancel()
			return fmt.Errorf("server failed to start: %w", err)
		}
	case <-time.After(serverStartTimeout):
		// Server started successfully within timeout
	}

	return nil
}

// cleanupServerResources cleans up server resources when the goroutine exits.
// This function is called from the server goroutine's defer and should not
// conflict with StopServer() which also cleans up resources.
func cleanupServerResources() {
	atomic.StoreInt32(&serverRunning, 0)
	// Note: listener is closed by StopServer() to avoid double-close
}

// createWebSocketHandler creates a WebSocket handler with connection validation.
func createWebSocketHandler(state *ServiceState) websocket.Handler {
	return websocket.Handler(func(ws *websocket.Conn) {
		if err := validateConnection(ws); err != nil {
			state.Logger.Warn("Connection validation failed",
				slog.String("reason", err.Error()),
				slog.String("remote_addr", ws.Request().RemoteAddr))
			ws.Close()
			return
		}
		HandleConnection(ws, state)
	})
}

// createHTTPServer creates an HTTP server with appropriate timeouts.
func createHTTPServer(handler websocket.Handler) *http.Server {
	return &http.Server{
		Handler:      handler,
		ReadTimeout:  serverReadTimeout,
		WriteTimeout: serverWriteTimeout,
		IdleTimeout:  serverIdleTimeout,
	}
}

// validateConnection performs security validation on incoming WebSocket connections.
// It checks origin headers and ensures connections are from localhost only.
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

// isLocalhost checks if the given address is a localhost address (127.0.0.1 or localhost).
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

// isValidOrigin validates that the Origin header is from a trusted localhost source.
// This prevents cross-site WebSocket hijacking attacks.
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

// StopServer gracefully stops the WebSocket server and closes all connections.
// It is safe to call multiple times and will not panic if the server is not running.
func StopServer() {
	serverMux.Lock()
	defer serverMux.Unlock()

	if atomic.LoadInt32(&serverRunning) == 1 {
		atomic.StoreInt32(&serverRunning, 0)
		if serverCancel != nil {
			serverCancel()
		}
		// Close listener if it exists
		if listener != nil {
			listener.Close()
			listener = nil // Clear the global reference
		}

		// Wait for server goroutine to complete
		if serverDone != nil {
			<-serverDone
			serverDone = nil
		}
	}
}
