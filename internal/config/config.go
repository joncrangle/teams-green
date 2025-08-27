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

func InitLogger(cfg *Config) *slog.Logger {
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

	return slog.New(handler)
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

	// Validate interval
	if cfg.Interval < 10 {
		errors = append(errors, "interval must be at least 10 seconds")
	}
	if cfg.Interval > 3600 {
		errors = append(errors, "interval must be no more than 3600 seconds (1 hour)")
	}

	// Validate port
	if cfg.WebSocket {
		if cfg.Port < 1024 {
			errors = append(errors, "port must be at least 1024")
		}
		if cfg.Port > 65535 {
			errors = append(errors, "port must be no more than 65535")
		}
		// Remove redundant port availability check here - let WebSocket server handle it gracefully
	}

	// Validate log format
	if cfg.LogFormat != "" && cfg.LogFormat != "text" && cfg.LogFormat != "json" {
		errors = append(errors, "log format must be 'text' or 'json'")
	}

	// Validate log file path
	if cfg.LogFile != "" {
		if !filepath.IsAbs(cfg.LogFile) {
			errors = append(errors, "log file path must be absolute")
		}

		// Check if directory exists or can be created
		logDir := filepath.Dir(cfg.LogFile)
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

	if len(errors) > 0 {
		return fmt.Errorf("configuration validation failed:\n  - %s", strings.Join(errors, "\n  - "))
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
