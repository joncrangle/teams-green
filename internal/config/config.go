// Package config handles application configuration, logging setup, and validation.
package config

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Debug      bool
	Interval   int
	WebSocket  bool
	Port       int
	LogFormat  string // "text", "json"
	LogFile    string
	LogRotate  bool
	MaxLogSize int // MB
	MaxLogAge  int // days

	// ActivityMode controls how keys are sent: "focus" (default) or "global"
	ActivityMode string

	// Teams key sending timing configuration (milliseconds)
	FocusDelayMs      int // Delay after setting focus before sending key
	RestoreDelayMs    int // Delay after restoring minimized window
	KeyProcessDelayMs int // Delay before restoring original focus

	// Input detection configuration (milliseconds)
	InputThresholdMs int // How recent input must be to defer Teams activity

	// WebSocket timeout configuration (seconds)
	WebSocketReadTimeout  int // Read timeout (default: 60)
	WebSocketWriteTimeout int // Write timeout (default: 60)
	WebSocketIdleTimeout  int // Idle timeout (default: 300)
}

// GetFocusDelay returns the focus delay as a time.Duration with fallback to default
func (cfg *Config) GetFocusDelay() time.Duration {
	if cfg.FocusDelayMs > 0 {
		return time.Duration(cfg.FocusDelayMs) * time.Millisecond
	}
	return 150 * time.Millisecond
}

// GetRestoreDelay returns the restore delay as a time.Duration with fallback to default
func (cfg *Config) GetRestoreDelay() time.Duration {
	if cfg.RestoreDelayMs > 0 {
		return time.Duration(cfg.RestoreDelayMs) * time.Millisecond
	}
	return 100 * time.Millisecond
}

// GetKeyProcessDelay returns the key process delay as a time.Duration with fallback to default
func (cfg *Config) GetKeyProcessDelay() time.Duration {
	if cfg.KeyProcessDelayMs > 0 {
		return time.Duration(cfg.KeyProcessDelayMs) * time.Millisecond
	}
	return 150 * time.Millisecond
}

// GetInputThreshold returns the duration threshold for considering input as "active"
func (cfg *Config) GetInputThreshold() time.Duration {
	if cfg.InputThresholdMs > 0 {
		return time.Duration(cfg.InputThresholdMs) * time.Millisecond
	}
	return 500 * time.Millisecond
}

// GetWebSocketReadTimeout returns the read timeout in seconds with fallback to default
func (cfg *Config) GetWebSocketReadTimeout() time.Duration {
	if cfg.WebSocketReadTimeout > 0 {
		return time.Duration(cfg.WebSocketReadTimeout) * time.Second
	}
	return 60 * time.Second
}

// GetWebSocketWriteTimeout returns the write timeout in seconds with fallback to default
func (cfg *Config) GetWebSocketWriteTimeout() time.Duration {
	if cfg.WebSocketWriteTimeout > 0 {
		return time.Duration(cfg.WebSocketWriteTimeout) * time.Second
	}
	return 60 * time.Second
}

// GetWebSocketIdleTimeout returns the idle timeout in seconds with fallback to default
func (cfg *Config) GetWebSocketIdleTimeout() time.Duration {
	if cfg.WebSocketIdleTimeout > 0 {
		return time.Duration(cfg.WebSocketIdleTimeout) * time.Second
	}
	return 300 * time.Second // Default: 5 minutes (300 seconds)
}

// IsDebugEnabled returns whether debug mode is enabled
func (cfg *Config) IsDebugEnabled() bool {
	return cfg.Debug
}

// GetActivityMode returns sanitized activity mode (focus|global)
func (cfg *Config) GetActivityMode() string {
	mode := strings.ToLower(strings.TrimSpace(cfg.ActivityMode))
	if mode != "global" {
		return "focus"
	}
	return mode
}

var PidFile string

// Global log file handle that can be closed properly
var (
	logFile       *os.File
	logFileMutex  sync.RWMutex // Use RWMutex for better concurrency
	logFileClosed bool         // Track if file is already closed
)

type LogFileCloser struct {
	*os.File
	closed *bool
	mu     *sync.RWMutex
}

