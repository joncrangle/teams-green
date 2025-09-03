package service

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

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
		// Attempt to kill the process before returning error
		if killErr := cmd.Process.Kill(); killErr != nil {
			// Wait a bit to ensure process cleanup, then try to get exit status
			time.Sleep(100 * time.Millisecond)
			if waitErr := cmd.Wait(); waitErr != nil {
				return fmt.Errorf("service started but failed to write PID file (%w), failed to cleanup process (%v), and failed to wait for process (%v)", err, killErr, waitErr)
			}
			return fmt.Errorf("service started but failed to write PID file (%w) and failed to kill process (%v)", err, killErr)
		}

		// Wait for process to actually exit
		if waitErr := cmd.Wait(); waitErr != nil {
			return fmt.Errorf("service started but failed to write PID file (%w), killed process but failed to wait (%v)", err, waitErr)
		}

		return fmt.Errorf("service started but failed to write PID file: %w", err)
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
		return fmt.Errorf("failed to stop process (PID %d): %v", pid, err)
	}

	if err := os.Remove(config.PidFile); err != nil {
		fmt.Printf("⚠️  Service stopped (PID %d) but failed to cleanup PID file: %v\n", pid, err)
	} else {
		fmt.Printf("✅ Service stopped (PID %d)\n", pid)
	}
	return nil
}

func cleanupPidFileWithError(err error) error {
	if removeErr := os.Remove(config.PidFile); removeErr != nil {
		return fmt.Errorf("%v (cleanup failed: %v)", err, removeErr)
	}
	return err
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
		return false, pid, nil, fmt.Errorf("stale PID file found but failed to cleanup: %v", removeErr)
	}
	return false, pid, nil, nil // Stale PID cleaned up
}

type StatusInfo struct {
	LastActivity     time.Time
	TeamsWindowCount int
	FailureStreak    int
}

// getTeamsWindowCountForStatus detects Teams windows for status reporting
// This is a simplified version of the main service's Teams detection logic
func getTeamsWindowCountForStatus() int {
	var windowCount int
	teamsExecutables := []string{"ms-teams.exe", "teams.exe", "msteams.exe"}

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

		// Extract base executable name
		exePath := windows.UTF16ToString(exeName[:size])
		lastSlash := strings.LastIndexByte(exePath, '\\')
		var exeBase string
		if lastSlash != -1 {
			exeBase = strings.ToLower(exePath[lastSlash+1:])
		} else {
			exeBase = strings.ToLower(exePath)
		}

		// Check if this is a Teams executable
		for _, teamsExe := range teamsExecutables {
			if exeBase == teamsExe {
				windowCount++
				break
			}
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
