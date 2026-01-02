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
	LastActivity     time.Time                // Timestamp of last Teams activity
	TeamsWindowCount int                      // Number of detected Teams windows
	FailureStreak    int                      // Current failure streak count
}

// Server manages the WebSocket server lifecycle and resources.
type Server struct {
	listener net.Listener
	mux      sync.Mutex
	running  int32
	cancel   context.CancelFunc
	done     chan struct{}
	port     int
	state    *ServiceState
	config   ConfigProvider
}

// ConfigProvider defines the interface for accessing configuration values
// needed by the WebSocket server and related components.
type ConfigProvider interface {
	GetFocusDelay() time.Duration
	GetRestoreDelay() time.Duration
	GetKeyProcessDelay() time.Duration
	GetInputThreshold() time.Duration
	IsDebugEnabled() bool
	GetActivityMode() string
	GetWebSocketReadTimeout() time.Duration
	GetWebSocketWriteTimeout() time.Duration
	GetWebSocketIdleTimeout() time.Duration
}

// NewServer creates a new WebSocket server instance.
func NewServer(port int, state *ServiceState, config ConfigProvider) *Server {
	return &Server{
		port:   port,
		state:  state,
		config: config,
		done:   make(chan struct{}),
	}
}

// Start starts the WebSocket server on the configured port.
func (s *Server) Start() error {
	s.mux.Lock()
	defer s.mux.Unlock()

	// Check if server is already running
	if err := s.checkServerRunning(); err != nil {
		return err
	}

	// Create and bind listener
	listener, err := s.createListener()
	if err != nil {
		return err
	}
	s.listener = listener

	// Set up context for cancellation
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel

	// Start server in background goroutine
	return s.startServerGoroutine(ctx, cancel)
}

// Stop gracefully stops the WebSocket server and closes all connections.
// It is safe to call multiple times and will not panic if the server is not running.
func (s *Server) Stop() {
	s.mux.Lock()
	defer s.mux.Unlock()

	if atomic.LoadInt32(&s.running) == 1 {
		atomic.StoreInt32(&s.running, 0)
		if s.cancel != nil {
			s.cancel()
		}
		// Close listener if it exists
		if s.listener != nil {
			s.listener.Close()
			s.listener = nil
		}

		// Wait for server goroutine to complete
		if s.done != nil {
			<-s.done
			s.done = nil
		}
	}
}

// checkServerRunning verifies that the server is not already running.
func (s *Server) checkServerRunning() error {
	if atomic.LoadInt32(&s.running) == 1 {
		return fmt.Errorf("❌ websocket server is already running")
	}
	return nil
}

// createListener creates and returns a TCP listener on the configured port.
func (s *Server) createListener() (net.Listener, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
	if err != nil {
		return nil, fmt.Errorf("❌ failed to bind to port %d: %w", s.port, err)
	}
	return listener, nil
}

// startServerGoroutine starts the WebSocket server in a background goroutine.
func (s *Server) startServerGoroutine(ctx context.Context, cancel context.CancelFunc) error {
	serverReady := make(chan error, 1)

	go func() {
		defer close(serverReady)
		defer s.cleanupServerResources()
		defer close(s.done) // Signal completion

		atomic.StoreInt32(&s.running, 1)

		slog.Info("🚀 WebSocket server starting",
			slog.String("address", fmt.Sprintf("ws://127.0.0.1:%d", s.port)))

		// Create WebSocket handler with connection validation
		wsHandler := s.createWebSocketHandler()

		// Create HTTP server with timeouts
		server := s.createHTTPServer(wsHandler)

		// Signal that server is ready
		serverReady <- nil

		// Start goroutine to handle context cancellation
		go func() {
			<-ctx.Done()
			// Force server shutdown when context is cancelled
			server.Close()
		}()

		// Serve requests until context is cancelled or error occurs
		if err := server.Serve(s.listener); err != nil && err != http.ErrServerClosed {
			if ctx.Err() == nil {
				slog.Error("❌ WebSocket server error", slog.String("error", err.Error()))
			}
		}
	}()

	// Wait for server to be ready or timeout
	select {
	case err := <-serverReady:
		if err != nil {
			if s.listener != nil {
				s.listener.Close()
			}
			cancel()
			return fmt.Errorf("❌ server failed to start: %w", err)
		}
	case <-time.After(serverStartTimeout):
		// Server started successfully within timeout
	}

	return nil
}

// cleanupServerResources cleans up server resources when the goroutine exits.
func (s *Server) cleanupServerResources() {
	atomic.StoreInt32(&s.running, 0)
}

// createWebSocketHandler creates a WebSocket handler with connection validation.
func (s *Server) createWebSocketHandler() websocket.Handler {
	return websocket.Handler(func(ws *websocket.Conn) {
		if err := s.validateConnection(ws); err != nil {
			slog.Warn("Connection validation failed",
				slog.String("reason", err.Error()),
				slog.String("remote_addr", ws.Request().RemoteAddr))
			ws.Close()
			return
		}
		HandleConnection(ws, s.state, s.config)
	})
}

// createHTTPServer creates an HTTP server with appropriate timeouts.
func (s *Server) createHTTPServer(handler websocket.Handler) *http.Server {
	return &http.Server{
		Handler:      handler,
		ReadTimeout:  s.config.GetWebSocketReadTimeout(),
		WriteTimeout: s.config.GetWebSocketWriteTimeout(),
		IdleTimeout:  s.config.GetWebSocketIdleTimeout(),
	}
}

// validateConnection performs security validation on incoming WebSocket connections.
func (s *Server) validateConnection(ws *websocket.Conn) error {
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

// defaultConfigProvider provides default values for WebSocket timeouts
type defaultConfigProvider struct{}

func (d *defaultConfigProvider) GetFocusDelay() time.Duration {
	return 10 * time.Millisecond
}

func (d *defaultConfigProvider) GetRestoreDelay() time.Duration {
	return 10 * time.Millisecond
}

func (d *defaultConfigProvider) GetKeyProcessDelay() time.Duration {
	return 75 * time.Millisecond
}

func (d *defaultConfigProvider) GetInputThreshold() time.Duration {
	return 500 * time.Millisecond
}

func (d *defaultConfigProvider) IsDebugEnabled() bool {
	return false
}

func (d *defaultConfigProvider) GetActivityMode() string {
	return "focus"
}

func (d *defaultConfigProvider) GetWebSocketReadTimeout() time.Duration {
	return 60 * time.Second
}

func (d *defaultConfigProvider) GetWebSocketWriteTimeout() time.Duration {
	return 60 * time.Second
}

func (d *defaultConfigProvider) GetWebSocketIdleTimeout() time.Duration {
	return 300 * time.Second
}
