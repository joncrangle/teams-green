# Teams-Green 🟢

Keep that Teams green.

## Overview

Teams-Green runs in the background and periodically sends a key (F15) to keep your Teams status from going idle. It includes optional WebSocket support for real-time status monitoring and control.

## Features

- **Background Service**: Runs in a background process
- **Configurable Intervals**: Set custom activity intervals (default: 180 seconds)
- **WebSocket Server**: Optional real-time monitoring and control via WebSocket API
- **Simple CLI**: Easy start/stop/status/toggle commands
- **Debug Mode**: Foreground execution with detailed logging
- **Process Management**: Automatic PID tracking and cleanup

## Installation

### From Release

1. Download the latest release from the [releases page](https://github.com/joncrangle/teams-green/releases)
2. Extract the ZIP file to your desired location
3. Run `teams-green.exe` from Command Prompt or PowerShell

### Go Install

```bash
go install github.com/joncrangle/teams-green@latest
```

### Build From Source

```bash
git clone https://github.com/joncrangle/teams-green
cd teams-green
just build
```

## Usage

### Basic Commands

```bash
# Start the service
teams-green start

# Stop the service
teams-green stop

# Check service status
teams-green status

# Toggle service on/off
teams-green toggle
```

### Advanced Options

```bash
# Start with custom interval (seconds)
teams-green start --interval 120

# Start in debug mode (foreground)
teams-green start --debug

# Start with WebSocket server
teams-green start --websocket --port 8765

# Enable logging to file with rotation
teams-green start --log-file logs/teams-green.log --log-rotate

# Use JSON logging format
teams-green start --log-format json --log-file logs/teams-green.log

# Combine options
teams-green start --debug --websocket --interval 60 --log-file logs/debug.log
```

### WebSocket API

When WebSocket is enabled, connect to `ws://127.0.0.1:8765/ws` to receive real-time events:

```json
{
  "service": "teams-green",
  "status": "running",
  "pid": 1234,
  "message": "Service started",
  "timestamp": "2025-01-20T10:30:00Z"
}
```

## Configuration

The service accepts the following flags:

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--debug` | `-d` | `false` | Run in foreground with debug logging |
| `--interval` | `-i` | `180` | Activity interval in seconds |
| `--websocket` | `-w` | `false` | Enable WebSocket server |
| `--port` | `-p` | `8765` | WebSocket server port |
| `--log-format` | | `text` | Log format: text or json |
| `--log-file` | | `` | Log file path (empty = no file logging) |
| `--log-rotate` | | `false` | Enable log rotation |
| `--max-log-size` | | `10` | Maximum log file size in MB |
| `--max-log-age` | | `30` | Maximum log file age in days |

## Development

### Requirements

- Go 1.25 or later
- Windows OS (uses Win32 APIs)
- Just (for task management)

```bash
# See Justfile recipes
just
```

### Project Structure

```
teams-green/
├── cmd/                # CLI commands and root configuration
├── internal/
│   ├── config/         # Configuration and logging setup
│   ├── service/        # Core service logic and Windows integration
│   └── websocket/      # WebSocket server and broadcasting
├── .goreleaser.yaml    # Release configuration
└── main.go             # Application entry point
```

## How It Works

Teams-Green works by:

1. **Process Detection**: Locates running Microsoft Teams processes
2. **Window Targeting**: Finds and focuses Teams windows when needed
3. **Key Simulation**: Sends keys (F15) that doesn't disrupt usage
4. **Background Operation**: Runs as a detached process with PID file management
5. **Status Monitoring**: Tracks service state and provides real-time feedback

## Troubleshooting

### Service Won't Start
- Check if Teams is running
- Ensure no other instance is already running: `teams-green status`
- Try running in debug mode: `teams-green start --debug`

### Teams Still Goes Idle
- Reduce the interval: `teams-green start --interval 60`
- Check Windows focus policies and permissions
- Ensure Teams has proper window focus

### WebSocket Connection Issues
- Verify the port is not in use: `netstat -an | findstr 8765`
- Try a different port: `teams-green start --websocket --port 9000`
- Check Windows Firewall settings

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## Disclaimer

Use this tool at your own risk. The author is not responsible for any misuse or damage caused by this software. Always ensure compliance with your organization's IT policies.

