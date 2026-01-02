package service

import (
	"log/slog"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows API declarations for input detection
var (
	user32InputAPI       = windows.NewLazySystemDLL("user32.dll")
	kernel32InputAPI     = windows.NewLazySystemDLL("kernel32.dll")
	procGetLastInputInfo = user32InputAPI.NewProc("GetLastInputInfo")
	procGetTickCount     = kernel32InputAPI.NewProc("GetTickCount")
)

// LASTINPUTINFO contains the time of the last input event
type LASTINPUTINFO struct {
	cbSize uint32
	dwTime uint32
}

// InputDetector interface for user input detection
type InputDetector interface {
	IsUserInputActive() bool
	GetTimeSinceLastInput() time.Duration
}

// inputDetector implementation
type inputDetector struct {
	threshold time.Duration
}

// NewInputDetector creates a new input detector with default threshold (2000ms)
func NewInputDetector() InputDetector {
	return &inputDetector{
		threshold: 2000 * time.Millisecond, // Default threshold
	}
}

// NewInputDetectorWithThreshold creates a new input detector with custom threshold
func NewInputDetectorWithThreshold(threshold time.Duration) InputDetector {
	return &inputDetector{
		threshold: threshold,
	}
}

// GetTimeSinceLastInput returns the duration since the last keyboard, mouse, or touch input event.
// This uses the Windows GetLastInputInfo API which tracks all user input activity including:
// - All keyboard input (any key press/release)
// - All mouse activity (movement, clicks, scroll wheel)
// - Touch input (on supported devices)
// - Pen input (on supported devices)
func (id *inputDetector) GetTimeSinceLastInput() time.Duration {
	var lii LASTINPUTINFO
	lii.cbSize = uint32(unsafe.Sizeof(lii))

	ret, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&lii)))
	if ret == 0 {
		// API call failed - assume no recent input for safety
		slog.Debug("GetLastInputInfo failed, assuming no recent input")
		return time.Hour
	}

	currentTick, _, _ := procGetTickCount.Call()

	// Handle tick count wraparound (occurs every ~49.7 days of system uptime)
	// Use 64-bit arithmetic to prevent overflow when adding two 32-bit values
	var timeSinceInput uint64
	if currentTick >= uintptr(lii.dwTime) {
		timeSinceInput = uint64(currentTick) - uint64(lii.dwTime)
	} else {
		// Wraparound occurred: calculate elapsed time across the boundary
		timeSinceInput = (uint64(0xFFFFFFFF) - uint64(lii.dwTime)) + uint64(currentTick)
	}

	return time.Duration(timeSinceInput) * time.Millisecond
}

// IsUserInputActive checks if any user input (keyboard, mouse, touch) occurred recently.
// Returns true if input was detected within the configured threshold, indicating active user interaction.
func (id *inputDetector) IsUserInputActive() bool {
	timeSince := id.GetTimeSinceLastInput()

	if timeSince < id.threshold {
		slog.Debug("Recent user input detected",
			slog.Duration("time_since_input", timeSince),
			slog.Duration("threshold", id.threshold))
		return true
	}

	return false
}
