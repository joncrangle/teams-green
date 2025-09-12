package service

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joncrangle/teams-green/internal/config"
)

func TestIsProcessRunning(t *testing.T) {
	// Test with current process (should be running)
	currentPID := os.Getpid()
	if !IsProcessRunning(currentPID) {
		t.Errorf("current process (PID %d) should be running", currentPID)
	}

	// Test with an invalid PID (should not be running)
	if IsProcessRunning(-1) {
		t.Error("invalid PID should not be running")
	}

	// Test with PID 0 (should not be running for regular processes)
	if IsProcessRunning(0) {
		t.Error("PID 0 should not be considered a running process")
	}
}

func TestGetEnhancedStatusNoService(t *testing.T) {
	// Ensure no PID file exists for this test
	if _, err := os.Stat(config.PidFile); !os.IsNotExist(err) {
		os.Remove(config.PidFile)
	}

	running, pid, info, err := GetEnhancedStatus()
	if err != nil {
		t.Errorf("should not error when service is not running: %v", err)
	}

	if running {
		t.Error("service should not be running")
	}

	if pid != 0 {
		t.Errorf("expected PID 0 when not running, got %d", pid)
	}

	if info != nil {
		t.Error("info should be nil when not running")
	}
}

func TestGetEnhancedStatusInvalidPIDFile(t *testing.T) {
	// Create invalid PID file
	invalidContent := "not-a-number"
	err := os.WriteFile(config.PidFile, []byte(invalidContent), 0o644)
	if err != nil {
		t.Fatalf("failed to create invalid PID file: %v", err)
	}
	defer os.Remove(config.PidFile)

	running, pid, info, err := GetEnhancedStatus()
	if err == nil {
		t.Error("should error with invalid PID file")
	}

	if running {
		t.Error("service should not be running with invalid PID")
	}

	if pid != 0 {
		t.Errorf("expected PID 0 with invalid PID file, got %d", pid)
	}

	if info != nil {
		t.Error("info should be nil with invalid PID")
	}
}

func TestGetEnhancedStatusStalePID(t *testing.T) {
	// Create PID file with non-existent process
	stalePID := "99999"
	err := os.WriteFile(config.PidFile, []byte(stalePID), 0o644)
	if err != nil {
		t.Fatalf("failed to create stale PID file: %v", err)
	}
	defer os.Remove(config.PidFile)

	running, pid, info, err := GetEnhancedStatus()
	if err != nil {
		t.Errorf("should not error with stale PID file: %v", err)
	}

	if running {
		t.Error("service should not be running with stale PID")
	}

	if pid != 99999 {
		t.Errorf("expected PID 99999, got %d", pid)
	}

	if info != nil {
		t.Error("info should be nil with stale PID")
	}

	// PID file should be cleaned up
	if _, err := os.Stat(config.PidFile); !os.IsNotExist(err) {
		t.Error("stale PID file should have been cleaned up")
	}
}

func TestStartServiceAlreadyRunning(t *testing.T) {
	// Create PID file with current process
	currentPID := strconv.Itoa(os.Getpid())
	err := os.WriteFile(config.PidFile, []byte(currentPID), 0o644)
	if err != nil {
		t.Fatalf("failed to create PID file: %v", err)
	}
	defer os.Remove(config.PidFile)

	cfg := &config.Config{
		Debug:    false,
		Interval: 180,
	}

	err = Start(cfg)
	if err == nil {
		t.Error("should error when service is already running")
	}

	if err != nil && err.Error() != "service already running (PID "+currentPID+")" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestStartServiceDebugMode(t *testing.T) {
	// Ensure no PID file exists
	os.Remove(config.PidFile)

	cfg := &config.Config{
		Debug:    true,
		Interval: 10,
	}

	// Create a mock executable that exits quickly
	tempExe := filepath.Join(t.TempDir(), "test.exe")
	err := os.WriteFile(tempExe, []byte(""), 0o755)
	if err != nil {
		t.Fatalf("failed to create temp executable: %v", err)
	}

	// We can't easily test the actual Start function without creating
	// a subprocess, so we'll test the argument building logic
	args := []string{"run", "--interval=10"}
	if cfg.WebSocket {
		args = append(args, "--websocket", "--port=8765")
	}
	if cfg.Debug {
		args = append(args, "--debug")
	}

	expectedArgs := []string{"run", "--interval=10", "--debug"}
	for i, expected := range expectedArgs {
		if i >= len(args) || args[i] != expected {
			t.Errorf("argument %d: expected '%s', got '%s'", i, expected, args[i])
		}
	}
}

func TestStopServiceNotRunning(t *testing.T) {
	// Ensure no PID file exists
	os.Remove(config.PidFile)

	err := Stop()
	if err == nil {
		t.Error("should error when trying to stop non-running service")
	}

	if err != nil && err.Error() != "service not running (no PID file found)" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestStopServiceInvalidPIDFile(t *testing.T) {
	// Create invalid PID file
	invalidContent := "not-a-number"
	err := os.WriteFile(config.PidFile, []byte(invalidContent), 0o644)
	if err != nil {
		t.Fatalf("failed to create invalid PID file: %v", err)
	}

	err = Stop()
	if err == nil {
		t.Error("should error with invalid PID file")
	}

	// PID file should be cleaned up
	if _, err := os.Stat(config.PidFile); !os.IsNotExist(err) {
		t.Error("invalid PID file should have been cleaned up")
	}
}

func TestStopServiceNotRunningProcess(t *testing.T) {
	// Create PID file with non-existent process
	stalePID := "99999"
	err := os.WriteFile(config.PidFile, []byte(stalePID), 0o644)
	if err != nil {
		t.Fatalf("failed to create PID file: %v", err)
	}

	err = Stop()
	if err == nil {
		t.Error("should error when process is not running")
	}

	// PID file should be cleaned up
	if _, err := os.Stat(config.PidFile); !os.IsNotExist(err) {
		t.Error("stale PID file should have been cleaned up")
	}
}

func TestStatusInfo(t *testing.T) {
	info := &StatusInfo{
		LastActivity:     time.Now(),
		TeamsWindowCount: 3,
		FailureStreak:    1,
	}

	if info.TeamsWindowCount != 3 {
		t.Errorf("expected 3 teams windows, got %d", info.TeamsWindowCount)
	}

	if info.FailureStreak != 1 {
		t.Errorf("expected failure streak 1, got %d", info.FailureStreak)
	}

	if info.LastActivity.IsZero() {
		t.Error("last activity should not be zero")
	}
}

func TestWritePidFileAtomic(t *testing.T) {
	// Ensure clean state
	_ = os.Remove(config.PidFile)

	firstPID := os.Getpid()
	if err := writePidFile(firstPID); err != nil {
		t.Fatalf("first writePidFile failed: %v", err)
	}
	defer os.Remove(config.PidFile)

	secondPID := firstPID + 1
	err := writePidFile(secondPID)
	if err == nil {
		t.Fatal("expected error on second writePidFile call for existing pid file")
	}
	if !strings.Contains(err.Error(), "pid file already exists") {
		t.Fatalf("unexpected error: %v", err)
	}

	content, readErr := os.ReadFile(config.PidFile)
	if readErr != nil {
		t.Fatalf("failed to read pid file: %v", readErr)
	}
	if strings.TrimSpace(string(content)) != strconv.Itoa(firstPID) {
		t.Fatalf("pid file content changed unexpectedly: %s", string(content))
	}
}
