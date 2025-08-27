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

var serviceState = &websocket.ServiceState{
	State:            "stopped",
	PID:              os.Getpid(),
	Clients:          make(map[*ws.Conn]bool),
	LastActivity:     time.Now(),
	TeamsWindowCount: 0,
	FailureStreak:    0,
}

func SetState(newState string, emit bool) {
	serviceState.Mutex.Lock()
	serviceState.State = newState
	serviceState.Mutex.Unlock()

	if emit {
		websocket.Broadcast(&websocket.Event{
			Service: "teams-green",
			Status:  serviceState.State,
			PID:     serviceState.PID,
			Message: fmt.Sprintf("State changed to %s", serviceState.State),
		}, serviceState)
	}
	serviceState.Logger.Info("Service state changed",
		slog.String("state", serviceState.State),
		slog.Int("pid", serviceState.PID))
}

func MainLoop(ctx context.Context, loopTime time.Duration, wsEnabled bool) {
	ticker := time.NewTicker(loopTime)
	defer ticker.Stop()

	teamsMissingCount := 0
	const maxMissingLogs = 3

	serviceState.Logger.Info("Main loop started",
		slog.Duration("interval", loopTime))

	for {
		select {
		case <-ticker.C:
			if err := SendKeysToTeams(); err != nil {
				teamsMissingCount++
				serviceState.Mutex.Lock()
				serviceState.FailureStreak++
				serviceState.Mutex.Unlock()

				if teamsMissingCount <= maxMissingLogs {
					serviceState.Logger.Warn("Teams activity failed",
						slog.String("error", err.Error()),
						slog.Int("missing_count", teamsMissingCount),
						slog.Int("failure_streak", serviceState.FailureStreak))
					if wsEnabled {
						websocket.Broadcast(&websocket.Event{
							Service: "teams-green",
							Status:  "warning",
							PID:     serviceState.PID,
							Message: err.Error(),
						}, serviceState)
					}
				}
			} else {
				// Success - update activity tracking
				serviceState.Mutex.Lock()
				serviceState.LastActivity = time.Now()
				serviceState.TeamsWindowCount = len(FindTeamsWindows())
				if serviceState.FailureStreak > 0 {
					serviceState.Logger.Info("Teams activity resumed",
						slog.Int("was_failure_streak", serviceState.FailureStreak),
						slog.Int("teams_windows", serviceState.TeamsWindowCount))
					if wsEnabled {
						websocket.Broadcast(&websocket.Event{
							Service: "teams-green",
							Status:  "running",
							PID:     serviceState.PID,
							Message: fmt.Sprintf("Teams activity resumed (found %d windows)", serviceState.TeamsWindowCount),
						}, serviceState)
					}
				}
				serviceState.FailureStreak = 0
				serviceState.Mutex.Unlock()
				teamsMissingCount = 0
			}
		case <-ctx.Done():
			serviceState.Logger.Info("Main loop stopping")
			return
		}
	}
}

func Run(cfg *config.Config) error {
	serviceState.Logger = config.InitLogger(cfg)

	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start WebSocket server if enabled
	if cfg.WebSocket {
		if err := websocket.StartServer(cfg.Port, serviceState); err != nil {
			return fmt.Errorf("failed to start WebSocket server: %v", err)
		}

		// Graceful server shutdown
		go func() {
			<-ctx.Done()
			websocket.StopServer()
		}()
	}

	SetState("running", cfg.WebSocket)

	// Start main loop
	go MainLoop(ctx, time.Duration(cfg.Interval)*time.Second, cfg.WebSocket)

	// Wait for shutdown signal
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	<-sigs

	serviceState.Logger.Info("Shutting down service")
	cancel()
	SetState("stopped", cfg.WebSocket)
	time.Sleep(500 * time.Millisecond)
	return nil
}
