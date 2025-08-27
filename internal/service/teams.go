package service

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/lxn/win"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
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
	procGetWindowTextLength      = user32.NewProc("GetWindowTextLengthW")
	procGetWindowText            = user32.NewProc("GetWindowTextW")
)

type TeamsWindow struct {
	HWND           win.HWND
	ProcessID      uint32
	ExecutablePath string
	WindowTitle    string
	LastSeen       time.Time
	IsValid        bool
}

type WindowCache struct {
	Windows       []TeamsWindow
	LastUpdate    time.Time
	Mutex         sync.RWMutex
	CacheDuration time.Duration
}

type RetryState struct {
	FailureCount   int
	LastFailure    time.Time
	BackoffSeconds int
	MaxBackoff     int
}

var (
	windowCache = &WindowCache{
		Windows:       make([]TeamsWindow, 0),
		CacheDuration: 30 * time.Second,
	}
	retryState = &RetryState{
		MaxBackoff: 300, // 5 minutes max
	}
	teamsExecutables []string
	currentHwnds     *[]win.HWND
)

func init() {
	teamsExecutables = discoverTeamsExecutables()
}

func discoverTeamsExecutables() []string {
	baseExes := []string{
		"ms-teams.exe",
		"teams.exe",
		"msteams.exe",
	}

	var discovered []string
	discovered = append(discovered, baseExes...)

	// Check registry for Teams installations
	if regExes := getTeamsFromRegistry(); len(regExes) > 0 {
		for _, exe := range regExes {
			if !slices.Contains(discovered, exe) {
				discovered = append(discovered, exe)
			}
		}
	}

	// Check common installation paths
	commonPaths := []string{
		os.Getenv("LOCALAPPDATA") + "\\Microsoft\\Teams",
		os.Getenv("LOCALAPPDATA") + "\\Programs\\Microsoft Teams",
		os.Getenv("PROGRAMFILES") + "\\Microsoft Teams",
		os.Getenv("PROGRAMFILES(X86)") + "\\Microsoft Teams",
	}

	for _, path := range commonPaths {
		if entries, err := os.ReadDir(path); err == nil {
			for _, entry := range entries {
				if strings.HasSuffix(strings.ToLower(entry.Name()), ".exe") &&
					strings.Contains(strings.ToLower(entry.Name()), "teams") {
					exe := strings.ToLower(entry.Name())
					if !slices.Contains(discovered, exe) {
						discovered = append(discovered, exe)
					}
				}
			}
		}
	}

	return discovered
}

func getTeamsFromRegistry() []string {
	var executables []string

	// Check uninstall registry for Teams entries
	key, err := registry.OpenKey(registry.LOCAL_MACHINE,
		"SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Uninstall", registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return executables
	}
	defer key.Close()

	subkeys, err := key.ReadSubKeyNames(0)
	if err != nil {
		return executables
	}

	for _, subkey := range subkeys {
		if strings.Contains(strings.ToLower(subkey), "teams") {
			subKey, err := registry.OpenKey(key, subkey, registry.QUERY_VALUE)
			if err != nil {
				continue
			}

			if displayName, _, err := subKey.GetStringValue("DisplayName"); err == nil {
				if strings.Contains(strings.ToLower(displayName), "teams") {
					if installLocation, _, err := subKey.GetStringValue("InstallLocation"); err == nil {
						if entries, err := os.ReadDir(installLocation); err == nil {
							for _, entry := range entries {
								if strings.HasSuffix(strings.ToLower(entry.Name()), ".exe") &&
									strings.Contains(strings.ToLower(entry.Name()), "teams") {
									executables = append(executables, strings.ToLower(entry.Name()))
								}
							}
						}
					}
				}
			}
			subKey.Close()
		}
	}

	return executables
}

