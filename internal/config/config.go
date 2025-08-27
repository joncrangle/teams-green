package config

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
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

func init() {
	// Use %LOCALAPPDATA%\teams-green for better security
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
		// Open log file with rotation support
		file, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			// Fallback to stdout if file opening fails
			output = os.Stdout
		} else {
			output = file
			// Simple rotation check (could be enhanced with proper rotation library)
			if cfg.LogRotate {
				if stat, err := file.Stat(); err == nil {
					sizeMB := stat.Size() / (1024 * 1024)
					if sizeMB > int64(cfg.MaxLogSize) {
						// Basic rotation - rename current file and create new one
						file.Close()
						rotatedName := cfg.LogFile + ".old"
						if err := os.Rename(cfg.LogFile, rotatedName); err != nil {
							// If rename fails, continue with current file
							if file, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
								output = file
							}
						} else {
							if file, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
								output = file
							}
						}
					}
				}
			}
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
