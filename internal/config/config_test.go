package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name        string
		config      Config
		expectError bool
	}{
		{
			name: "valid config",
			config: Config{
				Interval:   180,
				Port:       8765,
				WebSocket:  true,
				LogFormat:  "text",
				MaxLogSize: 10,
				MaxLogAge:  30,
			},
			expectError: false,
		},
		{
			name: "valid config minimal",
			config: Config{
				Interval: 10,
			},
			expectError: false,
		},
		{
			name: "invalid interval too low",
			config: Config{
				Interval: 5,
			},
			expectError: true,
		},
		{
			name: "invalid interval too high",
			config: Config{
				Interval: 4000,
			},
			expectError: true,
		},
		{
			name: "invalid port too low",
			config: Config{
				WebSocket: true,
				Port:      1000,
				Interval:  180,
			},
			expectError: true,
		},
		{
			name: "invalid port too high",
			config: Config{
				WebSocket: true,
				Port:      70000,
				Interval:  180,
			},
			expectError: true,
		},
		{
			name: "websocket disabled should not validate port",
			config: Config{
				WebSocket: false,
				Port:      70000,
				Interval:  180,
			},
			expectError: false,
		},
		{
			name: "invalid log format",
			config: Config{
				Interval:  180,
				LogFormat: "invalid",
			},
			expectError: true,
		},
		{
			name: "valid json log format",
			config: Config{
				Interval:  180,
				LogFormat: "json",
			},
			expectError: false,
		},
		{
			name: "log rotation without log file",
			config: Config{
				Interval:  180,
				LogRotate: true,
			},
			expectError: true,
		},
		{
			name: "invalid max log size too low",
			config: Config{
				Interval:   180,
				LogFile:    filepath.Join(os.TempDir(), "test.log"),
				LogRotate:  true,
				MaxLogSize: 0,
				MaxLogAge:  30,
			},
			expectError: true,
		},
		{
			name: "invalid max log size too high",
			config: Config{
				Interval:   180,
				LogFile:    filepath.Join(os.TempDir(), "test.log"),
				LogRotate:  true,
				MaxLogSize: 2000,
				MaxLogAge:  30,
			},
			expectError: true,
		},
		{
			name: "invalid max log age too low",
			config: Config{
				Interval:   180,
				LogFile:    filepath.Join(os.TempDir(), "test.log"),
				LogRotate:  true,
				MaxLogSize: 10,
				MaxLogAge:  0,
			},
			expectError: true,
		},
		{
			name: "invalid max log age too high",
			config: Config{
				Interval:   180,
				LogFile:    filepath.Join(os.TempDir(), "test.log"),
				LogRotate:  true,
				MaxLogSize: 10,
				MaxLogAge:  400,
			},
			expectError: true,
		},
		{
			name: "valid log rotation config",
			config: Config{
				Interval:   180,
				LogFile:    filepath.Join(os.TempDir(), "test.log"),
				LogRotate:  true,
				MaxLogSize: 10,
				MaxLogAge:  30,
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.expectError && err == nil {
				t.Errorf("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestInitLoggerDebugMode(t *testing.T) {
	cfg := &Config{
		Debug:     true,
		LogFormat: "text",
	}

	logger := InitLogger(cfg)
	if logger == nil {
		t.Errorf("expected logger but got nil")
	}
}

func TestInitLoggerJSONFormat(t *testing.T) {
	cfg := &Config{
		Debug:     true,
		LogFormat: "json",
	}

	logger := InitLogger(cfg)
	if logger == nil {
		t.Errorf("expected logger but got nil")
	}
}

func TestInitLoggerWithLogFile(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "test.log")

	cfg := &Config{
		Debug:     false,
		LogFormat: "text",
		LogFile:   logFile,
	}

	logger := InitLogger(cfg)
	if logger == nil {
		t.Errorf("expected logger but got nil")
	}

	// Check if log file was created
	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		t.Errorf("log file should have been created")
	}

	// Force garbage collection to help close any open file handles
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
}

func TestInitLoggerWithRotation(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "test.log")

	cfg := &Config{
		Debug:      false,
		LogFormat:  "text",
		LogFile:    logFile,
		LogRotate:  true,
		MaxLogSize: 1,
		MaxLogAge:  1,
	}

	logger := InitLogger(cfg)
	if logger == nil {
		t.Errorf("expected logger but got nil")
	}

	// Force garbage collection to help close any open file handles
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
}

func TestSetupLogFileError(t *testing.T) {
	cfg := &Config{
		LogFile:   "C:\\nonexistent\\very\\deep\\invalid\\path\\that\\cannot\\be\\created\\test.log",
		LogRotate: false,
	}

	// Try to make this path definitely invalid by using a file as a directory
	tmpFile := filepath.Join(os.TempDir(), "testfile")
	if err := os.WriteFile(tmpFile, []byte("test"), 0o644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile)

	cfg.LogFile = filepath.Join(tmpFile, "invalid", "test.log") // Try to use a file as a directory

	_, err := setupLogFile(cfg)
	if err == nil {
		t.Error("expected error for invalid log file path")
	} else {
		t.Logf("correctly got error: %v", err)
	}
}

func TestRotateLogIfNeeded(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "test.log")

	// Create a test file
	content := strings.Repeat("test log line\n", 100000) // Create a large file
	if err := os.WriteFile(logFile, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to create test log file: %v", err)
	}

	cfg := &Config{
		LogFile:    logFile,
		MaxLogSize: 1, // 1 MB
	}

	err := rotateLogIfNeeded(cfg)
	if err != nil {
		t.Errorf("unexpected error during log rotation: %v", err)
	}
}

