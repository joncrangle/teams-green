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

type TeamsWindow struct {
	HWND           win.HWND
	ProcessID      uint32
	ExecutablePath string
	WindowTitle    string
	LastSeen       time.Time
	IsValid        bool
}

type WindowCache struct {
	Windows              []TeamsWindow
	LastUpdate           time.Time
	Mutex                sync.RWMutex
	CacheDuration        time.Duration
	LastFullScan         time.Time
	QuickScanMode        bool
	FailureCount         int
	LastFailure          time.Time
	AdaptiveScanInterval time.Duration
	SystemBootTime       time.Time         // Track system boot time to detect hibernation/sleep
	LastCacheCheck       time.Time         // Track when cache was last validated
	BloomFilter          []uint64          // Simple bloom filter for PID tracking
	BloomSize            int               // Size of bloom filter
	ProcessHashes        map[uint32]uint64 // Process ID -> hash mapping
}

type RetryState struct {
	FailureCount        int
	LastFailure         time.Time
	BackoffSeconds      int
	MaxBackoff          int
	TeamsRestartCount   int
	LastTeamsRestart    time.Time
	ConsecutiveFailures int
	CircuitBreakerOpen  bool
	CircuitOpenTime     time.Time
}

type Service struct {
	logger             *slog.Logger
	config             *config.Config
	state              *websocket.ServiceState
	teamsMgr           *TeamsManager
	lastUserActivity   time.Time
	nextTeamsActivity  time.Time
	activityCheckMutex sync.RWMutex
}

type TeamsManager struct {
	windowCache     *WindowCache
	retryState      *RetryState
	logger          *slog.Logger
	enumContext     *WindowEnumContext
	config          *config.Config
	lastInputCheck  time.Time
	inputCheckMutex sync.RWMutex
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
			Windows:              make([]TeamsWindow, 0),
			CacheDuration:        30 * time.Second,
			AdaptiveScanInterval: 5 * time.Minute,
			SystemBootTime:       time.Now(), // Initialize with current time
			LastCacheCheck:       time.Now(),
			BloomSize:            256,                // 2KB bloom filter
			BloomFilter:          make([]uint64, 32), // 256 bits / 8 bits per uint64 = 32
			ProcessHashes:        make(map[uint32]uint64),
		},
		enumContext: &WindowEnumContext{
			teamsExecutables: getTeamsExecutables(),
			logger:           logger,
		},
		retryState: &RetryState{
			MaxBackoff: 300, // 5 minutes max
		},
		logger: logger,
		config: cfg,
	}

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

	teamsMissingCount := 0
	const maxMissingLogs = 3

	// Health check tracking
	var lastHealthCheck time.Time
	const healthCheckInterval = 5 * time.Minute

	// Panic recovery for main loop
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("Main loop panic recovered", slog.Any("error", r))
			// Don't restart if context is already done
			if ctx.Err() != nil {
				return
			}
			// Attempt to restart the loop after a delay only in production
			time.Sleep(10 * time.Second)
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
		case <-ticker.C:
			// Check context again before processing
			if ctx.Err() != nil {
				s.logger.Info("Main loop stopping due to context cancellation")
				return
			}

			s.logger.Debug("Main loop tick")

			// Periodic health check
			now := time.Now()
			if now.Sub(lastHealthCheck) >= healthCheckInterval {
				s.performHealthCheck()
				lastHealthCheck = now
			}

			// Check for user activity (throttled to avoid excessive API calls)
			if s.checkUserActivity() {
				s.logger.Debug("User activity detected, resetting Teams activity timer")
				continue
			}

			// Only attempt Teams activity if interval has elapsed
			if !s.shouldSendTeamsActivity() {
				continue
			}

			// Attempt Teams activity with error recovery
			if err := s.safeTeamsActivity(ctx); err != nil {
				// Check if this is user input deferral (not an actual failure)
				if !errors.Is(err, ErrUserInputActive) {
					// This is an actual failure
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

					// Implement exponential backoff for excessive failures, but respect context
					if s.state.FailureStreak > 10 {
						backoffDuration := time.Duration(min(s.state.FailureStreak*2, 60)) * time.Second
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
				// If it's user input deferral, we just skip the success handling and continue the loop
			} else {
				// Success - update activity tracking and reset timer
				s.resetTeamsActivityTimer()
				s.state.Mutex.Lock()
				s.state.LastActivity = time.Now()
				// Avoid duplicate window enumeration - if SendKeysToTeams succeeded, at least 1 window exists
				s.state.TeamsWindowCount = 1
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

func (s *Service) checkUserActivity() bool {
	// Check for active input without throttling to ensure accurate detection
	if s.teamsMgr.isUserInputActive() {
		now := time.Now()
		s.activityCheckMutex.Lock()
		s.lastUserActivity = now
		s.nextTeamsActivity = now.Add(time.Duration(s.config.Interval) * time.Second)
		s.activityCheckMutex.Unlock()
		return true
	}

	return false
}

func (s *Service) shouldSendTeamsActivity() bool {
	s.activityCheckMutex.RLock()
	defer s.activityCheckMutex.RUnlock()
	return time.Now().After(s.nextTeamsActivity)
}

func (s *Service) resetTeamsActivityTimer() {
	s.activityCheckMutex.Lock()
	s.nextTeamsActivity = time.Now().Add(time.Duration(s.config.Interval) * time.Second)
	s.activityCheckMutex.Unlock()
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

	if memStats.Alloc > 100*1024*1024 { // 100MB threshold
		s.logger.Warn("High memory usage detected",
			slog.Uint64("alloc_mb", memStats.Alloc/1024/1024))
		runtime.GC() // Force garbage collection
	}

	// Check goroutine count
	goroutineCount := runtime.NumGoroutine()
	if goroutineCount > 50 {
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

	// Properly close log files
	config.CloseLogFile()

	time.Sleep(500 * time.Millisecond)
	return nil
}
