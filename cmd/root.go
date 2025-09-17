// Package cmd implements the command-line interface for the teams-green application.
// It provides commands for starting, stopping, and monitoring the Teams-Green service.
package cmd

import (
	"fmt"
	"os"

	"github.com/joncrangle/teams-green/internal/config"
	"github.com/joncrangle/teams-green/internal/service"

	"github.com/spf13/cobra"
)

var cfg = &config.Config{}

// CLI constants
const (
	defaultIntervalSeconds = 180
	defaultWebSocketPort   = 8765
	defaultLogMaxSizeMB    = 10
	defaultLogMaxAgeDays   = 30
	defaultFocusDelayMs    = 150
	defaultRestoreDelayMs  = 100
	defaultKeyDelayMs      = 150
)

var rootCmd = &cobra.Command{
	Use:   "teams-green",
	Short: "Keep that Teams status green",
	Long: `Teams-Green keeps your Microsoft Teams status active by sending 
periodic keys to prevent the status from going idle.`,
	Version: "0.4.1",
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show version information",
	Long:  "Display version information for teams-green",
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Printf("teams-green version %s\n", rootCmd.Version)
		fmt.Println("Keep that Teams green")
		fmt.Println("https://github.com/joncrangle/teams-green")
	},
}

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start Teams-Green",
	Long:  "Start Teams-Green in the background to keep Teams status active",
	RunE: func(_ *cobra.Command, _ []string) error {
		if err := cfg.Validate(); err != nil {
			return err
		}

		if cfg.Debug {
			fmt.Println("🔧 Starting service in debug mode (foreground)")
		}

		if err := service.Start(cfg); err != nil {
			return fmt.Errorf("❌ %v", err)
		}

		if !cfg.Debug {
			if cfg.WebSocket {
				fmt.Printf("🌐 WebSocket server available at: ws://127.0.0.1:%d\n", cfg.Port)
			}
			fmt.Println("✅ Service started successfully")
		}
		return nil
	},
}

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop Teams-Green process",
	Long:  "Stop the running Teams-Green process",
	RunE: func(_ *cobra.Command, _ []string) error {
		if err := service.Stop(); err != nil {
			return fmt.Errorf("❌ %v", err)
		}
		return nil
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check the status of Teams-Green",
	Long:  "Display the current status of Teams-Green with detailed activity information",
	RunE: func(_ *cobra.Command, _ []string) error {
		running, pid, info, err := service.GetEnhancedStatus()
		if err != nil {
			return fmt.Errorf("❌ %v", err)
		}

		if running {
			fmt.Printf("✅ Service running (PID %d)\n", pid)
			if info != nil {
				fmt.Printf("   📊 Teams windows: %d\n", info.TeamsWindowCount)
				if info.FailureStreak > 0 {
					fmt.Printf("   ⚠️  Failure streak: %d\n", info.FailureStreak)
				}
			}
		} else {
			fmt.Println("❌ Service not running")
		}
		return nil
	},
}

var toggleCmd = &cobra.Command{
	Use:   "toggle",
	Short: "Toggle Teams-Green",
	Long:  "Start Teams-Green if it's not running, or stop it if it's currently running",
	RunE: func(_ *cobra.Command, _ []string) error {
		running, _, _, err := service.GetEnhancedStatus()
		if err != nil {
			return fmt.Errorf("❌ %v", err)
		}

		if running {
			return service.Stop()
		}

		if err := cfg.Validate(); err != nil {
			return err
		}

		if cfg.Debug {
			fmt.Println("🔧 Starting Teams-Green in debug mode (foreground)")
		}

		if err := service.Start(cfg); err != nil {
			return fmt.Errorf("❌ %v", err)
		}

		if !cfg.Debug {
			if cfg.WebSocket {
				fmt.Printf("🌐 WebSocket server available at: ws://127.0.0.1:%d\n", cfg.Port)
			}
			fmt.Println("✅ Teams-Green started successfully")
		}
		return nil
	},
}

var runCmd = &cobra.Command{
	Use:    "run",
	Short:  "Internal command to run Teams-Green",
	Hidden: true,
	RunE: func(_ *cobra.Command, _ []string) error {
		svc := service.NewService(cfg)
		return svc.Run()
	},
}

// addConfigFlags adds all configuration flags to a command.
// includeShortcuts determines whether to include short flag aliases for common options.
func addConfigFlags(cmd *cobra.Command, includeShortcuts bool) {
	if includeShortcuts {
		cmd.Flags().BoolVarP(&cfg.Debug, "debug", "d", false, "Run in foreground with debug logging")
		cmd.Flags().IntVarP(&cfg.Interval, "interval", "i", defaultIntervalSeconds, "Loop interval in seconds")
		cmd.Flags().BoolVarP(&cfg.WebSocket, "websocket", "w", false, "Enable WebSocket server")
		cmd.Flags().IntVarP(&cfg.Port, "port", "p", defaultWebSocketPort, "WebSocket server port")
	} else {
		cmd.Flags().BoolVar(&cfg.Debug, "debug", false, "Debug mode")
		cmd.Flags().IntVar(&cfg.Interval, "interval", defaultIntervalSeconds, "Loop interval in seconds")
		cmd.Flags().BoolVar(&cfg.WebSocket, "websocket", false, "Enable WebSocket server")
		cmd.Flags().IntVar(&cfg.Port, "port", defaultWebSocketPort, "WebSocket server port")
	}

	// Common flags for all commands
	cmd.Flags().StringVar(&cfg.LogFormat, "log-format", "text", "Log format (text or json)")
	cmd.Flags().StringVar(&cfg.LogFile, "log-file", "", "Log file path (empty for no file logging)")
	cmd.Flags().BoolVar(&cfg.LogRotate, "log-rotate", false, "Enable log file rotation")
	cmd.Flags().IntVar(&cfg.MaxLogSize, "max-log-size", defaultLogMaxSizeMB, "Maximum log file size in MB")
	cmd.Flags().IntVar(&cfg.MaxLogAge, "max-log-age", defaultLogMaxAgeDays, "Maximum log file age in days")
	cmd.Flags().IntVar(&cfg.FocusDelayMs, "focus-delay", defaultFocusDelayMs, "Delay after setting focus before sending key (milliseconds)")
	cmd.Flags().IntVar(&cfg.RestoreDelayMs, "restore-delay", defaultRestoreDelayMs, "Delay after restoring minimized window (milliseconds)")
	cmd.Flags().IntVar(&cfg.KeyProcessDelayMs, "key-process-delay", defaultKeyDelayMs, "Delay before restoring original focus (milliseconds)")
	cmd.Flags().StringVar(&cfg.ActivityMode, "activity-mode", "focus", "Activity mode: 'focus' (bring Teams forward) or 'global' (send key without focus change)")
}

func init() {
	// Add flags to commands using helper function
	addConfigFlags(startCmd, true)  // Include shortcuts for user-facing commands
	addConfigFlags(toggleCmd, true) // Include shortcuts for user-facing commands
	addConfigFlags(runCmd, false)   // No shortcuts for internal command

	// Add commands to root
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(toggleCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(runCmd)
}

// Execute runs the root command and handles any errors by exiting with code 1.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
