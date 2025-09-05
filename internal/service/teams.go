package service

import (
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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

type WindowEnumContext struct {
	hwnds            *[]win.HWND
	teamsExecutables []string
	logger           *slog.Logger
}

var (
	teamsExecutables    []string
	executablesCache    []string
	executablesCacheMux sync.RWMutex
	lastDiscoveryTime   time.Time
	discoveryInProgress int32 // atomic flag
)

func init() {
	teamsExecutables = discoverTeamsExecutablesSync()
	executablesCache = make([]string, len(teamsExecutables))
	copy(executablesCache, teamsExecutables)
	lastDiscoveryTime = time.Now()

	// Start background refresh after 5 minutes
	go func() {
		time.Sleep(5 * time.Minute)
		refreshTeamsExecutablesAsync()
	}()
}

func discoverTeamsExecutablesSync() []string {
	baseExes := []string{
		"ms-teams.exe",
		"teams.exe",
		"msteams.exe",
	}

	var discovered []string
	discovered = append(discovered, baseExes...)

	// Quick registry check (limited to avoid blocking)
	if regExes := getTeamsFromRegistryQuick(); len(regExes) > 0 {
		for _, exe := range regExes {
			if !slices.Contains(discovered, exe) {
				discovered = append(discovered, exe)
			}
		}
	}

	return discovered
}

func refreshTeamsExecutablesAsync() {
	// Use atomic compare-and-swap to ensure only one discovery runs at a time
	if !atomic.CompareAndSwapInt32(&discoveryInProgress, 0, 1) {
		return // Another discovery is in progress
	}
	defer atomic.StoreInt32(&discoveryInProgress, 0)

	// Only refresh if it's been more than 30 minutes since last discovery
	executablesCacheMux.RLock()
	timeSinceLastDiscovery := time.Since(lastDiscoveryTime)
	executablesCacheMux.RUnlock()

	if timeSinceLastDiscovery < 30*time.Minute {
		return
	}

	discovered := discoverTeamsExecutablesFull()

	executablesCacheMux.Lock()
	executablesCache = discovered
	lastDiscoveryTime = time.Now()
	executablesCacheMux.Unlock()

	// Schedule next refresh
	time.AfterFunc(30*time.Minute, refreshTeamsExecutablesAsync)
}

func getTeamsExecutables() []string {
	executablesCacheMux.RLock()
	defer executablesCacheMux.RUnlock()

	// Return a copy to prevent concurrent modification
	result := make([]string, len(executablesCache))
	copy(result, executablesCache)
	return result
}

func discoverTeamsExecutablesFull() []string {
	baseExes := []string{
		"ms-teams.exe",
		"teams.exe",
		"msteams.exe",
	}

	var discovered []string
	discovered = append(discovered, baseExes...)

	// Check registry for Teams installations (full scan)
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

func getTeamsFromRegistryQuick() []string {
	// Quick registry check with timeout to avoid blocking
	var executables []string

	// Use a timeout to prevent hanging
	timeout := time.After(2 * time.Second)
	done := make(chan []string, 1)

	go func() {
		result := getTeamsFromRegistry()
		done <- result
	}()

	select {
	case result := <-done:
		return result
	case <-timeout:
		// Return empty if registry check takes too long
		return executables
	}
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
	if hwnd == 0 {
		return ""
	}

	ret, _, _ := procGetWindowTextLength.Call(uintptr(hwnd))
	if ret == 0 || ret > 32767 { // Reasonable upper limit to prevent excessive allocation
		return ""
	}

	length := int(ret)
	buf := make([]uint16, length+1)
	ret, _, _ = procGetWindowText.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if ret == 0 {
		return ""
	}

	// Ensure we don't read beyond actual returned length
	actualLength := min(int(ret), len(buf)-1)
	return windows.UTF16ToString(buf[:actualLength])
}

func enumWindowsProc(hwnd syscall.Handle, lParam uintptr) uintptr {
	// Validate parameters
	if hwnd == 0 || lParam == 0 {
		return 1
	}

	ctx := (*WindowEnumContext)(unsafe.Pointer(lParam))
	if ctx == nil || ctx.hwnds == nil {
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

		// Ensure handle is always closed
		func() {
			defer func() {
				if closeErr := windows.CloseHandle(hProcess); closeErr != nil {
					tm.logger.Debug("Failed to close process handle during cache update",
						slog.String("error", closeErr.Error()),
						slog.Uint64("pid", uint64(pid)),
						slog.Uint64("handle", uint64(uintptr(hProcess))))
				}
			}()

			var exeName [windows.MAX_PATH]uint16
			size := uint32(len(exeName))
			var executablePath string
			if err := windows.QueryFullProcessImageName(hProcess, 0, &exeName[0], &size); err == nil {
				executablePath = windows.UTF16ToString(exeName[:size])
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
		}()
	}

	tm.windowCache.Windows = newWindows
	tm.windowCache.LastUpdate = now
}

func (tm *TeamsManager) FindTeamsWindows() []win.HWND {
	tm.windowCache.Mutex.RLock()

	now := time.Now()

	// Dynamic cache duration based on failure state
	cacheDuration := tm.windowCache.CacheDuration
	if tm.retryState.ConsecutiveFailures > 0 {
		// Reduce cache duration during failures for faster recovery
		cacheDuration = 15 * time.Second
		if tm.retryState.ConsecutiveFailures >= 5 {
			cacheDuration = 10 * time.Second // Even shorter during persistent failures
		}
	}

	cacheValid := time.Since(tm.windowCache.LastUpdate) < cacheDuration

	// Check for system sleep/hibernation by looking for large time gaps
	timeSinceLastCheck := time.Since(tm.windowCache.LastCacheCheck)
	if timeSinceLastCheck > 2*cacheDuration {
		tm.logger.Debug("Detected possible system sleep/hibernation, invalidating cache",
			slog.Duration("time_gap", timeSinceLastCheck))
		cacheValid = false
	}

	// Update last check time
	tm.windowCache.LastCacheCheck = now

	// Adaptive full scan interval based on failure rate
	fullScanInterval := tm.windowCache.AdaptiveScanInterval
	if tm.windowCache.FailureCount > 5 {
		fullScanInterval = 2 * time.Minute // Scan more frequently if failures
	} else if tm.windowCache.FailureCount == 0 && len(tm.windowCache.Windows) > 0 {
		fullScanInterval = 10 * time.Minute // Scan less frequently if stable
	}

	needsFullScan := time.Since(tm.windowCache.LastFullScan) > fullScanInterval || len(tm.windowCache.Windows) == 0
	tm.windowCache.Mutex.RUnlock()

	// If cache is valid and we have windows, try quick validation first
	if cacheValid && !needsFullScan {
		validWindows := tm.quickValidateWindows()
		if len(validWindows) > 0 {
			// Reset failure count on successful validation
			tm.windowCache.Mutex.Lock()
			tm.windowCache.FailureCount = 0
			tm.windowCache.Mutex.Unlock()
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
		// Quick batch check - combine window visibility and process running checks
		isVisible, _, _ := procIsWindowVisible.Call(uintptr(window.HWND))
		if isVisible != 0 && IsProcessRunning(int(window.ProcessID)) {
			window.LastSeen = time.Now()
			window.IsValid = true
			validWindows = append(validWindows, window)
			validHwnds = append(validHwnds, window.HWND)
		}
	}

	if len(validWindows) > 0 {
		tm.windowCache.Windows = validWindows
		tm.windowCache.LastUpdate = time.Now()
		tm.windowCache.FailureCount = 0
		tm.logger.Debug("Quick window validation successful",
			slog.Int("window_count", len(validWindows)))
	} else {
		tm.windowCache.FailureCount++
		tm.windowCache.LastFailure = time.Now()
	}

	return validHwnds
}

func (tm *TeamsManager) performFullWindowScan(isFullScan bool) []win.HWND {
	var hwnds []win.HWND
	tm.enumContext.hwnds = &hwnds

	tm.logger.Debug("Performing window enumeration",
		slog.Bool("full_scan", isFullScan))

	startTime := time.Now()
	ret, _, err := procEnumWindows.Call(
		syscall.NewCallback(enumWindowsProc),
		uintptr(unsafe.Pointer(tm.enumContext)),
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
		// Batch window state queries to reduce syscalls
		isMinimized, _, _ := procIsIconic.Call(uintptr(hWnd))
		isMaximized, _, _ := procIsZoomed.Call(uintptr(hWnd))

		// Check if this Teams window already has focus
		currentFocus, _, _ := procGetForegroundWindow.Call()
		if currentFocus == uintptr(hWnd) {
			// Teams window already has focus, safe to send key
			if err := sendKeyToWindow(hWnd, vkF15); err != nil {
				slog.Debug("Failed to send F15 key to focused window",
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
				slog.Debug("Failed to restore minimized window",
					slog.String("error", err.Error()),
					slog.Uint64("hwnd", uint64(uintptr(hWnd))))
				lastError = err
				continue
			}
			time.Sleep(2 * time.Millisecond)
		} else if isMaximized != 0 {
			originalState = swShowMaximized
		}

		// Focus and send key with validation
		if ret, _, err := procSetForegroundWindow.Call(uintptr(hWnd)); ret != 0 {
			time.Sleep(1 * time.Millisecond)

			// Verify focus was actually set before sending key
			if focusedWindow, _, _ := procGetForegroundWindow.Call(); focusedWindow == uintptr(hWnd) {
				if err := sendKeyToWindow(hWnd, vkF15); err != nil {
					slog.Debug("Failed to send F15 key",
						slog.String("error", err.Error()),
						slog.Uint64("hwnd", uint64(uintptr(hWnd))))
					lastError = err
				} else {
					successCount++
					tm.logger.Debug("Sent F15 to newly focused Teams window",
						slog.Uint64("hwnd", uint64(uintptr(hWnd))))
				}
			} else {
				slog.Debug("Focus validation failed - skipping key send to prevent leakage",
					slog.Uint64("expected_hwnd", uint64(uintptr(hWnd))),
					slog.Uint64("actual_focused", uint64(focusedWindow)))
				lastError = fmt.Errorf("focus validation failed")
			}
		} else {
			slog.Debug("Failed to set foreground window",
				slog.String("error", err.Error()),
				slog.Uint64("hwnd", uint64(uintptr(hWnd))))
			lastError = err
		}

		// Restore original window state if needed
		if originalState != swShowNormal {
			if ret, _, err := procShowWindow.Call(uintptr(hWnd), uintptr(originalState)); ret == 0 {
				slog.Debug("Failed to restore window state",
					slog.String("error", err.Error()),
					slog.Int("state", originalState),
					slog.Uint64("hwnd", uint64(uintptr(hWnd))))
			}
		}
	}

	// Minimal delay before restoring focus to ensure key event is processed by Teams
	time.Sleep(5 * time.Millisecond)

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

func calculateJitteredBackoff(attempt int, maxBackoff int) int {
	// Exponential backoff with jitter to prevent thundering herd
	baseBackoff := min(30*(1<<uint(attempt)), maxBackoff)
	// Add random jitter (±25%)
	jitter := int(float64(baseBackoff) * 0.25 * (rand.Float64()*2 - 1))
	return max(1, baseBackoff+jitter)
}

func (tm *TeamsManager) handleTeamsNotFound(_ *websocket.ServiceState) error {
	// Handle circuit breaker logic
	now := time.Now()

	// Check if circuit breaker should close (recovery period)
	if tm.retryState.CircuitBreakerOpen {
		timeSinceOpened := time.Since(tm.retryState.CircuitOpenTime)
		if timeSinceOpened > 45*time.Second {
			tm.retryState.CircuitBreakerOpen = false
			tm.retryState.ConsecutiveFailures = max(0, tm.retryState.ConsecutiveFailures-5) // Gradual recovery
			tm.logger.Info("Circuit breaker closed - attempting recovery",
				slog.Int("remaining_consecutive_failures", tm.retryState.ConsecutiveFailures))
		} else {
			// Still in circuit breaker open state
			return fmt.Errorf("circuit breaker open - Teams unavailable (open for %v, consecutive failures: %d)",
				timeSinceOpened.Truncate(time.Second), tm.retryState.ConsecutiveFailures)
		}
	}

	// Open circuit breaker if too many consecutive failures
	if tm.retryState.ConsecutiveFailures >= 12 && !tm.retryState.CircuitBreakerOpen {
		tm.retryState.CircuitBreakerOpen = true
		tm.retryState.CircuitOpenTime = now
		tm.logger.Warn("Circuit breaker opened due to consecutive failures",
			slog.Int("consecutive_failures", tm.retryState.ConsecutiveFailures))
		return fmt.Errorf("circuit breaker opened - Teams unavailable (consecutive failures: %d)", tm.retryState.ConsecutiveFailures)
	}

	if tm.retryState.FailureCount == 0 {
		tm.retryState.LastFailure = now
	}

	tm.retryState.FailureCount++
	tm.retryState.ConsecutiveFailures++

	// Check if we should attempt Teams process restart detection
	if tm.retryState.ConsecutiveFailures >= 5 &&
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

	// Calculate jittered backoff
	backoff := calculateJitteredBackoff(tm.retryState.FailureCount, tm.retryState.MaxBackoff)
	tm.retryState.BackoffSeconds = backoff

	if time.Since(tm.retryState.LastFailure) < time.Duration(backoff)*time.Second {
		// Still in backoff period, return cached error without logging
		remaining := backoff - int(time.Since(tm.retryState.LastFailure).Seconds())
		return fmt.Errorf("no Teams windows found (backoff: %ds remaining, consecutive failures: %d)",
			remaining, tm.retryState.ConsecutiveFailures)
	}

	tm.retryState.LastFailure = now
	return fmt.Errorf("no Teams windows found (attempt %d, next retry in %ds, consecutive: %d)",
		tm.retryState.FailureCount, backoff, tm.retryState.ConsecutiveFailures)
}

func getRunningTeamsProcesses() []uint32 {
	var processes []uint32
	var hwnds []win.HWND

	// Pre-allocate with reasonable capacity to reduce allocations
	processes = make([]uint32, 0, 10)
	processSet := make(map[uint32]bool, 10) // Use map for O(1) duplicate detection

	enumCtx := &WindowEnumContext{
		hwnds:            &hwnds,
		teamsExecutables: getTeamsExecutables(),
		logger:           slog.Default(),
	}

	_, _, err := procEnumWindows.Call(
		syscall.NewCallback(func(hwnd syscall.Handle, lParam uintptr) uintptr {
			ctx := (*WindowEnumContext)(unsafe.Pointer(lParam))

			var pid uint32
			_, _, _ = procGetWindowThreadProcessID.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&pid)))

			hProcess, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
			if err != nil || hProcess == 0 {
				return 1
			}

			// Ensure handle is closed immediately after use
			defer func() {
				if closeErr := windows.CloseHandle(hProcess); closeErr != nil {
					ctx.logger.Debug("Failed to close process handle in getRunningTeamsProcesses",
						slog.String("error", closeErr.Error()),
						slog.Uint64("pid", uint64(pid)))
				}
			}()

			var exeName [windows.MAX_PATH]uint16
			size := uint32(len(exeName))
			if err := windows.QueryFullProcessImageName(hProcess, 0, &exeName[0], &size); err != nil {
				return 1
			}

			// Optimize string operations to avoid repeated allocations
			exePath := windows.UTF16ToString(exeName[:size])
			lastSlash := strings.LastIndexByte(exePath, '\\')
			var exeBase string
			if lastSlash != -1 {
				exeBase = strings.ToLower(exePath[lastSlash+1:])
			} else {
				exeBase = strings.ToLower(exePath)
			}

			if slices.Contains(ctx.teamsExecutables, exeBase) {
				// Use map to avoid duplicates with O(1) lookup
				if !processSet[pid] {
					processSet[pid] = true
					processes = append(processes, pid)
				}
			}

			return 1
		}),
		uintptr(unsafe.Pointer(enumCtx)),
	)
	if err != nil {
		slog.Debug("EnumWindows failed in getRunningTeamsProcesses", slog.String("error", err.Error()))
	}

	return processes
}

func (tm *TeamsManager) monitorTeamsStartup() {
	// Monitor for Teams startup for up to 60 seconds
	timeout := time.After(60 * time.Second)
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

	// Add small delay between key down and key up to ensure proper key processing
	time.Sleep(1 * time.Millisecond)

	ret, _, err = procKeyboardEvent.Call(vkKey, 0, keyeventfKeyup, 0)
	if ret == 0 {
		return fmt.Errorf("failed to send key up: %v", err)
	}

	return nil
}
