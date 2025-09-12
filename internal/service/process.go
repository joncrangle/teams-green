package service

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/joncrangle/teams-green/internal/config"

	"golang.org/x/sys/windows"
)

const (
	stillActiveExitCode = 259 // STILL_ACTIVE on Windows
	pidFilePermissions  = 0o644
)

// IsProcessRunning checks if a process with the given PID is currently running.
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

	// STILL_ACTIVE indicates the process is still running
	return exitCode == stillActiveExitCode
}

// Start launches the teams-green service in the background or foreground based on configuration.
func Start(cfg *config.Config) error {
	// Check if service is already running
	if err := checkServiceAlreadyRunning(); err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	args := buildCommandArgs(cfg)

	if cfg.Debug {
		return startInForeground(exe, args)
	}

	return startInBackground(exe, args)
}

// checkServiceAlreadyRunning checks if the service is already running and handles stale PID files.
func checkServiceAlreadyRunning() error {
	pidBytes, err := os.ReadFile(config.PidFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No PID file, service not running
		}
		return fmt.Errorf("failed to read PID file: %w", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		// Invalid PID file, clean it up
		os.Remove(config.PidFile)
		return nil
	}

	if IsProcessRunning(pid) {
		return fmt.Errorf("service already running (PID %d)", pid)
	}

	// Clean up stale PID file
	os.Remove(config.PidFile)
	return nil
}

// buildCommandArgs constructs the command line arguments for the service.
func buildCommandArgs(cfg *config.Config) []string {
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
	return args
}

// startInForeground runs the service in the foreground for debugging.
func startInForeground(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// startInBackground runs the service in the background with proper Windows attributes.
func startInBackground(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP,
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start service: %w", err)
	}

	if err := writePidFile(cmd.Process.Pid); err != nil {
		// Attempt to kill the process before returning error
		if killErr := cmd.Process.Kill(); killErr != nil {
			time.Sleep(100 * time.Millisecond)
			if waitErr := cmd.Wait(); waitErr != nil {
				return fmt.Errorf("service started but failed to write PID file (%w), failed to cleanup process (%v), and failed to wait for process (%v)", err, killErr, waitErr)
			}
			return fmt.Errorf("service started but failed to write PID file (%w) and failed to kill process (%v)", err, killErr)
		}

		if waitErr := cmd.Wait(); waitErr != nil {
			return fmt.Errorf("service started but failed to write PID file (%w), killed process but failed to wait (%v)", err, waitErr)
		}

		return fmt.Errorf("service started but failed to write PID file: %w", err)
	}

	fmt.Printf("Service started in background (PID %d)\n", cmd.Process.Pid)
	return nil
}

// writePidFile writes the process ID to the PID file atomically.
// It fails if the pid file already exists to avoid races where two instances
// start nearly simultaneously. If a stale pid file exists pointing to a dead
// process, the caller should have removed it prior to calling this function.
func writePidFile(pid int) error {
	pidContent := fmt.Sprintf("%d", pid)
	// Use O_CREATE|O_EXCL for atomic creation; O_WRONLY to write.
	f, err := os.OpenFile(config.PidFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, pidFilePermissions)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("pid file already exists: %w", err)
		}
		return fmt.Errorf("failed to create pid file: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(pidContent); err != nil {
		return fmt.Errorf("failed to write pid file: %w", err)
	}
	// Ensure contents are flushed.
	if err := f.Sync(); err != nil {
		return fmt.Errorf("failed to sync pid file: %w", err)
	}
	return nil
}

// Stop terminates the running teams-green service process.
func Stop() error {
	pidBytes, err := os.ReadFile(config.PidFile)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("service not running (no PID file found)")
		}
		return fmt.Errorf("failed to read PID file: %w", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		return cleanupPidFileWithError(fmt.Errorf("invalid PID file: %w", err))
	}

	if !IsProcessRunning(pid) {
		return cleanupPidFileWithError(fmt.Errorf("process not running"))
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return cleanupPidFileWithError(fmt.Errorf("process not found: %w", err))
	}

	if err := proc.Kill(); err != nil {
		return fmt.Errorf("failed to stop process (PID %d): %w", pid, err)
	}

	if err := os.Remove(config.PidFile); err != nil {
		fmt.Printf("Service stopped (PID %d) but failed to cleanup PID file: %v\n", pid, err)
	} else {
		fmt.Printf("Service stopped (PID %d)\n", pid)
	}
	return nil
}

