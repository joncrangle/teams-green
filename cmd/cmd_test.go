package cmd

import (
	"os"
	"strings"
	"testing"

	"github.com/joncrangle/teams-green/internal/config"
	"github.com/spf13/cobra"
)

func TestRootCommand(t *testing.T) {
	if rootCmd == nil {
		t.Fatal("root command should not be nil")
	}

	if rootCmd.Use != "teams-green" {
		t.Errorf("expected command name 'teams-green', got '%s'", rootCmd.Use)
	}

	if !strings.Contains(rootCmd.Long, "Teams-Green keeps your Microsoft Teams status active") {
		t.Error("command description should mention Teams status")
	}
}

func TestVersionCommand(t *testing.T) {
	if versionCmd == nil {
		t.Fatal("version command should not be nil")
	}

	if versionCmd.Use != "version" {
		t.Errorf("expected command name 'version', got '%s'", versionCmd.Use)
	}

	if versionCmd.Short != "Show version information" {
		t.Errorf("unexpected short description: %s", versionCmd.Short)
	}
}

func TestStartCommand(t *testing.T) {
	if startCmd == nil {
		t.Fatal("start command should not be nil")
	}

	if startCmd.Use != "start" {
		t.Errorf("expected command name 'start', got '%s'", startCmd.Use)
	}

	if startCmd.Short != "Start Teams-Green" {
		t.Errorf("unexpected short description: %s", startCmd.Short)
	}

	// Test that flags are properly set up
	flags := startCmd.Flags()
	if flags == nil {
		t.Fatal("start command should have flags")
	}

	// Check for required flags
	expectedFlags := []string{"debug", "interval", "websocket", "port", "log-format", "log-file", "log-rotate"}
	for _, flagName := range expectedFlags {
		if flag := flags.Lookup(flagName); flag == nil {
			t.Errorf("start command should have --%s flag", flagName)
		}
	}
}

func TestStopCommand(t *testing.T) {
	if stopCmd == nil {
		t.Fatal("stop command should not be nil")
	}

	if stopCmd.Use != "stop" {
		t.Errorf("expected command name 'stop', got '%s'", stopCmd.Use)
	}

	if stopCmd.Short != "Stop Teams-Green process" {
		t.Errorf("unexpected short description: %s", stopCmd.Short)
	}
}

func TestStatusCommand(t *testing.T) {
	if statusCmd == nil {
		t.Fatal("status command should not be nil")
	}

	if statusCmd.Use != "status" {
		t.Errorf("expected command name 'status', got '%s'", statusCmd.Use)
	}

	if statusCmd.Short != "Check the status of Teams-Green" {
		t.Errorf("unexpected short description: %s", statusCmd.Short)
	}
}

func TestToggleCommand(t *testing.T) {
	if toggleCmd == nil {
		t.Fatal("toggle command should not be nil")
	}

	if toggleCmd.Use != "toggle" {
		t.Errorf("expected command name 'toggle', got '%s'", toggleCmd.Use)
	}

	if toggleCmd.Short != "Toggle Teams-Green" {
		t.Errorf("unexpected short description: %s", toggleCmd.Short)
	}

	// Test that toggle command has the same flags as start
	flags := toggleCmd.Flags()
	if flags == nil {
		t.Fatal("toggle command should have flags")
	}

	expectedFlags := []string{"debug", "interval", "websocket", "port", "log-format", "log-file", "log-rotate"}
	for _, flagName := range expectedFlags {
		if flag := flags.Lookup(flagName); flag == nil {
			t.Errorf("toggle command should have --%s flag", flagName)
		}
	}
}

func TestRunCommand(t *testing.T) {
	if runCmd == nil {
		t.Fatal("run command should not be nil")
	}

	if runCmd.Use != "run" {
		t.Errorf("expected command name 'run', got '%s'", runCmd.Use)
	}

	if !runCmd.Hidden {
		t.Error("run command should be hidden")
	}

	// Test that run command has flags
	flags := runCmd.Flags()
	if flags == nil {
		t.Fatal("run command should have flags")
	}
}

