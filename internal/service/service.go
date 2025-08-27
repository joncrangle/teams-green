package service

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joncrangle/teams-green/internal/config"
	"github.com/joncrangle/teams-green/internal/websocket"
	ws "golang.org/x/net/websocket"
)

type Service struct {
	logger   *slog.Logger
	config   *config.Config
	state    *websocket.ServiceState
	teamsMgr *TeamsManager
}

type TeamsManager struct {
	windowCache *WindowCache
	retryState  *RetryState
	logger      *slog.Logger
}

func NewService(cfg *config.Config) *Service {
	logger := config.InitLogger(cfg)

	state := &websocket.ServiceState{
		State:            "stopped",
		PID:              os.Getpid(),
		Clients:          make(map[*ws.Conn]bool),
		LastActivity:     time.Now(),
		TeamsWindowCount: 0,
		FailureStreak:    0,
		Logger:           logger,
	}

	teamsMgr := &TeamsManager{
		windowCache: &WindowCache{
			Windows:       make([]TeamsWindow, 0),
			CacheDuration: 30 * time.Second,
		},
		retryState: &RetryState{
			MaxBackoff: 300, // 5 minutes max
		},
		logger: logger,
	}

	return &Service{
		logger:   logger,
		config:   cfg,
		state:    state,
		teamsMgr: teamsMgr,
	}
}

func (s *Service) SetState(newState string, emit bool) {
	s.state.Mutex.Lock()
	s.state.State = newState
	currentPID := s.state.PID
	currentState := s.state.State
	s.state.Mutex.Unlock()

	if emit && s.config.WebSocket {
		websocket.Broadcast(&websocket.Event{
			Service: "teams-green",
			Status:  currentState,
			PID:     currentPID,
			Message: fmt.Sprintf("State changed to %s", currentState),
		}, s.state)
	}
	s.logger.Info("Service state changed",
		slog.String("state", currentState),
		slog.Int("pid", currentPID))
}

func (s *Service) MainLoop(ctx context.Context, loopTime time.Duration) {
	ticker := time.NewTicker(loopTime)
	defer ticker.Stop()

	teamsMissingCount := 0
	const maxMissingLogs = 3

	s.logger.Info("Main loop started",
		slog.Duration("interval", loopTime))

	for {
		select {
		case <-ticker.C:
			if err := s.teamsMgr.SendKeysToTeams(s.state); err != nil {
				teamsMissingCount++
				s.state.Mutex.Lock()
				s.state.FailureStreak++
				s.state.Mutex.Unlock()

				if teamsMissingCount <= maxMissingLogs {
					s.logger.Warn("Teams activity failed",
						slog.String("error", err.Error()),
						slog.Int("missing_count", teamsMissingCount),
						slog.Int("failure_streak", s.state.FailureStreak))
					if s.config.WebSocket {
						websocket.Broadcast(&websocket.Event{
							Service: "teams-green",
							Status:  "warning",
							PID:     s.state.PID,
							Message: err.Error(),
						}, s.state)
					}
				}
			} else {
				// Success - update activity tracking
				s.state.Mutex.Lock()
				s.state.LastActivity = time.Now()
				s.state.TeamsWindowCount = len(s.teamsMgr.FindTeamsWindows())
				if s.state.FailureStreak > 0 {
					s.logger.Info("Teams activity resumed",
						slog.Int("was_failure_streak", s.state.FailureStreak),
						slog.Int("teams_windows", s.state.TeamsWindowCount))
					if s.config.WebSocket {
						websocket.Broadcast(&websocket.Event{
							Service: "teams-green",
							Status:  "running",
							PID:     s.state.PID,
							Message: fmt.Sprintf("Teams activity resumed (found %d windows)", s.state.TeamsWindowCount),
						}, s.state)
					}
				}
				s.state.FailureStreak = 0
				s.state.Mutex.Unlock()
				teamsMissingCount = 0
			}
		case <-ctx.Done():
			s.logger.Info("Main loop stopping")
			return
		}
	}
}

func (s *Service) Run() error {
	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start WebSocket server if enabled
	if s.config.WebSocket {
		if err := websocket.StartServer(s.config.Port, s.state); err != nil {
			return fmt.Errorf("failed to start WebSocket server: %v", err)
		}

		// Graceful server shutdown
		go func() {
			<-ctx.Done()
			websocket.StopServer()
		}()
	}

	s.SetState("running", s.config.WebSocket)

	// Start main loop
	go s.MainLoop(ctx, time.Duration(s.config.Interval)*time.Second)

	// Wait for shutdown signal
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	<-sigs

	s.logger.Info("Shutting down service")
	cancel()
	s.SetState("stopped", s.config.WebSocket)
	time.Sleep(500 * time.Millisecond)
	return nil
}