func cleanupPidFileWithError(err error) error {
	if removeErr := os.Remove(config.PidFile); removeErr != nil {
		return fmt.Errorf("%v (cleanup failed: %v)", err, removeErr)
	}
	return err
}

// GetEnhancedStatus checks if the service is running and returns detailed status information.
func GetEnhancedStatus() (bool, int, *StatusInfo, error) {
	pidBytes, err := os.ReadFile(config.PidFile)
	if err != nil {
		if os.IsNotExist(err) {
			return false, 0, nil, nil // Not running, no error
		}
		return false, 0, nil, fmt.Errorf("failed to read PID file: %w", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		if removeErr := os.Remove(config.PidFile); removeErr != nil {
			return false, 0, nil, fmt.Errorf("invalid PID file (%v) and failed to cleanup (%v)", err, removeErr)
		}
		return false, 0, nil, fmt.Errorf("invalid PID file (cleaned up): %v", err)
	}

	if IsProcessRunning(pid) {
		// Get actual Teams window count by detecting windows
		teamsWindowCount := getTeamsWindowCountForStatus()

		info := &StatusInfo{
			LastActivity:     time.Now(), // Default to now if we can't get actual info
			TeamsWindowCount: teamsWindowCount,
			FailureStreak:    0, // Can't get this from external process
		}
		return true, pid, info, nil
	}

	// Process not running, clean up stale PID file
	if removeErr := os.Remove(config.PidFile); removeErr != nil {
		return false, pid, nil, fmt.Errorf("stale PID file found but failed to cleanup: %w", removeErr)
	}
	return false, pid, nil, nil // Stale PID cleaned up
}

// StatusInfo contains detailed information about the service's current state.
type StatusInfo struct {
	LastActivity     time.Time // Timestamp of the last Teams activity
	TeamsWindowCount int       // Number of detected Teams windows
	FailureStreak    int       // Current failure streak count
}

// getTeamsWindowCountForStatus detects Teams windows for status reporting.
// This uses the same logic as the main service's Teams detection but simplified for external use.
func getTeamsWindowCountForStatus() int {
	var windowCount int

	// Use the same window enumeration approach as the main service
	enumCallback := func(hwnd syscall.Handle, _ uintptr) uintptr {
		// Skip invisible windows
		ret, _, _ := procIsWindowVisible.Call(uintptr(hwnd))
		if ret == 0 {
			return 1
		}

		var pid uint32
		_, _, _ = procGetWindowThreadProcessID.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&pid)))
		if pid == 0 {
			return 1
		}

		// Get process executable name
		hProcess, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if err != nil || hProcess == 0 {
			return 1
		}
		defer func() {
			_ = windows.CloseHandle(hProcess)
		}()

		var exeName [windows.MAX_PATH]uint16
		size := uint32(len(exeName))
		if err := windows.QueryFullProcessImageName(hProcess, 0, &exeName[0], &size); err != nil {
			return 1
		}

		// Extract base executable name (same logic as teams.go)
		exePath := windows.UTF16ToString(exeName[:size])
		lastSlash := strings.LastIndexByte(exePath, '\\')
		var exeBase string
		if lastSlash != -1 {
			exeBase = strings.ToLower(exePath[lastSlash+1:])
		} else {
			exeBase = strings.ToLower(exePath)
		}

		// Check if this is a Teams executable
		if slices.Contains(teamsExecutables, exeBase) {
			windowCount++
		}

		return 1
	}

	// Enumerate all windows
	_, _, _ = procEnumWindows.Call(
		syscall.NewCallback(enumCallback),
		0,
	)

	return windowCount
}