func TestRotateLogIfNeededNoFile(t *testing.T) {
	cfg := &Config{
		LogFile:    filepath.Join(os.TempDir(), "nonexistent.log"),
		MaxLogSize: 1,
	}

	err := rotateLogIfNeeded(cfg)
	if err != nil {
		t.Errorf("should not error when file doesn't exist: %v", err)
	}
}

func TestCleanupOldLogs(t *testing.T) {
	tempDir := t.TempDir()
	baseLogFile := filepath.Join(tempDir, "test.log")

	// Create some old log files
	oldLog1 := baseLogFile + ".2023-01-01T12-00-00"
	oldLog2 := baseLogFile + ".2023-01-02T12-00-00"

	if err := os.WriteFile(oldLog1, []byte("old log 1"), 0o644); err != nil {
		t.Fatalf("failed to create old log file: %v", err)
	}
	if err := os.WriteFile(oldLog2, []byte("old log 2"), 0o644); err != nil {
		t.Fatalf("failed to create old log file: %v", err)
	}

	// Set old modification times
	oldTime := time.Now().Add(-40 * 24 * time.Hour) // 40 days ago
	if err := os.Chtimes(oldLog1, oldTime, oldTime); err != nil {
		t.Logf("Failed to set mtime for %s: %v", oldLog1, err)
	}
	if err := os.Chtimes(oldLog2, oldTime, oldTime); err != nil {
		t.Logf("Failed to set mtime for %s: %v", oldLog2, err)
	}

	cfg := &Config{
		LogFile:   baseLogFile,
		MaxLogAge: 30, // 30 days
	}

	err := cleanupOldLogs(cfg)
	if err != nil {
		t.Errorf("unexpected error during cleanup: %v", err)
	}
}

func TestCheckPortAvailable(t *testing.T) {
	// Test with a port that should be available
	err := checkPortAvailable(0) // Port 0 means system will assign available port
	if err != nil {
		t.Errorf("port 0 should be available: %v", err)
	}
}

func TestPidFileInitialization(t *testing.T) {
	if PidFile == "" {
		t.Errorf("PidFile should be initialized")
	}

	if filepath.Base(PidFile) != "teams-green.pid" {
		t.Errorf("PidFile should end with teams-green.pid, got %s", PidFile)
	}
}
