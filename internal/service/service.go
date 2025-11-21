// Package service implements the main functionality of the teams-green application.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/joncrangle/teams-green/internal/config"
	"github.com/joncrangle/teams-green/internal/websocket"
	"github.com/lxn/win"
	ws "golang.org/x/net/websocket"
)

const (
	healthCheckInterval = 5 * time.Minute
	memoryThreshold     = 100 * 1024 * 1024 // 100MB
	goroutineThreshold  = 50
	maxMissingLogs      = 3
	failureBackoffStart = 10
	maxBackoffSeconds   = 60
	panicRestartDelay   = 10 * time.Second
	shutdownSleep       = 500 * time.Millisecond
)

// Service manages the teams-green application lifecycle and activity monitoring.
type Service struct {
	logger             *slog.Logger
	config             *config.Config
	state              *websocket.ServiceState
	teamsMgr           *TeamsManager
	lastUserActivity   time.Time
	nextTeamsActivity  time.Time
	activityCheckMutex sync.RWMutex
}

// updateActivityState atomically updates service scheduling timestamps and shared state following
// a successful Teams activity cycle. This reduces lock interleaving between state.Mutex and
// activityCheckMutex and ensures timing and observable state advance together.
func (s *Service) updateActivityState(activityTime time.Time) {
	// Lock order: activityCheckMutex then state.Mutex to avoid potential deadlocks.
	s.activityCheckMutex.Lock()
	s.state.Mutex.Lock()

	s.state.LastActivity = activityTime
	// SendKeysToTeams success implies at least one Teams window; set to 1 (actual enumeration would be redundant)
	s.state.TeamsWindowCount = 1
	s.state.FailureStreak = 0
	s.nextTeamsActivity = activityTime.Add(time.Duration(s.config.Interval) * time.Second)

	s.state.Mutex.Unlock()
	s.activityCheckMutex.Unlock()
}

func newTeamsManager(logger *slog.Logger, cfg *config.Config) *TeamsManager {
	return &TeamsManager{
		now:                time.Now,
		logger:             logger,
		config:             cfg,
		focusFailures:      make(map[win.HWND]focusFailInfo),
		lastEscalation:     make(map[win.HWND]time.Time),
		escalationCooldown: escalationCooldownDefault,
		windowCache: &windowCache{
			handles:   make([]win.HWND, 0),
			timestamp: time.Time{}, // Zero time forces initial cache miss
		},
		cacheTTL: windowCacheTTLDefault,
		enumContext: &WindowEnumContext{
			teamsExecutables: teamsExecutables,
			logger:           logger,
			mutex:            &sync.Mutex{},
		},
	}
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

	teamsMgr := newTeamsManager(logger, cfg)

	now := time.Now()
	return &Service{
		logger:            logger,
		config:            cfg,
		state:             state,
		teamsMgr:          teamsMgr,
		lastUserActivity:  now,
		nextTeamsActivity: now.Add(time.Duration(cfg.Interval) * time.Second),
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

	// Separate ticker for user activity (check every 500ms for responsive detection)
	activityTicker := time.NewTicker(500 * time.Millisecond)
	defer activityTicker.Stop()

	teamsMissingCount := 0

	// Health check tracking
	var lastHealthCheck time.Time

	// Panic recovery for main loop
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("Main loop panic recovered", slog.Any("error", r))
			// Don't restart if context is already done
			if ctx.Err() != nil {
				return
			}
			// Attempt to restart the loop after a delay only in production
			time.Sleep(panicRestartDelay)
			if ctx.Err() == nil {
				s.logger.Info("Attempting to restart main loop after panic")
				go s.MainLoop(ctx, loopTime)
			}
		}
	}()

	s.logger.Info("Main loop started",
		slog.Duration("interval", loopTime))

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("Main loop stopping")
			return
		case <-activityTicker.C:
			// Check context again before processing
			if ctx.Err() != nil {
				return
			}
			// Quick user activity check only
			s.handleUserActivity()
		case <-ticker.C:
			// Check context again before processing
			if ctx.Err() != nil {
				s.logger.Info("Main loop stopping due to context cancellation")
				return
			}

			// Log time until next Teams activity in MM:SS format
			s.activityCheckMutex.RLock()
			timeUntilNext := time.Until(s.nextTeamsActivity)
			s.activityCheckMutex.RUnlock()

			if timeUntilNext > 0 {
				minutes := int(timeUntilNext.Minutes())
				seconds := int(timeUntilNext.Seconds()) % 60
				s.logger.Debug(fmt.Sprintf("Next Teams activity in %02d:%02d", minutes, seconds))
			} else {
				s.logger.Debug("Teams activity ready")
			}

			// Periodic health check
			now := time.Now()
			s.handleHealthCheck(now, &lastHealthCheck)

			// Handle Teams activity (user activity already checked by activityTicker)
			if err := s.handleTeamsActivity(ctx, &teamsMissingCount); err != nil {
				s.handleFailure(ctx, err, teamsMissingCount)
			}
		}
	}
}

