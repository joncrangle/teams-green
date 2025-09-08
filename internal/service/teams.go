package service

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/lxn/win"
	"golang.org/x/sys/windows"

	"github.com/joncrangle/teams-green/internal/websocket"
)

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procKeyboardEvent            = user32.NewProc("keybd_event")
	procIsIconic                 = user32.NewProc("IsIconic")
	procIsZoomed                 = user32.NewProc("IsZoomed")
	procShowWindow               = user32.NewProc("ShowWindow")
	procGetAsyncKeyState         = user32.NewProc("GetAsyncKeyState")
)

// ErrUserInputActive is returned when user input is detected during Teams activity
var ErrUserInputActive = errors.New("user input active, deferring Teams activity")

type WindowEnumContext struct {
	hwnds            *[]win.HWND
	teamsExecutables []string
	logger           *slog.Logger
}

var teamsExecutables []string

func init() {
	teamsExecutables = []string{
		"ms-teams.exe",
		"teams.exe",
		"msteams.exe",
	}
}

func getTeamsExecutables() []string {
	return teamsExecutables
}

func enumWindowsProc(hwnd syscall.Handle, lParam uintptr) uintptr {
	// Validate parameters
	if hwnd == 0 || lParam == 0 {
		return 1
	}

	// Safe conversion from uintptr to unsafe.Pointer for Windows callback
	// This is safe because lParam comes directly from Windows EnumWindows callback
	// and we've validated it's non-zero above
	//nolint:unsafeptr // Windows callback pattern requires unsafe pointer conversion
	ctx := (*WindowEnumContext)(unsafe.Pointer(lParam))

	// Safety checks after unsafe conversion
	if ctx == nil {
		return 1
	}

	// Validate all required context fields before use
	if ctx.hwnds == nil || ctx.teamsExecutables == nil || ctx.logger == nil {
		return 1
	}

	hwnds := ctx.hwnds

	// Skip invisible windows
	ret, _, _ := procIsWindowVisible.Call(uintptr(hwnd))
	if ret == 0 {
		return 1
	}

	var pid uint32
	_, _, _ = procGetWindowThreadProcessID.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&pid)))

	// Validate PID
	if pid == 0 {
		return 1
	}

	hProcess, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil || hProcess == 0 {
		return 1
	}

	// Ensure handle is always closed, even if function returns early
	defer func() {
		if closeErr := windows.CloseHandle(hProcess); closeErr != nil {
			ctx.logger.Debug("Failed to close process handle",
				slog.String("error", closeErr.Error()),
				slog.Uint64("pid", uint64(pid)),
				slog.Uint64("handle", uint64(uintptr(hProcess))))
		}
	}()

	var exeName [windows.MAX_PATH]uint16
	size := uint32(len(exeName))
	if err := windows.QueryFullProcessImageName(hProcess, 0, &exeName[0], &size); err != nil {
		ctx.logger.Debug("Failed to get process image name",
			slog.Uint64("pid", uint64(pid)),
			slog.String("error", err.Error()))
		return 1
	}

	// Convert to string only once and extract base name efficiently
	exePath := windows.UTF16ToString(exeName[:size])
	lastSlash := strings.LastIndexByte(exePath, '\\')
	var exeBase string
	if lastSlash != -1 {
		exeBase = strings.ToLower(exePath[lastSlash+1:])
	} else {
		exeBase = strings.ToLower(exePath)
	}

	// Check if this is a Teams executable using context's list
	if slices.Contains(ctx.teamsExecutables, exeBase) {
		*hwnds = append(*hwnds, win.HWND(hwnd))
	}

	return 1
}

func (tm *TeamsManager) FindTeamsWindows() []win.HWND {
	var hwnds []win.HWND
	tm.enumContext.hwnds = &hwnds

	ret, _, err := procEnumWindows.Call(
		syscall.NewCallback(enumWindowsProc),
		uintptr(unsafe.Pointer(tm.enumContext)),
	)
	if ret == 0 {
		tm.logger.Debug("EnumWindows failed", slog.String("error", err.Error()))
	}

	return hwnds
}