func (lfc *LogFileCloser) Write(p []byte) (n int, err error) {
	lfc.mu.RLock()
	defer lfc.mu.RUnlock()

	if *lfc.closed || lfc.File == nil {
		return 0, fmt.Errorf("log file is closed")
	}
	return lfc.File.Write(p)
}

func (lfc *LogFileCloser) Close() error {
	lfc.mu.Lock()
	defer lfc.mu.Unlock()

	if *lfc.closed || lfc.File == nil {
		return nil // Already closed
	}

	*lfc.closed = true
	err := lfc.File.Close()
	lfc.File = nil
	return err
}

func init() {
	// Use %LOCALAPPDATA%\teams-green
	appDataDir := os.Getenv("LOCALAPPDATA")
	if appDataDir == "" {
		// Fallback to temp directory if LOCALAPPDATA is not available
		if tmpDir := os.Getenv("TEMP"); tmpDir != "" {
			PidFile = filepath.Join(tmpDir, "teams-green.pid")
		} else {
			PidFile = "teams-green.pid"
		}
	} else {
		teamsGreenDir := filepath.Join(appDataDir, "teams-green")
		// Create directory if it doesn't exist
		if err := os.MkdirAll(teamsGreenDir, 0o755); err != nil {
			// Fallback to temp if we can't create the directory
			if tmpDir := os.Getenv("TEMP"); tmpDir != "" {
				PidFile = filepath.Join(tmpDir, "teams-green.pid")
			} else {
				PidFile = "teams-green.pid"
			}
		} else {
			PidFile = filepath.Join(teamsGreenDir, "teams-green.pid")
		}
	}
}

func InitLogger(cfg *Config) {
	var handler slog.Handler
	output := io.Discard

	if cfg.Debug {
		output = os.Stdout
	} else if cfg.LogFile != "" {
		var err error
		output, err = setupLogFile(cfg)
		if err != nil {
			// Fallback to stdout if file setup fails
			output = os.Stdout
			slog.Warn("Failed to setup log file, falling back to stdout", slog.String("error", err.Error()))
		}
	}

	opts := &slog.HandlerOptions{}
	if cfg.Debug {
		opts.Level = slog.LevelDebug
	} else {
		opts.Level = slog.LevelInfo
	}

	if cfg.LogFormat == "json" {
		handler = slog.NewJSONHandler(output, opts)
	} else {
		handler = slog.NewTextHandler(output, opts)
	}

	logger := slog.New(handler)
	slog.SetDefault(logger)
}

func setupLogFile(cfg *Config) (io.Writer, error) {
	// Ensure log directory exists
	logDir := filepath.Dir(cfg.LogFile)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	// Handle rotation if enabled
	if cfg.LogRotate {
		if err := rotateLogIfNeeded(cfg); err != nil {
			return nil, fmt.Errorf("failed to rotate log: %w", err)
		}
		// Clean up old log files
		if err := cleanupOldLogs(cfg); err != nil {
			slog.Warn("Failed to cleanup old log files", slog.String("error", err.Error()))
		}
	}

	logFileMutex.Lock()
	defer logFileMutex.Unlock()

	// Close existing log file if open
	if logFile != nil && !logFileClosed {
		logFile.Close()
		logFile = nil
	}

	file, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	logFile = file
	logFileClosed = false
	return &LogFileCloser{
		File:   file,
		closed: &logFileClosed,
		mu:     &logFileMutex,
	}, nil
}

// CloseLogFile should be called during shutdown to properly close the log file
func CloseLogFile() {
	logFileMutex.Lock()
	defer logFileMutex.Unlock()
	if logFile != nil && !logFileClosed {
		logFile.Close()
		logFile = nil
		logFileClosed = true
	}
}

