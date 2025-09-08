# Teams-Green 🟢

Keep that Teams green.

## Overview

Teams-Green runs in the background and periodically sends a key (F15) to keep your Teams status from going idle. It includes optional WebSocket support for real-time status monitoring and control.

## Features

- **Background Service**: Runs in a background process
- **Configurable Intervals**: Set custom activity intervals (default: 180 seconds)
- **Advanced Timing Control**: Fine-tune focus delays and key processing timing
- **Input Safety**: Automatic detection and deferral when user is actively typing or using the mouse
- **Enhanced Focus Validation**: Multiple safety checks to prevent key leakage to wrong windows
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

# Configure timing delays for reliability (milliseconds)
teams-green start --focus-delay 30 --key-process-delay 150

# Conservative timing to prevent pending notifications
teams-green start --focus-delay 25 --restore-delay 20 --key-process-delay 200

# Fast timing for performance (if Teams responds quickly)
teams-green start --focus-delay 10 --key-process-delay 75

# Enable logging to file with rotation
teams-green start --log-file logs/teams-green.log --log-rotate

# Use JSON logging format
teams-green start --log-format json --log-file logs/teams-green.log

# Combine options for troubleshooting
teams-green start --debug --focus-delay 50 --key-process-delay 150 --log-file debug.log
```

### WebSocket API

When WebSocket is enabled, connect to `ws://127.0.0.1:8765/ws` to receive real-time events:

```json
{
  "service": "teams-green",
  "status": "running",
  "pid": 1234,
  "message": "Service started",
  "timestamp": "2025-01-20T10:30:00Z",
  "type": "status"
}
```

#### Connection Stability Features

- **Keep-Alive Messages**: Automatic keep-alive messages every 45 seconds to maintain connection
- **Ping/Pong Support**: Send `{"type": "ping"}` to test connection health
- **Extended Timeouts**: 2-minute read timeout and 5-minute idle timeout for better stability
- **Graceful Error Handling**: Timeout errors logged at debug level to reduce noise

#### Message Types

- `status` - Service state changes
- `keepalive` - Periodic keep-alive messages
- `ping` - Connection health check request
- `pong` - Response to ping messages

## Configuration

The service accepts the following flags:

| Flag                  | Short | Default | Description                                       |
|-----------------------|-------|---------|---------------------------------------------------|
| `--debug`             | `-d`  | `false` | Run in foreground with debug logging              |
| `--interval`          | `-i`  | `180`   | Activity interval in seconds                      |
| `--websocket`         | `-w`  | `false` | Enable WebSocket server                           |
| `--port`              | `-p`  | `8765`  | WebSocket server port                             |
| `--focus-delay`       |       | `150`    | Delay after setting focus before sending key (ms) |
| `--restore-delay`     |       | `100`    | Delay after restoring minimized window (ms)       |
| `--key-process-delay` |       | `150`   | Delay before restoring original focus (ms)        |
| `--log-format`        |       | `text`  | Log format: text or json                          |
| `--log-file`          |       | ``      | Log file path (empty = no file logging)           |
| `--log-rotate`        |       | `falsmse` | Enable log rotation                               |
| `--max-log-size`      |       | `10`    | Maximum log file size in MB                       |
| `--max-log-age`       |       | `30`    | Maximum log file age in days                      |

### Timing Configuration

The timing delays control how the service interacts with Teams windows:

