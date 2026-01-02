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
	maxPanicRestarts    = 5             // Circuit breaker: max restarts per hour
	panicRestartWindow  = 1 * time.Hour // Time window for panic restart tracking
)

// Service manages the teams-green application lifecycle and activity monitoring.
type Service struct {
	config             *config.Config
	state              *websocket.ServiceState
	teamsMgr           *TeamsManager
	lastUserActivity   time.Time
	nextTeamsActivity  time.Time
	activityCheckMutex sync.RWMutex
	panicRestarts      []time.Time
	panicMutex         sync.Mutex
	mainLoopWG         sync.WaitGroup // Tracks main loop goroutine lifecycle
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

func newTeamsManager(cfg *config.Config) *TeamsManager {
	return &TeamsManager{
		now:                time.Now,
		config:             cfg,
		focusFailures:      make(map[win.HWND]focusFailInfo),
		lastEscalation:     make(map[win.HWND]time.Time),
		escalationCooldown: escalationCooldownDefault,
		windowCache:        &windowCacheWithAtomics{},
		cacheTTL:           windowCacheTTLDefault,
		enumContext: &WindowEnumContext{
			teamsExecutables: teamsExecutables,
		},
	}
}

// canRestartAfterPanic checks if we should restart after a panic (circuit breaker)
func (s *Service) canRestartAfterPanic() bool {
	s.panicMutex.Lock()
	defer s.panicMutex.Unlock()

	now := time.Now()
	windowStart := now.Add(-panicRestartWindow)

	// Filter to keep only restarts within the window
	var validRestarts []time.Time
	for _, t := range s.panicRestarts {
		if t.After(windowStart) {
			validRestarts = append(validRestarts, t)
		}
	}
	s.panicRestarts = validRestarts

	if len(s.panicRestarts) >= maxPanicRestarts {
		slog.Error("Circuit breaker: too many panic restarts, giving up",
			slog.Int("restarts", len(s.panicRestarts)),
			slog.Duration("window", panicRestartWindow))
		return false
	}

	s.panicRestarts = append(s.panicRestarts, now)
	return true
}

func NewService(cfg *config.Config) *Service {
	// Initialize global logger
	config.InitLogger(cfg)

	state := &websocket.ServiceState{
		State:            "stopped",
		PID:              os.Getpid(),
		Clients:          make(map[*ws.Conn]bool),
		LastActivity:     time.Now(),
		TeamsWindowCount: 0,
		FailureStreak:    0,
	}

	teamsMgr := newTeamsManager(cfg)

	now := time.Now()
	return &Service{
		config:            cfg,
		state:             state,
		teamsMgr:          teamsMgr,
		lastUserActivity:  now,
		nextTeamsActivity: now.Add(time.Duration(cfg.Interval) * time.Second),
		panicRestarts:     make([]time.Time, 0, maxPanicRestarts),
		mainLoopWG:        sync.WaitGroup{},
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
	slog.Info("Service state changed",
		"state", currentState,
		"pid", currentPID)
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

	// Register this goroutine in WaitGroup and ensure it's deregistered
	s.mainLoopWG.Add(1)
	defer s.mainLoopWG.Done()

	// Panic recovery for main loop
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Main loop panic recovered", slog.Any("error", r))
			// Don't restart if context is already done
			if ctx.Err() != nil {
				return
			}
			// Circuit breaker: limit restarts to prevent infinite loop
			if !s.canRestartAfterPanic() {
				slog.Error("Giving up after too many panic restarts")
				return
			}
			// Attempt to restart the loop after a delay only in production
			time.Sleep(panicRestartDelay)
			if ctx.Err() == nil {
				slog.Info("Attempting to restart main loop after panic")
				// Register new goroutine before spawning
				s.mainLoopWG.Add(1)
				go func() {
					defer s.mainLoopWG.Done()
					s.MainLoop(ctx, loopTime)
				}()
			}
		}
	}()

	slog.Info("Main loop started",
		slog.Duration("interval", loopTime))

	for {
		select {
		case <-ctx.Done():
			slog.Info("Main loop stopping")
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
				slog.Info("Main loop stopping due to context cancellation")
				return
			}

			// Log time until next Teams activity in MM:SS format
			s.activityCheckMutex.RLock()
			timeUntilNext := time.Until(s.nextTeamsActivity)
			s.activityCheckMutex.RUnlock()

			if timeUntilNext > 0 {
				minutes := int(timeUntilNext.Minutes())
				seconds := int(timeUntilNext.Seconds()) % 60
				slog.Debug("Next Teams activity scheduled",
					slog.Int("minutes", minutes),
					slog.Int("seconds", seconds))
			} else {
				slog.Debug("Teams activity ready")
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
			slog.Error("Teams activity panic recovered", slog.Any("error", r))
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
	inputDetector := NewInputDetectorWithThreshold(inputThreshold)

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
			slog.Info("Teams activity resumed",
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
		s.state.Mutex.Lock()
		failureStreak := s.state.FailureStreak
		s.state.Mutex.Unlock()

		slog.Warn("⚠️Teams activity failed",
			slog.String("error", err.Error()),
			slog.Int("missing_count", teamsMissingCount),
			slog.Int("failure_streak", failureStreak))
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
		slog.Info("Applying failure backoff", slog.Duration("duration", backoffDuration))

		// Use context-aware sleep
		select {
		case <-ctx.Done():
			slog.Info("Main loop stopping during backoff")
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
			slog.Error("Health check panic recovered", slog.Any("error", r))
		}
	}()

	slog.Debug("Performing health check")

	// Check memory usage (basic check)
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	if memStats.Alloc > memoryThreshold { // 100MB threshold
		slog.Warn("High memory usage detected",
			slog.Uint64("alloc_mb", memStats.Alloc/1024/1024))
		runtime.GC() // Force garbage collection
	}

	// Check goroutine count
	goroutineCount := runtime.NumGoroutine()
	if goroutineCount > goroutineThreshold {
		slog.Warn("High goroutine count", slog.Int("count", goroutineCount))
	}

	// Check WebSocket connections if enabled
	if s.config.WebSocket {
		s.state.Mutex.RLock()
		clientCount := len(s.state.Clients)
		s.state.Mutex.RUnlock()

		slog.Debug("Health check completed",
			slog.Uint64("memory_mb", memStats.Alloc/1024/1024),
			slog.Int("goroutines", goroutineCount),
			slog.Int("websocket_clients", clientCount))
	}
}

func (s *Service) Run() error {
	// Setup graceful shutdown with timeout
	const shutdownTimeout = 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	// Start WebSocket server if enabled
	if s.config.WebSocket {
		server := websocket.NewServer(s.config.Port, s.state, s.config)
		if err := server.Start(); err != nil {
			return fmt.Errorf("failed to start WebSocket server: %w", err)
		}

		// Graceful server shutdown
		go func() {
			<-ctx.Done()
			server.Stop()
		}()
	}

	s.SetState("running", s.config.WebSocket)

	// Start main loop
	go s.MainLoop(ctx, time.Duration(s.config.Interval)*time.Second)

	// Wait for shutdown signal
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	<-sigs

	slog.Info("Shutting down service")
	cancel()
	s.SetState("stopped", s.config.WebSocket)

	// Wait for main loop goroutines to finish
	s.mainLoopWG.Wait()

	// Properly close log files
	config.CloseLogFile()

	time.Sleep(shutdownSleep)
	return nil
}