func rotateLogIfNeeded(cfg *Config) error {
	stat, err := os.Stat(cfg.LogFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // File doesn't exist, no rotation needed
		}
		return fmt.Errorf("failed to stat log file: %w", err)
	}

	sizeMB := stat.Size() / (1024 * 1024)
	if sizeMB >= int64(cfg.MaxLogSize) {
		timestamp := time.Now().Format("2006-01-02T15-04-05")
		rotatedName := fmt.Sprintf("%s.%s", cfg.LogFile, timestamp)

		if err := os.Rename(cfg.LogFile, rotatedName); err != nil {
			return fmt.Errorf("failed to rotate log file: %w", err)
		}

		slog.Info("Log file rotated",
			slog.String("original", cfg.LogFile),
			slog.String("rotated", rotatedName),
			slog.Int64("size_mb", sizeMB))
	}

	return nil
}

func cleanupOldLogs(cfg *Config) error {
	logDir := filepath.Dir(cfg.LogFile)
	baseLogName := filepath.Base(cfg.LogFile)

	entries, err := os.ReadDir(logDir)
	if err != nil {
		return fmt.Errorf("failed to read log directory: %w", err)
	}

	// Find all rotated log files
	var rotatedLogs []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if strings.HasPrefix(name, baseLogName+".") && len(name) > len(baseLogName)+1 {
			rotatedLogs = append(rotatedLogs, filepath.Join(logDir, name))
		}
	}

	// Sort by modification time (newest first)
	sort.Slice(rotatedLogs, func(i, j int) bool {
		statI, errI := os.Stat(rotatedLogs[i])
		statJ, errJ := os.Stat(rotatedLogs[j])
		if errI != nil || errJ != nil {
			return false
		}
		return statI.ModTime().After(statJ.ModTime())
	})

	maxAge := time.Duration(cfg.MaxLogAge) * 24 * time.Hour
	now := time.Now()

	// Remove logs older than MaxLogAge days
	for _, logPath := range rotatedLogs {
		stat, err := os.Stat(logPath)
		if err != nil {
			continue
		}

		if now.Sub(stat.ModTime()) > maxAge {
			if err := os.Remove(logPath); err != nil {
				slog.Warn("Failed to remove old log file",
					slog.String("file", logPath),
					slog.String("error", err.Error()))
			} else {
				slog.Debug("Removed old log file", slog.String("file", logPath))
			}
		}
	}

	return nil
}

// Validate validates the configuration and returns any errors
func (cfg *Config) Validate() error {
	var errors []string

	errors = append(errors, cfg.validateInterval()...)
	errors = append(errors, cfg.validateTiming()...)
	errors = append(errors, cfg.validateWebSocket()...)
	errors = append(errors, cfg.validateActivityMode()...)
	errors = append(errors, cfg.validateLogConfig()...)

	if len(errors) > 0 {
		return fmt.Errorf("configuration validation failed:\n  - %s", strings.Join(errors, "\n  - "))
	}

	return nil
}

func (cfg *Config) validateInterval() []string {
	var errors []string
	if cfg.Interval < 10 {
		errors = append(errors, "interval must be at least 10 seconds")
	}
	if cfg.Interval > 3600 {
		errors = append(errors, "interval must be no more than 3600 seconds (1 hour)")
	}
	return errors
}

func (cfg *Config) validateTiming() []string {
	var errors []string
	if cfg.FocusDelayMs < 0 || cfg.FocusDelayMs > 1000 {
		errors = append(errors, "focus delay must be between 0 and 1000 milliseconds")
	}
	if cfg.RestoreDelayMs < 0 || cfg.RestoreDelayMs > 1000 {
		errors = append(errors, "restore delay must be between 0 and 1000 milliseconds")
	}
	if cfg.KeyProcessDelayMs < 0 || cfg.KeyProcessDelayMs > 5000 {
		errors = append(errors, "key process delay must be between 0 and 5000 milliseconds")
	}
	if cfg.InputThresholdMs < 0 || cfg.InputThresholdMs > 5000 {
		errors = append(errors, "input threshold must be between 0 and 5000 milliseconds")
	}
	return errors
}

