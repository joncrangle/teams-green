package config

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

type Config struct {
	Debug     bool
	Interval  int
	WebSocket bool
	Port      int
}

var PidFile string

func init() {
	PidFile = "teams-green.pid"
	if tmpDir := os.Getenv("TEMP"); tmpDir != "" {
		PidFile = filepath.Join(tmpDir, "teams-green.pid")
	}
}

func InitLogger(debug bool) *slog.Logger {
	var handler slog.Handler

	if debug {
		// Text handler for debug mode (human readable)
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})
	} else {
		// Discard logs in background mode
		handler = slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})
	}

	return slog.New(handler)
}
