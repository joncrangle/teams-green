package service

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

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

	if err := os.WriteFile(config.PidFile, fmt.Appendf(nil, "%d", cmd.Process.Pid), 0o644); err != nil {
		return fmt.Errorf("service started but failed to write PID file: %v", err)
	}

	fmt.Printf("🚀 Service started in background (PID %d)\n", cmd.Process.Pid)
	return nil
}

func Stop() error {
	pidBytes, err := os.ReadFile(config.PidFile)
	if err != nil {
		return fmt.Errorf("service not running (no PID file)")
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		os.Remove(config.PidFile)
		return fmt.Errorf("invalid PID file: %v", err)
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		os.Remove(config.PidFile)
		return fmt.Errorf("process not found: %v", err)
	}

	if err := proc.Kill(); err != nil {
		return fmt.Errorf("error stopping process: %v", err)
	}

	os.Remove(config.PidFile)
	fmt.Printf("✅ Service stopped (PID %d)\n", pid)
	return nil
}

func GetStatus() (bool, int, error) {
	pidBytes, err := os.ReadFile(config.PidFile)
	if err != nil {
		return false, 0, nil // Not running
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		os.Remove(config.PidFile)
		return false, 0, fmt.Errorf("invalid PID file")
	}

	if IsProcessRunning(pid) {
		return true, pid, nil
	}
	os.Remove(config.PidFile)
	return false, pid, nil // Stale PID
}