func isKeyPressed() bool {
	// Simplified check for just modifier keys to minimize API calls
	modifierKeys := []uintptr{
		0x10, // VK_SHIFT
		0x11, // VK_CONTROL
		0x12, // VK_MENU (Alt)
	}

	for _, key := range modifierKeys {
		ret, _, _ := procGetAsyncKeyState.Call(key)
		if ret&0x8000 != 0 {
			return true
		}
	}

	return false
}

func (tm *TeamsManager) SendKeysToTeams(state *websocket.ServiceState) error {
	// Check for user input activity first - real-time keyboard detection only
	if isKeyPressed() {
		tm.logger.Debug("User input detected, deferring Teams key send to avoid interference")
		return ErrUserInputActive
	}

	hwnds := tm.FindTeamsWindows()
	if len(hwnds) == 0 {
		return tm.handleTeamsNotFound(state)
	}

	// Reset failure count on success
	tm.retryState.FailureCount = 0

	// Get configurable delays
	focusDelay := tm.config.GetFocusDelay()
	restoreDelay := tm.config.GetRestoreDelay()
	keyProcessDelay := tm.config.GetKeyProcessDelay()

	// Store the currently focused window to restore it later
	currentWindow, _, _ := procGetForegroundWindow.Call()

	const (
		vkF15            = 0x7E // F15 key
		keyeventfKeydown = 0
		keyeventfKeyup   = 2
		swRestore        = 9
		swShowMinimized  = 2
		swShowMaximized  = 3
		swShowNormal     = 1
	)

	successCount := 0
	var lastError error

	for _, hWnd := range hwnds {

		// Batch window state queries to reduce syscalls
		isMinimized, _, _ := procIsIconic.Call(uintptr(hWnd))
		isMaximized, _, _ := procIsZoomed.Call(uintptr(hWnd))

		// Check if this Teams window already has focus
		currentFocus, _, _ := procGetForegroundWindow.Call()
		if currentFocus == uintptr(hWnd) {
			// Teams window already has focus, safe to send key
			if err := sendKeyToWindow(hWnd, vkF15); err != nil {
				tm.logger.Debug("Failed to send F15 key to focused window",
					slog.String("error", err.Error()),
					slog.Uint64("hwnd", uint64(uintptr(hWnd))))
				lastError = err
			} else {
				successCount++
				tm.logger.Debug("Sent F15 to already focused Teams window",
					slog.Uint64("hwnd", uint64(uintptr(hWnd))))
			}
			continue
		}

		// Only restore if minimized (performance optimization)
		originalState := swShowNormal
		if isMinimized != 0 {
			originalState = swShowMinimized
			if ret, _, err := procShowWindow.Call(uintptr(hWnd), swRestore); ret == 0 {
				tm.logger.Debug("Failed to restore minimized window",
					slog.String("error", err.Error()),
					slog.Uint64("hwnd", uint64(uintptr(hWnd))))
				lastError = err
				continue
			}
			time.Sleep(restoreDelay)
		} else if isMaximized != 0 {
			originalState = swShowMaximized
		}

		// Focus with retry logic
		const (
			maxRetries = 3
			retryDelay = 25 * time.Millisecond
		)

		focusSuccess := false
		var focusError error

		for attempt := range maxRetries {
			if ret, _, err := procSetForegroundWindow.Call(uintptr(hWnd)); ret != 0 {
				time.Sleep(focusDelay)

				// Validate focus was actually set
				focusedWindow, _, _ := procGetForegroundWindow.Call()
				if focusedWindow == uintptr(hWnd) {
					focusSuccess = true
					tm.logger.Debug("Focus set successfully",
						slog.Uint64("hwnd", uint64(uintptr(hWnd))),
						slog.Int("attempt", attempt+1))
					break
				}

				// Focus validation failed
				if attempt < maxRetries-1 {
					tm.logger.Debug("Focus validation failed, retrying",
						slog.Uint64("expected_hwnd", uint64(uintptr(hWnd))),
						slog.Uint64("actual_focused", uint64(focusedWindow)),
						slog.Int("attempt", attempt+1),
						slog.Int("max_retries", maxRetries))
					time.Sleep(retryDelay)
					continue
				}
				focusError = fmt.Errorf("focus validation failed after %d attempts", maxRetries)
			} else {
				focusError = fmt.Errorf("SetForegroundWindow failed: %v", err)
				if attempt < maxRetries-1 {
					tm.logger.Debug("SetForegroundWindow failed, retrying",
						slog.String("error", err.Error()),
						slog.Uint64("hwnd", uint64(uintptr(hWnd))),
						slog.Int("attempt", attempt+1))
					time.Sleep(retryDelay)
					continue
				}
			}
		}

		if focusSuccess {
			// Send key to focused window
			if err := sendKeyToWindow(hWnd, vkF15); err != nil {
				tm.logger.Debug("Failed to send F15 key",
					slog.String("error", err.Error()),
					slog.Uint64("hwnd", uint64(uintptr(hWnd))))
				lastError = err
			} else {
				successCount++
				tm.logger.Debug("Sent F15 to newly focused Teams window",
					slog.Uint64("hwnd", uint64(uintptr(hWnd))))

				// Post-send validation - verify focus didn't change unexpectedly
				time.Sleep(5 * time.Millisecond)
				if verifyWindow, _, _ := procGetForegroundWindow.Call(); verifyWindow != uintptr(hWnd) {
					tm.logger.Debug("Focus changed after key send - possible interference",
						slog.Uint64("expected", uint64(uintptr(hWnd))),
						slog.Uint64("actual", uint64(verifyWindow)))
				}
			}
		} else {
			tm.logger.Debug("Failed to set focus after retries",
				slog.String("error", focusError.Error()),
				slog.Uint64("hwnd", uint64(uintptr(hWnd))))
			lastError = focusError
		}

		// Restore original window state if needed
		if originalState != swShowNormal {
			if ret, _, err := procShowWindow.Call(uintptr(hWnd), uintptr(originalState)); ret == 0 {
				tm.logger.Debug("Failed to restore window state",
					slog.String("error", err.Error()),
					slog.Int("state", originalState),
					slog.Uint64("hwnd", uint64(uintptr(hWnd))))
			}
		}
	}

	// Configurable delay before restoring focus to ensure key event is processed by Teams
	time.Sleep(keyProcessDelay)

	// Restore focus to original window
	if currentWindow != 0 {
		ret, _, err := procSetForegroundWindow.Call(currentWindow)
		if ret == 0 {
			tm.logger.Debug("Failed to restore focus to original window",
				slog.String("error", err.Error()),
				slog.Uint64("hwnd", uint64(currentWindow)))
		}
	}

	if successCount > 0 {
		tm.logger.Debug("Sent keys to Teams windows",
			slog.String("keys", "F15"),
			slog.Int("window_count", successCount),
			slog.Int("total_windows", len(hwnds)),
			slog.Duration("focus_delay", focusDelay),
			slog.Duration("restore_delay", restoreDelay),
			slog.Duration("key_process_delay", keyProcessDelay))
		return nil
	}

	if lastError != nil {
		return fmt.Errorf("failed to send keys to any Teams window: %v", lastError)
	}
	return fmt.Errorf("failed to send keys to any Teams window")
}

func (tm *TeamsManager) handleTeamsNotFound(_ *websocket.ServiceState) error {
	tm.retryState.FailureCount++
	return fmt.Errorf("no Teams windows found")
}

func sendKeyToWindow(_ win.HWND, vkKey uintptr) error {
	const (
		keyeventfKeydown = 0
		keyeventfKeyup   = 2
	)

	ret, _, err := procKeyboardEvent.Call(vkKey, 0, keyeventfKeydown, 0)
	if ret == 0 {
		return fmt.Errorf("failed to send key down: %v", err)
	}

	// Add small delay between key down and key up to ensure proper key processing
	time.Sleep(1 * time.Millisecond)

	ret, _, err = procKeyboardEvent.Call(vkKey, 0, keyeventfKeyup, 0)
	if ret == 0 {
		return fmt.Errorf("failed to send key up: %v", err)
	}

	return nil
}