func getWindowTitle(hwnd win.HWND) string {
	ret, _, _ := procGetWindowTextLength.Call(uintptr(hwnd))
	if ret == 0 {
		return ""
	}

	buf := make([]uint16, ret+1)
	ret, _, _ = procGetWindowText.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if ret == 0 {
		return ""
	}

	return windows.UTF16ToString(buf)
}

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
	if err := windows.QueryFullProcessImageName(hProcess, 0, &exeName[0], &size); err != nil {
		slog.Debug("Failed to get process image name",
			slog.Uint64("pid", uint64(pid)),
			slog.String("error", err.Error()))
		return 1
	}

	exe := strings.ToLower(windows.UTF16ToString(exeName[:size]))
	exeBase := filepath.Base(exe)

	// Check if this is a Teams executable using discovered list
	if slices.Contains(teamsExecutables, exeBase) {
		*hwnds = append(*hwnds, win.HWND(hwnd))
	}

	return 1
}

func validateCachedWindows() []TeamsWindow {
	windowCache.Mutex.Lock()
	defer windowCache.Mutex.Unlock()

	var validWindows []TeamsWindow
	for _, window := range windowCache.Windows {
		// Check if window still exists and is visible
		if ret, _, _ := procIsWindowVisible.Call(uintptr(window.HWND)); ret != 0 {
			// Check if process is still running
			if IsProcessRunning(int(window.ProcessID)) {
				window.LastSeen = time.Now()
				window.IsValid = true
				validWindows = append(validWindows, window)
			}
		}
	}

	windowCache.Windows = validWindows
	return validWindows
}

func updateWindowCache(hwnds []win.HWND) {
	windowCache.Mutex.Lock()
	defer windowCache.Mutex.Unlock()

	now := time.Now()
	var newWindows []TeamsWindow

	for _, hwnd := range hwnds {
		var pid uint32
		_, _, _ = procGetWindowThreadProcessID.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&pid)))

		hProcess, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if err != nil {
			continue
		}

		var exeName [windows.MAX_PATH]uint16
		size := uint32(len(exeName))
		var executablePath string
		if err := windows.QueryFullProcessImageName(hProcess, 0, &exeName[0], &size); err == nil {
			executablePath = windows.UTF16ToString(exeName[:size])
		}
		_ = windows.CloseHandle(hProcess)

		windowTitle := getWindowTitle(hwnd)

		window := TeamsWindow{
			HWND:           hwnd,
			ProcessID:      pid,
			ExecutablePath: executablePath,
			WindowTitle:    windowTitle,
			LastSeen:       now,
			IsValid:        true,
		}

		newWindows = append(newWindows, window)
	}

	windowCache.Windows = newWindows
	windowCache.LastUpdate = now
}

func FindTeamsWindows() []win.HWND {
	windowCache.Mutex.RLock()
	cacheValid := time.Since(windowCache.LastUpdate) < windowCache.CacheDuration
	windowCache.Mutex.RUnlock()

	if cacheValid {
		validWindows := validateCachedWindows()
		if len(validWindows) > 0 {
			hwnds := make([]win.HWND, len(validWindows))
			for i, window := range validWindows {
				hwnds[i] = window.HWND
			}
			return hwnds
		}
	}

	// Cache miss or invalid, enumerate windows
	var hwnds []win.HWND
	currentHwnds = &hwnds

	ret, _, err := procEnumWindows.Call(
		syscall.NewCallback(enumWindowsProc),
		0,
	)
	if ret == 0 {
		slog.Debug("EnumWindows failed", slog.String("error", err.Error()))
	}

	// Update cache with new results
	updateWindowCache(hwnds)

	return hwnds
}