func (s *Service) safeTeamsActivity(ctx context.Context) (err error) {
	// Check context before attempting Teams activity
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Panic recovery for Teams activity
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("Teams activity panic recovered", slog.Any("error", r))
			err = fmt.Errorf("teams activity panic: %v", r)
		}
	}()

	// For tests with very short timeouts, don't perform Teams activity
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return s.teamsMgr.SendKeysToTeams(ctx, s.state)
	}
}

func (s *Service) handleHealthCheck(now time.Time, lastHealthCheck *time.Time) {
	if now.Sub(*lastHealthCheck) >= healthCheckInterval {
		s.performHealthCheck()
		*lastHealthCheck = now
	}
}

func (s *Service) handleUserActivity() bool {
	// Create input detector with configured threshold
	inputThreshold := s.config.GetInputThreshold()
	inputDetector := NewInputDetectorWithThreshold(s.logger, inputThreshold)

	// Check for active input - detects keyboard, mouse, touch
	if inputDetector.IsUserInputActive() {
		now := time.Now()
		s.activityCheckMutex.Lock()
		s.lastUserActivity = now
		s.nextTeamsActivity = now.Add(time.Duration(s.config.Interval) * time.Second)
		s.activityCheckMutex.Unlock()
		return true
	}
	return false
}

func (s *Service) handleTeamsActivity(ctx context.Context, teamsMissingCount *int) error {
	// Only attempt Teams activity if interval has elapsed
	if !s.shouldSendTeamsActivity() {
		return nil
	}

	// Attempt Teams activity with error recovery
	if err := s.safeTeamsActivity(ctx); err != nil {
		// Check if this is user input deferral (not an actual failure)
		if !errors.Is(err, ErrUserInputActive) {
			// This is an actual failure
			*teamsMissingCount++
			s.state.Mutex.Lock()
			s.state.FailureStreak++
			s.state.Mutex.Unlock()

			return err
		}
		// User input detected during Teams activity - don't reset timer here
		// Timer will be reset by handleUserActivity() on next iteration
	} else {
		// Success - update activity tracking and advance next scheduled activity
		activityTime := time.Now()
		// If there was a failure streak, log and broadcast before resetting to preserve prior value
		if func() int { s.state.Mutex.RLock(); defer s.state.Mutex.RUnlock(); return s.state.FailureStreak }() > 0 {
			s.state.Mutex.RLock()
			prevStreak := s.state.FailureStreak
			windowCount := s.state.TeamsWindowCount
			s.state.Mutex.RUnlock()
			s.logger.Info("Teams activity resumed",
				slog.Int("was_failure_streak", prevStreak),
				slog.Int("teams_windows", windowCount))
			if s.config.WebSocket {
				websocket.Broadcast(&websocket.Event{
					Service: "teams-green",
					Status:  "running",
					PID:     s.state.PID,
					Message: fmt.Sprintf("Teams activity resumed (found %d windows)", windowCount),
				}, s.state)
			}
		}
		// Atomic state + schedule update
		s.updateActivityState(activityTime)
		*teamsMissingCount = 0
	}
	return nil
}

func (s *Service) handleFailure(ctx context.Context, err error, teamsMissingCount int) {
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

	// Implement exponential backoff for excessive failures, but respect context
	if s.state.FailureStreak > failureBackoffStart {
		backoffDuration := time.Duration(min(s.state.FailureStreak*2, maxBackoffSeconds)) * time.Second
		s.logger.Info("Applying failure backoff", slog.Duration("duration", backoffDuration))

		// Use context-aware sleep
		select {
		case <-ctx.Done():
			s.logger.Info("Main loop stopping during backoff")
			return
		case <-time.After(backoffDuration):
			// Continue with backoff
		}
	}
}

func (s *Service) shouldSendTeamsActivity() bool {
	s.activityCheckMutex.RLock()
	defer s.activityCheckMutex.RUnlock()
	return time.Now().After(s.nextTeamsActivity)
}

func (s *Service) performHealthCheck() {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("Health check panic recovered", slog.Any("error", r))
		}
	}()

	s.logger.Debug("Performing health check")

	// Check memory usage (basic check)
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	if memStats.Alloc > memoryThreshold { // 100MB threshold
		s.logger.Warn("High memory usage detected",
			slog.Uint64("alloc_mb", memStats.Alloc/1024/1024))
		runtime.GC() // Force garbage collection
	}

	// Check goroutine count
	goroutineCount := runtime.NumGoroutine()
	if goroutineCount > goroutineThreshold {
		s.logger.Warn("High goroutine count", slog.Int("count", goroutineCount))
	}

	// Check WebSocket connections if enabled
	if s.config.WebSocket {
		s.state.Mutex.RLock()
		clientCount := len(s.state.Clients)
		s.state.Mutex.RUnlock()

		s.logger.Debug("Health check completed",
			slog.Uint64("memory_mb", memStats.Alloc/1024/1024),
			slog.Int("goroutines", goroutineCount),
			slog.Int("websocket_clients", clientCount))
	}
}

func (s *Service) Run() error {
	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start WebSocket server if enabled
	if s.config.WebSocket {
		if err := websocket.StartServer(s.config.Port, s.state); err != nil {
			return fmt.Errorf("failed to start WebSocket server: %w", err)
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

	// Properly close log files
	config.CloseLogFile()

	time.Sleep(shutdownSleep)
	return nil
}
