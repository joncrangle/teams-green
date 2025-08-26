package cmd

import (
	"fmt"
	"os"

	"github.com/joncrangle/teams-green/internal/config"
	"github.com/joncrangle/teams-green/internal/service"

	"github.com/spf13/cobra"
)

var cfg = &config.Config{}

var rootCmd = &cobra.Command{
	Use:   "teams-green",
	Short: "Keep Microsoft Teams status active",
	Long: `Teams Green Service keeps your Microsoft Teams status active by sending 
periodic key combinations to prevent the status from going idle.`,
	Version: "1.0.0",
}

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the Teams Green service",
	Long:  "Start the Teams Green service in the background to keep Teams status active",
	RunE: func(_ *cobra.Command, _ []string) error {
		if cfg.Debug {
			fmt.Println("🔧 Starting service in debug mode (foreground)")
		}

		if err := service.Start(cfg); err != nil {
			return fmt.Errorf("❌ %v", err)
		}

		if !cfg.Debug {
			if cfg.WebSocket {
				fmt.Printf("🌐 WebSocket server available at: ws://127.0.0.1:%d/ws\n", cfg.Port)
			}
			fmt.Println("✅ Service started successfully")
		}
		return nil
	},
}

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the Teams Green service",
	Long:  "Stop the running Teams Green service",
	RunE: func(_ *cobra.Command, _ []string) error {
		if err := service.Stop(); err != nil {
			return fmt.Errorf("❌ %v", err)
		}
		return nil
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check the status of the Teams Green service",
	Long:  "Display the current status of the Teams Green service",
	RunE: func(_ *cobra.Command, _ []string) error {
		running, pid, err := service.GetStatus()
		if err != nil {
			return fmt.Errorf("❌ %v", err)
		}

		if running {
			fmt.Printf("✅ Service running (PID %d)\n", pid)
		} else {
			fmt.Println("❌ Service not running")
		}
		return nil
	},
}

var toggleCmd = &cobra.Command{
	Use:   "toggle",
	Short: "Toggle the Teams Green service",
	Long:  "Start the service if it's not running, or stop it if it's currently running",
	RunE: func(_ *cobra.Command, _ []string) error {
		running, _, err := service.GetStatus()
		if err != nil {
			return fmt.Errorf("❌ %v", err)
		}

		if running {
			return service.Stop()
		}
		if cfg.Debug {
			fmt.Println("🔧 Starting service in debug mode (foreground)")
		}

		if err := service.Start(cfg); err != nil {
			return fmt.Errorf("❌ %v", err)
		}

		if !cfg.Debug {
			if cfg.WebSocket {
				fmt.Printf("🌐 WebSocket server available at: ws://127.0.0.1:%d/ws\n", cfg.Port)
			}
			fmt.Println("✅ Service started successfully")
		}
		return nil
	},
}

var runCmd = &cobra.Command{
	Use:    "run",
	Short:  "Internal command to run the service",
	Hidden: true,
	RunE: func(_ *cobra.Command, _ []string) error {
		return service.Run(cfg)
	},
}

func init() {
	// Add flags to commands
	startCmd.Flags().BoolVarP(&cfg.Debug, "debug", "d", false, "Run in foreground with debug logging")
	startCmd.Flags().IntVarP(&cfg.Interval, "interval", "i", 180, "Loop interval in seconds")
	startCmd.Flags().BoolVarP(&cfg.WebSocket, "websocket", "w", false, "Enable WebSocket server")
	startCmd.Flags().IntVarP(&cfg.Port, "port", "p", 8765, "WebSocket server port")

	toggleCmd.Flags().BoolVarP(&cfg.Debug, "debug", "d", false, "Run in foreground with debug logging")
	toggleCmd.Flags().IntVarP(&cfg.Interval, "interval", "i", 180, "Loop interval in seconds")
	toggleCmd.Flags().BoolVarP(&cfg.WebSocket, "websocket", "w", false, "Enable WebSocket server")
	toggleCmd.Flags().IntVarP(&cfg.Port, "port", "p", 8765, "WebSocket server port")

	// Hidden flags for run command
	runCmd.Flags().BoolVar(&cfg.Debug, "debug", false, "Debug mode")
	runCmd.Flags().IntVar(&cfg.Interval, "interval", 180, "Loop interval in seconds")
	runCmd.Flags().BoolVar(&cfg.WebSocket, "websocket", false, "Enable WebSocket server")
	runCmd.Flags().IntVar(&cfg.Port, "port", 8765, "WebSocket server port")

	// Add commands to root
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(toggleCmd)
	rootCmd.AddCommand(runCmd)
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