func SendKeysToTeams() error {
	hwnds := FindTeamsWindows()
	if len(hwnds) == 0 {
		// Handle retry logic with exponential backoff
		now := time.Now()
		if retryState.FailureCount == 0 {
			retryState.LastFailure = now
		}

		retryState.FailureCount++

		// Calculate backoff (exponential up to max)
		backoff := min(30*(1<<uint(retryState.FailureCount-1)), retryState.MaxBackoff)
		retryState.BackoffSeconds = backoff

		if time.Since(retryState.LastFailure) < time.Duration(backoff)*time.Second {
			// Still in backoff period, return cached error without logging
			return fmt.Errorf("no Teams windows found (backoff: %ds remaining)",
				backoff-int(time.Since(retryState.LastFailure).Seconds()))
		}

		retryState.LastFailure = now
		return fmt.Errorf("no Teams windows found (attempt %d, next retry in %ds)",
			retryState.FailureCount, backoff)
	}

	// Reset retry state on success
	if retryState.FailureCount > 0 {
		serviceState.Logger.Info("Teams windows found after failures",
			slog.Int("failure_count", retryState.FailureCount))
		retryState.FailureCount = 0
		retryState.BackoffSeconds = 0
	}

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
		// Try to send key without changing window state first
		if err := sendKeyToWindow(hWnd, vkF15); err == nil {
			successCount++
			continue
		}

		// If direct key sending failed, try with window focus
		isMinimized, _, _ := procIsIconic.Call(uintptr(hWnd))
		isMaximized, _, _ := procIsZoomed.Call(uintptr(hWnd))

		// Only restore if minimized
		originalState := swShowNormal
		if isMinimized != 0 {
			originalState = swShowMinimized
			ret, _, err := procShowWindow.Call(uintptr(hWnd), swRestore)
			if ret == 0 {
				slog.Debug("Failed to restore minimized window",
					slog.String("error", err.Error()),
					slog.Uint64("hwnd", uint64(uintptr(hWnd))))
				lastError = err
				continue
			}
			time.Sleep(5 * time.Millisecond)
		} else if isMaximized != 0 {
			originalState = swShowMaximized
		}

		// Focus the window
		ret, _, err := procSetForegroundWindow.Call(uintptr(hWnd))
		if ret == 0 {
			slog.Debug("Failed to set foreground window",
				slog.String("error", err.Error()),
				slog.Uint64("hwnd", uint64(uintptr(hWnd))))
			lastError = err
			// Restore window state before continuing
			if isMinimized != 0 {
				_, _, _ = procShowWindow.Call(uintptr(hWnd), swShowMinimized)
			}
			continue
		}

		time.Sleep(5 * time.Millisecond)

		// Send F15 key
		if err := sendKeyToWindow(hWnd, vkF15); err != nil {
			slog.Debug("Failed to send F15 key",
				slog.String("error", err.Error()),
				slog.Uint64("hwnd", uint64(uintptr(hWnd))))
			lastError = err
		} else {
			successCount++
		}

		// Restore original window state
		if originalState != swShowNormal {
			ret, _, err := procShowWindow.Call(uintptr(hWnd), uintptr(originalState))
			if ret == 0 {
				slog.Debug("Failed to restore window state",
					slog.String("error", err.Error()),
					slog.Int("state", originalState),
					slog.Uint64("hwnd", uint64(uintptr(hWnd))))
			}
		}

		time.Sleep(5 * time.Millisecond)
	}

	// Restore focus to original window
	if currentWindow != 0 {
		ret, _, err := procSetForegroundWindow.Call(currentWindow)
		if ret == 0 {
			slog.Debug("Failed to restore focus to original window",
				slog.String("error", err.Error()),
				slog.Uint64("hwnd", uint64(currentWindow)))
		}
	}

	if successCount > 0 {
		serviceState.Logger.Debug("Sent keys to Teams windows",
			slog.String("keys", "F15"),
			slog.Int("window_count", successCount),
			slog.Int("total_windows", len(hwnds)))
		return nil
	}

	if lastError != nil {
		return fmt.Errorf("failed to send keys to any Teams window: %v", lastError)
	}
	return fmt.Errorf("failed to send keys to any Teams window")
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

	ret, _, err = procKeyboardEvent.Call(vkKey, 0, keyeventfKeyup, 0)
	if ret == 0 {
		return fmt.Errorf("failed to send key up: %v", err)
	}

	return nil
}
