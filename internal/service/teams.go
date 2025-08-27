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

	"github.com/joncrangle/teams-green/internal/websocket"
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
	LastFullScan  time.Time
	QuickScanMode bool
}

type RetryState struct {
	FailureCount        int
	LastFailure         time.Time
	BackoffSeconds      int
	MaxBackoff          int
	TeamsRestartCount   int
	LastTeamsRestart    time.Time
	ConsecutiveFailures int
}

var (
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
			slog.Debug("Failed to close process handle",
				slog.String("error", err.Error()),
				slog.Uint64("pid", uint64(pid)),
				slog.Uint64("handle", uint64(uintptr(hProcess))))
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

func (tm *TeamsManager) updateWindowCache(hwnds []win.HWND) {
	tm.windowCache.Mutex.Lock()
	defer tm.windowCache.Mutex.Unlock()

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
		if err := windows.CloseHandle(hProcess); err != nil {
			slog.Debug("Failed to close process handle during cache update",
				slog.String("error", err.Error()),
				slog.Uint64("pid", uint64(pid)),
				slog.Uint64("handle", uint64(uintptr(hProcess))))
		}

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

	tm.windowCache.Windows = newWindows
	tm.windowCache.LastUpdate = now
}

func (tm *TeamsManager) FindTeamsWindows() []win.HWND {
	tm.windowCache.Mutex.RLock()
	cacheValid := time.Since(tm.windowCache.LastUpdate) < tm.windowCache.CacheDuration
	needsFullScan := time.Since(tm.windowCache.LastFullScan) > 5*time.Minute || len(tm.windowCache.Windows) == 0
	tm.windowCache.Mutex.RUnlock()

	// If cache is valid and we have windows, try quick validation first
	if cacheValid && !needsFullScan {
		validWindows := tm.quickValidateWindows()
		if len(validWindows) > 0 {
			return validWindows
		}
	}

	// Fallback to full enumeration
	return tm.performFullWindowScan(needsFullScan)
}

func (tm *TeamsManager) quickValidateWindows() []win.HWND {
	tm.windowCache.Mutex.Lock()
	defer tm.windowCache.Mutex.Unlock()

	var validHwnds []win.HWND
	var validWindows []TeamsWindow

	for _, window := range tm.windowCache.Windows {
		// Quick check if window still exists and is visible
		if ret, _, _ := procIsWindowVisible.Call(uintptr(window.HWND)); ret != 0 {
			// Verify process is still running (lightweight check)
			if IsProcessRunning(int(window.ProcessID)) {
				window.LastSeen = time.Now()
				window.IsValid = true
				validWindows = append(validWindows, window)
				validHwnds = append(validHwnds, window.HWND)
			}
		}
	}

	if len(validWindows) > 0 {
		tm.windowCache.Windows = validWindows
		tm.windowCache.LastUpdate = time.Now()
		tm.logger.Debug("Quick window validation successful",
			slog.Int("window_count", len(validWindows)))
	}

	return validHwnds
}

func (tm *TeamsManager) performFullWindowScan(isFullScan bool) []win.HWND {
	var hwnds []win.HWND
	currentHwnds = &hwnds

	tm.logger.Debug("Performing window enumeration",
		slog.Bool("full_scan", isFullScan))

	startTime := time.Now()
	ret, _, err := procEnumWindows.Call(
		syscall.NewCallback(enumWindowsProc),
		0,
	)
	scanDuration := time.Since(startTime)

	if ret == 0 {
		slog.Debug("EnumWindows failed", slog.String("error", err.Error()))
	}

	tm.logger.Debug("Window enumeration completed",
		slog.Int("window_count", len(hwnds)),
		slog.Duration("scan_time", scanDuration))

	// Update cache with new results
	tm.updateWindowCache(hwnds)

	tm.windowCache.Mutex.Lock()
	if isFullScan {
		tm.windowCache.LastFullScan = time.Now()
	}
	tm.windowCache.Mutex.Unlock()

	return hwnds
}

func (tm *TeamsManager) SendKeysToTeams(state *websocket.ServiceState) error {
	hwnds := tm.FindTeamsWindows()
	if len(hwnds) == 0 {
		return tm.handleTeamsNotFound(state)
	}

	// Reset retry state on success
	if tm.retryState.FailureCount > 0 {
		tm.logger.Info("Teams windows found after failures",
			slog.Int("failure_count", tm.retryState.FailureCount),
			slog.Int("consecutive_failures", tm.retryState.ConsecutiveFailures))
		tm.retryState.FailureCount = 0
		tm.retryState.BackoffSeconds = 0
		tm.retryState.ConsecutiveFailures = 0
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
		tm.logger.Debug("Sent keys to Teams windows",
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

func (tm *TeamsManager) handleTeamsNotFound(_ *websocket.ServiceState) error {
	// Handle retry logic with exponential backoff
	now := time.Now()
	if tm.retryState.FailureCount == 0 {
		tm.retryState.LastFailure = now
	}

	tm.retryState.FailureCount++
	tm.retryState.ConsecutiveFailures++

	// Check if we should attempt Teams process restart detection
	if tm.retryState.ConsecutiveFailures >= 10 &&
		time.Since(tm.retryState.LastTeamsRestart) > 5*time.Minute {

		// Check if Teams processes still exist
		if teamsProcesses := getRunningTeamsProcesses(); len(teamsProcesses) == 0 {
			tm.logger.Warn("No Teams processes found, Teams may have been closed")

			// Try to detect if Teams is starting up
			go tm.monitorTeamsStartup()

			tm.retryState.TeamsRestartCount++
			tm.retryState.LastTeamsRestart = now

			return fmt.Errorf("teams not running (restart attempt %d)", tm.retryState.TeamsRestartCount)
		}
	}

	// Calculate backoff (exponential up to max)
	backoff := min(30*(1<<uint(tm.retryState.FailureCount-1)), tm.retryState.MaxBackoff)
	tm.retryState.BackoffSeconds = backoff

	if time.Since(tm.retryState.LastFailure) < time.Duration(backoff)*time.Second {
		// Still in backoff period, return cached error without logging
		return fmt.Errorf("no Teams windows found (backoff: %ds remaining, consecutive failures: %d)",
			backoff-int(time.Since(tm.retryState.LastFailure).Seconds()),
			tm.retryState.ConsecutiveFailures)
	}

	tm.retryState.LastFailure = now
	return fmt.Errorf("no Teams windows found (attempt %d, next retry in %ds, consecutive: %d)",
		tm.retryState.FailureCount, backoff, tm.retryState.ConsecutiveFailures)
}

func getRunningTeamsProcesses() []uint32 {
	var processes []uint32
	var hwnds []win.HWND
	currentHwnds = &hwnds

	_, _, err := procEnumWindows.Call(
		syscall.NewCallback(func(hwnd syscall.Handle, _ uintptr) uintptr {
			var pid uint32
			_, _, _ = procGetWindowThreadProcessID.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&pid)))

			hProcess, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
			if err != nil || hProcess == 0 {
				return 1
			}
			defer func() {
				if err := windows.CloseHandle(hProcess); err != nil {
					slog.Debug("Failed to close process handle in getRunningTeamsProcesses",
						slog.String("error", err.Error()),
						slog.Uint64("pid", uint64(pid)))
				}
			}()

			var exeName [windows.MAX_PATH]uint16
			size := uint32(len(exeName))
			if err := windows.QueryFullProcessImageName(hProcess, 0, &exeName[0], &size); err != nil {
				return 1
			}

			exe := strings.ToLower(windows.UTF16ToString(exeName[:size]))
			exeBase := filepath.Base(exe)

			if slices.Contains(teamsExecutables, exeBase) {
				// Check if PID is already in the list
				for _, existingPID := range processes {
					if existingPID == pid {
						return 1 // Already added
					}
				}
				processes = append(processes, pid)
			}

			return 1
		}),
		0,
	)
	if err != nil {
		slog.Debug("EnumWindows failed in getRunningTeamsProcesses", slog.String("error", err.Error()))
	}

	return processes
}

func (tm *TeamsManager) monitorTeamsStartup() {
	// Monitor for Teams startup for up to 30 seconds
	timeout := time.After(30 * time.Second)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			tm.logger.Debug("Teams startup monitoring timed out")
			return
		case <-ticker.C:
			if processes := getRunningTeamsProcesses(); len(processes) > 0 {
				tm.logger.Info("Detected Teams startup",
					slog.Int("process_count", len(processes)))

				// Reset consecutive failures on successful detection
				tm.retryState.ConsecutiveFailures = 0

				// Clear window cache to force fresh discovery
				tm.windowCache.Mutex.Lock()
				tm.windowCache.Windows = nil
				tm.windowCache.LastUpdate = time.Time{}
				tm.windowCache.Mutex.Unlock()

				return
			}
		}
	}
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
