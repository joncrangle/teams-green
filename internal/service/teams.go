package service

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/lxn/win"
	"golang.org/x/sys/windows"
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
)

var currentHwnds *[]win.HWND

func enumWindowsProc(hwnd syscall.Handle, _ uintptr) uintptr {
	hwnds := currentHwnds

	// Skip invisible windows
	ret, _, _ := procIsWindowVisible.Call(uintptr(hwnd))
	if ret == 0 {
		return 1
	}

	var pid uint32
	_, _, _ = procGetWindowThreadProcessID.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&pid)))

	hProcess, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil || hProcess == 0 {
		return 1
	}
	defer func() {
		if err := windows.CloseHandle(hProcess); err != nil {
			slog.Debug("Failed to close process handle", slog.String("error", err.Error()))
		}
	}()

	var exeName [windows.MAX_PATH]uint16
	size := uint32(len(exeName))
	if windows.QueryFullProcessImageName(hProcess, 0, &exeName[0], &size) != nil {
		return 1
	}

	exe := strings.ToLower(windows.UTF16ToString(exeName[:size]))
	exeBase := filepath.Base(exe)

	// Common Teams executable names
	teamsExes := []string{
		"ms-teams.exe", // Classic Teams
		"teams.exe",    // New Teams
		"msteams.exe",  // Alternative name
	}

	// Check if this is a Teams executable
	if slices.Contains(teamsExes, exeBase) {
		*hwnds = append(*hwnds, win.HWND(hwnd))
	}

	return 1
}

func FindTeamsWindows() []win.HWND {
	var hwnds []win.HWND
	currentHwnds = &hwnds

	ret, _, err := procEnumWindows.Call(
		syscall.NewCallback(enumWindowsProc),
		0,
	)
	if ret == 0 {
		slog.Debug("EnumWindows failed", slog.String("error", err.Error()))
	}

	return hwnds
}

func SendKeysToTeams() error {
	hwnds := FindTeamsWindows()
	if len(hwnds) == 0 {
		return fmt.Errorf("no Teams windows found")
	}

	// Store the currently focused window to restore it later
	currentWindow, _, _ := procGetForegroundWindow.Call()

	const (
		vkF15            = 0x41 // F15 key
		keyeventfKeydown = 0
		keyeventfKeyup   = 2
		swHide           = 0
		swRestore        = 9
		swShowMinimized  = 2
		swShowMaximized  = 3
		swShowNormal     = 1
	)

	successCount := 0
	for _, hWnd := range hwnds {
		// Save the current window state
		isMinimized, _, _ := procIsIconic.Call(uintptr(hWnd))
		isMaximized, _, _ := procIsZoomed.Call(uintptr(hWnd))

		// Temporarily restore the window if it's minimized (but don't change maximized state)
		if isMinimized != 0 {
			ret, _, err := procShowWindow.Call(uintptr(hWnd), swRestore)
			if ret == 0 {
				slog.Debug("Failed to restore window", slog.String("error", err.Error()))
			}
			time.Sleep(10 * time.Millisecond) // Brief delay for window state change
		}

		// Focus the Teams window
		ret, _, _ := procSetForegroundWindow.Call(uintptr(hWnd))
		if ret == 0 {
			// Restore minimized state if we changed it
			if isMinimized != 0 {
				ret, _, err := procShowWindow.Call(uintptr(hWnd), swShowMinimized)
				if ret == 0 {
					slog.Debug("Failed to minimize window", slog.String("error", err.Error()))
				}
			}
			continue
		}

		// Minimal delay for focus to take effect
		time.Sleep(5 * time.Millisecond)

		// Send F15
		ret, _, err := procKeyboardEvent.Call(vkF15, 0, keyeventfKeydown, 0)
		if ret == 0 {
			slog.Debug("Failed to send F15 key down", slog.String("error", err.Error()))
		}
		ret, _, err = procKeyboardEvent.Call(vkF15, 0, keyeventfKeyup, 0)
		if ret == 0 {
			slog.Debug("Failed to send F15 key up", slog.String("error", err.Error()))
		}

		// Restore the original window state
		if isMinimized != 0 {
			ret, _, err := procShowWindow.Call(uintptr(hWnd), swShowMinimized)
			if ret == 0 {
				slog.Debug("Failed to minimize window after F15", slog.String("error", err.Error()))
			}
		} else if isMaximized != 0 {
			ret, _, err := procShowWindow.Call(uintptr(hWnd), swShowMaximized)
			if ret == 0 {
				slog.Debug("Failed to maximize window after F15", slog.String("error", err.Error()))
			}
		} else {
			ret, _, err := procShowWindow.Call(uintptr(hWnd), swShowNormal)
			if ret == 0 {
				slog.Debug("Failed to show window as normal after F15", slog.String("error", err.Error()))
			}
		}

		successCount++

		// Small delay before moving to next window
		time.Sleep(5 * time.Millisecond)
	}

	// Restore focus to the original window
	if currentWindow != 0 {
		ret, _, err := procSetForegroundWindow.Call(currentWindow)
		if ret == 0 {
			slog.Debug("Failed to restore focus to original window", slog.String("error", err.Error()))
		}
	}

	if successCount > 0 {
		serviceState.Logger.Debug("Sent keys to Teams windows",
			slog.String("keys", "F15"),
			slog.Int("window_count", successCount))
		return nil
	}

	return fmt.Errorf("failed to send keys to any Teams window")
}
