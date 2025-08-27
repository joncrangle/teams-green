package service

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joncrangle/teams-green/internal/config"

	"golang.org/x/sys/windows"
)

func IsProcessRunning(pid int) bool {
	// Use Windows API to check if process is running
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() {
		_ = windows.CloseHandle(handle)
	}()

	var exitCode uint32
	err = windows.GetExitCodeProcess(handle, &exitCode)
	if err != nil {
		return false
	}

	// STILL_ACTIVE is 259 on Windows
	return exitCode == 259
}

func Start(cfg *config.Config) error {
	// Check if service is already running
	if pidBytes, err := os.ReadFile(config.PidFile); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes))); err == nil {
			if IsProcessRunning(pid) {
				return fmt.Errorf("service already running (PID %d)", pid)
			}
			// Clean up stale PID file
			os.Remove(config.PidFile)
		}
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %v", err)
	}

	args := []string{"run", fmt.Sprintf("--interval=%d", cfg.Interval)}
	if cfg.WebSocket {
		args = append(args, "--websocket", fmt.Sprintf("--port=%d", cfg.Port))
	}
	if cfg.Debug {
		args = append(args, "--debug")
	}
	if cfg.LogFile != "" {
		args = append(args, fmt.Sprintf("--log-file=%s", cfg.LogFile))
	}
	if cfg.LogFormat != "" {
		args = append(args, fmt.Sprintf("--log-format=%s", cfg.LogFormat))
	}
	if cfg.LogRotate {
		args = append(args, "--log-rotate")
	}

	if cfg.Debug {
		// Run in foreground for debugging
		cmd := exec.Command(exe, args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	// Run in background
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP,
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start service: %v", err)
	}

	pidContent := fmt.Sprintf("%d", cmd.Process.Pid)
	if err := os.WriteFile(config.PidFile, []byte(pidContent), 0o644); err != nil {
		if killErr := cmd.Process.Kill(); killErr != nil {
			return fmt.Errorf("service started but failed to write PID file (%v) and failed to cleanup process (%v)", err, killErr)
		}
		return fmt.Errorf("service started but failed to write PID file: %v", err)
	}

	fmt.Printf("🚀 Service started in background (PID %d)\n", cmd.Process.Pid)
	return nil
}

func Stop() error {
	pidBytes, err := os.ReadFile(config.PidFile)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("service not running (no PID file found)")
		}
		return fmt.Errorf("failed to read PID file: %v", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		if removeErr := os.Remove(config.PidFile); removeErr != nil {
			return fmt.Errorf("invalid PID file (%v) and failed to cleanup (%v)", err, removeErr)
		}
		return fmt.Errorf("invalid PID file: %v", err)
	}

	if !IsProcessRunning(pid) {
		if removeErr := os.Remove(config.PidFile); removeErr != nil {
			return fmt.Errorf("process not running and failed to cleanup stale PID file: %v", removeErr)
		}
		return fmt.Errorf("process not running (cleaned up stale PID file)")
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		if removeErr := os.Remove(config.PidFile); removeErr != nil {
			return fmt.Errorf("process not found (%v) and failed to cleanup PID file (%v)", err, removeErr)
		}
		return fmt.Errorf("process not found: %v", err)
	}

	if err := proc.Kill(); err != nil {
		return fmt.Errorf("failed to stop process (PID %d): %v", pid, err)
	}

	if err := os.Remove(config.PidFile); err != nil {
		fmt.Printf("⚠️  Service stopped (PID %d) but failed to cleanup PID file: %v\n", pid, err)
	} else {
		fmt.Printf("✅ Service stopped (PID %d)\n", pid)
	}
	return nil
}

func GetEnhancedStatus() (bool, int, *StatusInfo, error) {
	pidBytes, err := os.ReadFile(config.PidFile)
	if err != nil {
		if os.IsNotExist(err) {
			return false, 0, nil, nil // Not running, no error
		}
		return false, 0, nil, fmt.Errorf("failed to read PID file: %v", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		if removeErr := os.Remove(config.PidFile); removeErr != nil {
			return false, 0, nil, fmt.Errorf("invalid PID file (%v) and failed to cleanup (%v)", err, removeErr)
		}
		return false, 0, nil, fmt.Errorf("invalid PID file (cleaned up): %v", err)
	}

	if IsProcessRunning(pid) {
		// Try to read service state if available (this is a simplified approach)
		tempService := NewService(&config.Config{Debug: false, LogFormat: "text", Interval: 180})
		info := &StatusInfo{
			LastActivity:     time.Now(), // Default to now if we can't get actual info
			TeamsWindowCount: len(tempService.teamsMgr.FindTeamsWindows()),
			FailureStreak:    0, // Can't get this from external process
		}
		return true, pid, info, nil
	}

	// Process not running, clean up stale PID file
	if removeErr := os.Remove(config.PidFile); removeErr != nil {
		return false, pid, nil, fmt.Errorf("stale PID file found but failed to cleanup: %v", removeErr)
	}
	return false, pid, nil, nil // Stale PID cleaned up
}

type StatusInfo struct {
	LastActivity     time.Time
	TeamsWindowCount int
	FailureStreak    int
}