- **`--focus-delay`**: Time to wait after focusing a Teams window before sending the key. Increase if Teams needs more time to process focus changes.
- **`--restore-delay`**: Time to wait after restoring a minimized Teams window. Increase if Windows is slow to restore windows.
- **`--key-process-delay`**: Time to wait before restoring focus to the original window. **Most important for preventing pending notifications** - increase to 150-200ms if Teams shows pending items.

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
├── .golangci.yml       # Linting configuration
├── .goreleaser.yaml    # Release configuration
└── main.go             # Application entry point
```

## How It Works

Teams-Green works by:

1. **Process Detection**: Locates running Microsoft Teams processes
2. **Smart Activity Detection**: Monitors user activity (keyboard, mouse, application focus) and intelligently resets the activity timer when user interaction is detected
3. **Efficient Resource Usage**: Throttles Windows API calls to avoid system slowdown while maintaining responsiveness
4. **Window Targeting**: Finds and focuses Teams windows with enhanced focus validation
5. **Smart Key Simulation**: Sends F15 keys with configurable timing to prevent pending notifications
6. **Focus Protection**: Multiple validation checks prevent keys from going to wrong windows
7. **Background Operation**: Runs as a detached process with PID file management
8. **Status Monitoring**: Tracks service state and provides real-time feedback

### Key Safety Features

- **Comprehensive Input Detection**: Automatically detects and defers Teams operations when user is actively using keyboard, mouse, or other applications
- **Activity-Based Timer Reset**: When user activity is detected, the Teams activity interval is reset, ensuring natural user behavior takes precedence
- **Throttled API Monitoring**: Efficiently monitors user activity with minimal system impact (maximum once per second API calls)
- **Enhanced Focus Validation**: Double-checks window focus before and after key sending
- **Configurable Timing**: Adjustable delays to work with different system performance levels
- **Post-Send Verification**: Confirms focus state after key operations to detect interference

## Troubleshooting

### Service Won't Start
- Check if Teams is running
- Ensure no other instance is already running: `teams-green status`
- Try running in debug mode: `teams-green start --debug`

### Teams Still Goes Idle
- Reduce the interval: `teams-green start --interval 60`
- Check Windows focus policies and permissions
- Ensure Teams has proper window focus

### Teams Shows Pending Notifications After Key Send
- **Most Common Issue**: Increase key processing delay: `teams-green start --key-process-delay 150`
- Try conservative timing: `teams-green start --focus-delay 30 --key-process-delay 200`
- Run in debug mode to monitor timing: `teams-green start --debug --key-process-delay 150`

### Keys Going to Wrong Applications
- The service includes multiple safety checks to prevent this
- Enhanced input detection now monitors mouse activity in addition to keyboard input
- Activity detection automatically resets the interval timer when user interaction is detected
- Run with debug logging to see protection mechanisms in action

### Performance Issues
- Use faster timing for responsive systems: `teams-green start --focus-delay 10 --key-process-delay 75`
- Increase delays for slower systems: `teams-green start --focus-delay 50 --key-process-delay 200`

### Activity Detection Behavior
- **Mouse Activity**: Clicking, dragging, or using any mouse buttons will reset the Teams activity timer
- **Keyboard Activity**: Pressing modifier keys (Shift, Ctrl, Alt) will reset the timer
- **Application Focus**: Actively using other applications will defer Teams activity
- **Efficient Monitoring**: Activity is checked at most once per second to minimize system impact
- **Smart Timer Reset**: When activity is detected, the full interval timer is reset (e.g., if interval is 180s, timer resets to 180s from current time)

### WebSocket Connection Issues
- **Frequent Disconnections**: The improved WebSocket implementation includes keep-alive messages and extended timeouts to reduce disconnections
- **Timeout Errors**: These are now logged at debug level - use `--debug` flag to see detailed connection information
- Verify the port is not in use: `netstat -an | findstr 8765`
- Try a different port: `teams-green start --websocket --port 9000`
- Check Windows Firewall settings

### Connection Health Monitoring
- WebSocket connections send keep-alive messages every 45 seconds
- Clients can send ping messages to test connection health
- Extended timeouts (2 minutes read, 5 minutes idle) improve stability
- Connection limits: Maximum 50 concurrent connections

### Fine-Tuning Timing

**If Teams shows pending notifications:**
```bash
# Start with conservative delays
teams-green start --key-process-delay 200

# Gradually reduce if working well
teams-green start --key-process-delay 150
```

**If Teams doesn't register the activity:**
```bash
# Increase focus delay to ensure Teams processes the focus change
teams-green start --focus-delay 50 --key-process-delay 150
```

**For optimal performance:**
```bash
# Test different combinations based on your system
teams-green start --debug --focus-delay 25 --key-process-delay 125
```

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## Disclaimer

Use this tool at your own risk. The author is not responsible for any misuse or damage caused by this software. Always ensure compliance with your organization's IT policies.

