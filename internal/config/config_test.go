package config

import (
	"path/filepath"
	"testing"
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
			name: "invalid interval too low",
			config: Config{
				Interval: 5,
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
			name: "invalid log format",
			config: Config{
				Interval:  180,
				LogFormat: "invalid",
			},
			expectError: true,
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

func TestInitLogger(t *testing.T) {
	cfg := &Config{
		Debug:     true,
		LogFormat: "text",
	}

	logger := InitLogger(cfg)
	if logger == nil {
		t.Errorf("expected logger but got nil")
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