func (cfg *Config) validateWebSocket() []string {
	var errors []string
	if cfg.WebSocketReadTimeout < 0 || cfg.WebSocketReadTimeout > 3600 {
		errors = append(errors, "WebSocket read timeout must be between 0 and 3600 seconds")
	}
	if cfg.WebSocketWriteTimeout < 0 || cfg.WebSocketWriteTimeout > 3600 {
		errors = append(errors, "WebSocket write timeout must be between 0 and 3600 seconds")
	}
	if cfg.WebSocketIdleTimeout < 0 || cfg.WebSocketIdleTimeout > 36000 {
		errors = append(errors, "WebSocket idle timeout must be between 0 and 36000 seconds")
	}

	if cfg.WebSocket {
		if cfg.Port < 1024 {
			errors = append(errors, "port must be at least 1024")
		}
		if cfg.Port > 65535 {
			errors = append(errors, "port must be no more than 65535")
		}
		// Security: Warn about commonly used ports that might conflict
		commonPorts := []int{80, 443, 8080, 8443, 3000, 3001, 5000, 5001}
		for _, port := range commonPorts {
			if cfg.Port == port {
				slog.Warn("Port is commonly used and may conflict with other services", "port", port)
				break
			}
		}

		// Verify the port is available (best-effort). This attempts a bind and then
		// immediately releases it. If unavailable, treat as validation error.
		if err := checkPortAvailable(cfg.Port); err != nil {
			errors = append(errors, fmt.Sprintf("port %d is unavailable: %v", cfg.Port, err))
		}
	}
	return errors
}

func (cfg *Config) validateActivityMode() []string {
	var errors []string
	if cfg.ActivityMode != "" {
		am := strings.ToLower(cfg.ActivityMode)
		if am != "focus" && am != "global" {
			errors = append(errors, "activity mode must be 'focus' or 'global'")
		}
	}
	return errors
}

func (cfg *Config) validateLogConfig() []string {
	var errors []string
	// Validate log format with additional security
	if cfg.LogFormat != "" && cfg.LogFormat != "text" && cfg.LogFormat != "json" {
		errors = append(errors, "log format must be 'text' or 'json'")
	}

	// Validate log file path with security checks
	if cfg.LogFile != "" {
		if !filepath.IsAbs(cfg.LogFile) {
			errors = append(errors, "log file path must be absolute")
		}

		// Security: Prevent path traversal attacks
		if strings.Contains(cfg.LogFile, "..") {
			errors = append(errors, "log file path cannot contain '..' (path traversal protection)")
		}

		// Security: Ensure log file is in safe directory
		logDir := filepath.Dir(cfg.LogFile)
		if err := validateLogDirectory(logDir); err != nil {
			errors = append(errors, fmt.Sprintf("log directory validation failed: %v", err))
		}

		// Check if directory exists or can be created
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			errors = append(errors, fmt.Sprintf("cannot create log directory: %v", err))
		}
	}

	// Validate log rotation settings
	if cfg.LogRotate {
		if cfg.LogFile == "" {
			errors = append(errors, "log file must be specified when log rotation is enabled")
		}
		if cfg.MaxLogSize < 1 {
			errors = append(errors, "max log size must be at least 1 MB")
		}
		if cfg.MaxLogSize > 1000 {
			errors = append(errors, "max log size must be no more than 1000 MB")
		}
		if cfg.MaxLogAge < 1 {
			errors = append(errors, "max log age must be at least 1 day")
		}
		if cfg.MaxLogAge > 365 {
			errors = append(errors, "max log age must be no more than 365 days")
		}
	}
	return errors
}

func validateLogDirectory(logDir string) error {
	// Security: Prevent logging to system directories
	systemDirs := []string{
		"C:\\Windows", "C:\\System32", "C:\\Program Files", "C:\\Program Files (x86)",
		"/etc", "/bin", "/sbin", "/usr/bin", "/usr/sbin", "/boot", "/sys", "/proc",
	}

	logDirLower := strings.ToLower(logDir)
	for _, sysDir := range systemDirs {
		sysDirLower := strings.ToLower(sysDir)
		if strings.HasPrefix(logDirLower, sysDirLower) {
			return fmt.Errorf("cannot write logs to system directory: %s", logDir)
		}
	}

	// Security: Ensure directory is not a symlink to a sensitive location
	if info, err := os.Lstat(logDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("log directory cannot be a symbolic link: %s", logDir)
		}
	}

	return nil
}

func checkPortAvailable(port int) error {
	addr := ":" + strconv.Itoa(port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	return nil
}