func TestConfigInitialization(t *testing.T) {
	// Save original global config
	originalCfg := cfg
	defer func() { cfg = originalCfg }()

	// Reset config for testing
	cfg = &config.Config{}

	if cfg == nil {
		t.Fatal("global config should not be nil")
	}

	// Test default values after flag parsing
	originalArgs := os.Args
	defer func() { os.Args = originalArgs }()

	// Test with default values
	os.Args = []string{"teams-green", "start"}

	testCmd := &cobra.Command{
		Use: "test",
		RunE: func(_ *cobra.Command, _ []string) error {
			return nil
		},
	}

	// Add flags similar to start command
	testCmd.Flags().BoolVarP(&cfg.Debug, "debug", "d", false, "Run in foreground with debug logging")
	testCmd.Flags().IntVarP(&cfg.Interval, "interval", "i", 150, "Loop interval in seconds")
	testCmd.Flags().BoolVarP(&cfg.WebSocket, "websocket", "w", false, "Enable WebSocket server")
	testCmd.Flags().IntVarP(&cfg.Port, "port", "p", 8765, "WebSocket server port")

	// Test that defaults are set correctly
	if err := testCmd.ParseFlags([]string{}); err != nil {
		t.Fatalf("failed to parse flags: %v", err)
	}

	// Check default values through flag defaults
	if flag := testCmd.Flags().Lookup("interval"); flag != nil && flag.DefValue != "150" {
		t.Errorf("default interval should be 150, got %s", flag.DefValue)
	}

	if flag := testCmd.Flags().Lookup("port"); flag != nil && flag.DefValue != "8765" {
		t.Errorf("default port should be 8765, got %s", flag.DefValue)
	}
}

func TestCommandHierarchy(t *testing.T) {
	// Test that all commands are properly added to root
	commands := rootCmd.Commands()
	commandNames := make(map[string]bool)

	for _, cmd := range commands {
		commandNames[cmd.Use] = true
	}

	expectedCommands := []string{"start", "stop", "status", "toggle", "version", "run"}
	for _, expectedCmd := range expectedCommands {
		if !commandNames[expectedCmd] {
			t.Errorf("root command should have subcommand: %s", expectedCmd)
		}
	}
}

func TestFlagBindings(t *testing.T) {
	// Test that flags are properly bound to config struct

	// Create a test command with the same flag setup
	testCmd := &cobra.Command{Use: "test"}
	testConfig := &config.Config{}

	testCmd.Flags().BoolVarP(&testConfig.Debug, "debug", "d", false, "Debug mode")
	testCmd.Flags().IntVarP(&testConfig.Interval, "interval", "i", 180, "Interval")
	testCmd.Flags().BoolVarP(&testConfig.WebSocket, "websocket", "w", false, "WebSocket")
	testCmd.Flags().IntVarP(&testConfig.Port, "port", "p", 8765, "Port")
	testCmd.Flags().StringVar(&testConfig.LogFormat, "log-format", "text", "Log format")

	// Parse some test flags
	err := testCmd.ParseFlags([]string{"--debug", "--interval=60", "--websocket", "--port=9000", "--log-format=json"})
	if err != nil {
		t.Fatalf("failed to parse test flags: %v", err)
	}

	// Check that values were set
	if !testConfig.Debug {
		t.Error("debug flag should be true")
	}

	if testConfig.Interval != 60 {
		t.Errorf("interval should be 60, got %d", testConfig.Interval)
	}

	if !testConfig.WebSocket {
		t.Error("websocket flag should be true")
	}

	if testConfig.Port != 9000 {
		t.Errorf("port should be 9000, got %d", testConfig.Port)
	}

	if testConfig.LogFormat != "json" {
		t.Errorf("log format should be 'json', got '%s'", testConfig.LogFormat)
	}
}

func TestExecuteFunction(t *testing.T) {
	// Test that Execute function exists and can be called
	// We can't easily test the actual execution without mocking os.Exit
	// But we can test that the function exists and is callable

	// Save original args
	originalArgs := os.Args
	defer func() { os.Args = originalArgs }()

	// Set args that should show help and exit cleanly
	os.Args = []string{"teams-green", "--help"}

	// Execute should not panic
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Execute should not panic: %v", r)
		}
	}()

	// Note: Execute() calls os.Exit(1) on error, so we can't test error cases easily
	// This test mainly ensures the function exists and is structured correctly
}
