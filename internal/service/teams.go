package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/lxn/win"
	"golang.org/x/sys/windows"

	"github.com/joncrangle/teams-green/internal/websocket"
)

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	kernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procIsIconic                 = user32.NewProc("IsIconic")
	procIsZoomed                 = user32.NewProc("IsZoomed")
	procShowWindow               = user32.NewProc("ShowWindow")
	procBringWindowToTop         = user32.NewProc("BringWindowToTop")
	procAllowSetForegroundWindow = user32.NewProc("AllowSetForegroundWindow")
	procAttachThreadInput        = user32.NewProc("AttachThreadInput")
	procGetCurrentThreadID       = kernel32.NewProc("GetCurrentThreadId")
	procGetWindowLong            = user32.NewProc("GetWindowLongW")
	procSetWindowPos             = user32.NewProc("SetWindowPos")
	procGetWindowText            = user32.NewProc("GetWindowTextW")
	procGetWindowTextLength      = user32.NewProc("GetWindowTextLengthW")
	procGetClassName             = user32.NewProc("GetClassNameW")
)

// ErrUserInputActive is returned when user input is detected during Teams activity
var (
	ErrUserInputActive = errors.New("user input active, deferring Teams activity")
	errStage3Skipped   = errors.New("focus escalation stage 3 skipped")
)

// WindowInfo represents information about a window
type WindowInfo struct {
	HWND       win.HWND
	Title      string
	ClassName  string
	ProcessID  uint32
	Executable string
}

type WindowEnumContext struct {
	windows          *[]WindowInfo
	teamsExecutables []string
	logger           *slog.Logger
	mutex            *sync.Mutex
}

// ConfigProvider interface for configuration access
type ConfigProvider interface {
	GetFocusDelay() time.Duration
	GetRestoreDelay() time.Duration
	GetKeyProcessDelay() time.Duration
	GetInputThreshold() time.Duration
	IsDebugEnabled() bool
	GetActivityMode() string
}

// (Deprecated) Previously exported SendKeyToWindow removed as focus-based key injection
// is now handled internally with optional global mode fallback.

// windowCache stores cached window handles with metadata
type windowCache struct {
	handles   []win.HWND
	timestamp time.Time
	mutex     sync.RWMutex
}

// TeamsManager manages interactions with Teams windows
type TeamsManager struct {
	now         func() time.Time
	logger      *slog.Logger
	config      ConfigProvider
	enumContext *WindowEnumContext
	retryState  struct {
		FailureCount int
	}
	focusFailures      map[win.HWND]focusFailInfo
	lastEscalation     map[win.HWND]time.Time
	escalationCooldown time.Duration
	windowCache        *windowCache
	cacheTTL           time.Duration
}

const (
	focusFailureThreshold        = 3
	focusFailureWindow           = 90 * time.Second
	focusFailureRecordExpiry     = 5 * time.Minute
	maxFocusFailureCountCap      = 1000
	foregroundVerificationChecks = 3
	escalationCooldownDefault    = 3 * time.Minute
	windowCacheTTLDefault        = 30 * time.Second
)

type focusFailInfo struct {
	lastFail          time.Time
	lastForegroundPID uint32
	failCount         int
}

// recordFocusFailure updates failure tracking for a window focus attempt.
func (tm *TeamsManager) recordFocusFailure(hWnd win.HWND, foregroundPID uint32) {
	if tm.focusFailures == nil {
		return
	}
	info := tm.focusFailures[hWnd]
	// Reset streak if different foreground PID (context changed)
	if info.lastForegroundPID != 0 && info.lastForegroundPID != foregroundPID {
		info.failCount = 0
	}
	info.failCount++
	if tm.now != nil {
		info.lastFail = tm.now()
	}
	info.lastForegroundPID = foregroundPID
	// Cap fail count to avoid unbounded growth
	if info.failCount > maxFocusFailureCountCap {
		info.failCount = maxFocusFailureCountCap
	}
	tm.focusFailures[hWnd] = info
}

// resetFocusFailure clears failure tracking for a window after success.
func (tm *TeamsManager) resetFocusFailure(hWnd win.HWND) {
	if tm.focusFailures == nil {
		return
	}
	delete(tm.focusFailures, hWnd)
}

// shouldSuppressFocus determines if further intrusive focus attempts should be skipped
// for this window to avoid user disruption. Conservative defaults until configurable.
func (tm *TeamsManager) shouldSuppressFocus(hWnd win.HWND, foregroundPID uint32) bool {
	if tm.focusFailures == nil {
		return false
	}
	info, ok := tm.focusFailures[hWnd]
	if !ok {
		return false
	}
	now := time.Now()
	if tm.now != nil {
		now = tm.now()
	}
	if info.failCount >= focusFailureThreshold && info.lastForegroundPID == foregroundPID && now.Sub(info.lastFail) < focusFailureWindow {
		if tm.logger != nil {
			tm.logger.Debug("Focus suppression active",
				slog.Uint64("hwnd", uint64(uintptr(hWnd))),
				slog.Int("fail_count", info.failCount),
				slog.Uint64("foreground_pid", uint64(foregroundPID)),
				slog.Duration("since_last_fail", now.Sub(info.lastFail)))
		}
		return true
	}
	if now.Sub(info.lastFail) > focusFailureRecordExpiry {
		delete(tm.focusFailures, hWnd)
	}
	return false
}

var teamsExecutables []string

func init() {
	teamsExecutables = []string{
		"ms-teams.exe",
		"teams.exe",
		"msteams.exe",
	}
}

func enumWindowsProc(hwnd syscall.Handle, lParam uintptr) uintptr {
	// Validate parameters
	if hwnd == 0 || lParam == 0 {
		return 1
	}

	// Safe conversion from uintptr to unsafe.Pointer for Windows callback
	// This is safe because lParam comes directly from Windows EnumWindows callback
	// and we've validated it's non-zero above
	//nolint:govet // Windows callback pattern requires unsafe pointer conversion
	ctx := (*WindowEnumContext)(unsafe.Pointer(lParam))

	// Safety checks after unsafe conversion
	if ctx == nil {
		return 1
	}

	// Validate all required context fields before use
	if ctx.windows == nil || ctx.teamsExecutables == nil || ctx.logger == nil {
		return 1
	}

	windowList := ctx.windows

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
		// Get window title
		titleLength, _, _ := procGetWindowTextLength.Call(uintptr(hwnd))
		title := ""
		if titleLength > 0 {
			titleBuf := make([]uint16, titleLength+1)
			ret, _, _ := procGetWindowText.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&titleBuf[0])), uintptr(len(titleBuf)))
			if ret > 0 {
				title = windows.UTF16ToString(titleBuf)
			}
		}

		// Get window class name
		classNameBuf := make([]uint16, 256)
		ret, _, _ := procGetClassName.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&classNameBuf[0])), uintptr(len(classNameBuf)))
		className := ""
		if ret > 0 {
			className = windows.UTF16ToString(classNameBuf)
		}

		windowInfo := WindowInfo{
			HWND:       win.HWND(hwnd),
			Title:      title,
			ClassName:  className,
			ProcessID:  pid,
			Executable: exeBase,
		}

		*windowList = append(*windowList, windowInfo)
	}

	return 1
}

// isWindowValid checks if a cached window handle is still valid and visible
func (tm *TeamsManager) isWindowValid(hWnd win.HWND) bool {
	if hWnd == 0 {
		return false
	}

	// Check if window is visible
	ret, _, _ := procIsWindowVisible.Call(uintptr(hWnd))
	if ret == 0 {
		return false
	}

	// Verify window still exists by checking if we can get its thread/process ID
	var pid uint32
	threadID, _, _ := procGetWindowThreadProcessID.Call(uintptr(hWnd), uintptr(unsafe.Pointer(&pid)))
	if threadID == 0 || pid == 0 {
		return false
	}

	return true
}

// validateCachedWindows filters out invalid window handles from cache
func (tm *TeamsManager) validateCachedWindows(cached []win.HWND) []win.HWND {
	valid := make([]win.HWND, 0, len(cached))
	for _, hWnd := range cached {
		if tm.isWindowValid(hWnd) {
			valid = append(valid, hWnd)
		} else {
			tm.logger.Debug("Removing invalid window from cache",
				slog.Uint64("hwnd", uint64(uintptr(hWnd))))
		}
	}
	return valid
}

// isCacheExpired checks if the window cache has exceeded its TTL
func (tm *TeamsManager) isCacheExpired() bool {
	if tm.windowCache == nil {
		return true
	}

	now := time.Now()
	if tm.now != nil {
		now = tm.now()
	}

	ttl := tm.cacheTTL
	if ttl == 0 {
		ttl = windowCacheTTLDefault
	}

	tm.windowCache.mutex.RLock()
	expired := now.Sub(tm.windowCache.timestamp) > ttl
	tm.windowCache.mutex.RUnlock()

	return expired
}

// updateWindowCache stores new window handles in the cache with current timestamp
func (tm *TeamsManager) updateWindowCache(handles []win.HWND) {
	if tm.windowCache == nil {
		return
	}

	now := time.Now()
	if tm.now != nil {
		now = tm.now()
	}

	tm.windowCache.mutex.Lock()
	tm.windowCache.handles = handles
	tm.windowCache.timestamp = now
	tm.windowCache.mutex.Unlock()

	tm.logger.Debug("Updated window cache",
		slog.Int("count", len(handles)),
		slog.Time("timestamp", now))
}

// getCachedWindows returns cached window handles if valid, otherwise returns nil
func (tm *TeamsManager) getCachedWindows() []win.HWND {
	if tm.windowCache == nil {
		return nil
	}

	// Check if cache is expired
	if tm.isCacheExpired() {
		tm.logger.Debug("Window cache expired")
		return nil
	}

	tm.windowCache.mutex.RLock()
	cached := tm.windowCache.handles
	tm.windowCache.mutex.RUnlock()

	// Validate cached handles
	if len(cached) == 0 {
		return nil
	}

	validated := tm.validateCachedWindows(cached)
	if len(validated) == 0 {
		tm.logger.Debug("No valid windows in cache after validation")
		return nil
	}

	// Update cache with validated handles if some were removed
	if len(validated) != len(cached) {
		tm.updateWindowCache(validated)
	}

	tm.logger.Debug("Using cached window handles",
		slog.Int("count", len(validated)))

	return validated
}

func (tm *TeamsManager) FindTeamsWindows() []win.HWND {
	// Try to use cached windows first
	if cached := tm.getCachedWindows(); cached != nil {
		return cached
	}

	// Cache miss or expired - perform full enumeration
	tm.logger.Debug("Cache miss - performing full window enumeration")
	var windowList []WindowInfo
	tm.enumContext.windows = &windowList

	ret, _, err := procEnumWindows.Call(
		syscall.NewCallback(enumWindowsProc),
		uintptr(unsafe.Pointer(tm.enumContext)),
	)
	if ret == 0 {
		tm.logger.Debug("EnumWindows failed", slog.String("error", err.Error()))
		// Ensure we always return a valid slice, never nil
		return make([]win.HWND, 0)
	}

	// Extract HWNDs from WindowInfo structs
	var hwnds []win.HWND
	for _, window := range windowList {
		hwnds = append(hwnds, window.HWND)
	}

	// Ensure we always return a valid slice, never nil
	if hwnds == nil {
		hwnds = make([]win.HWND, 0)
	}

	// Update cache with newly enumerated windows (even if empty)
	tm.updateWindowCache(hwnds)
	if len(hwnds) > 0 {
		tm.logger.Debug("Found and cached Teams windows",
			slog.Int("count", len(hwnds)))
	} else {
		tm.logger.Debug("No Teams windows found, cached empty result")
	}

	return hwnds
}

func (tm *TeamsManager) setWindowFocusFromPIP(hWnd win.HWND) error {
	const (
		maxRetries = 3
		retryDelay = 150 * time.Millisecond
		hwndTop    = uintptr(0)
		swpNosize  = 0x0001
		swpNomove  = 0x0002
	)

	// Get thread IDs for AttachThreadInput
	var targetPID uint32
	targetThreadID, _, _ := procGetWindowThreadProcessID.Call(uintptr(hWnd), uintptr(unsafe.Pointer(&targetPID)))
	currentThreadID, _, _ := procGetCurrentThreadID.Call()

	// Get the PIP window's thread ID
	currentFocus, _, _ := procGetForegroundWindow.Call()
	var pipPID uint32
	pipThreadID, _, _ := procGetWindowThreadProcessID.Call(currentFocus, uintptr(unsafe.Pointer(&pipPID)))

	tm.logger.Debug("Attempting focus switch from PIP window",
		slog.Uint64("pip_hwnd", uint64(currentFocus)),
		slog.Uint64("pip_thread", uint64(pipThreadID)),
		slog.Uint64("target_hwnd", uint64(uintptr(hWnd))),
		slog.Uint64("target_thread", uint64(targetThreadID)),
		slog.Uint64("current_thread", uint64(currentThreadID)))

	for attempt := range maxRetries {
		// Method 1: Try SetWindowPos with HWND_TOP to force window ordering
		ret, _, _ := procSetWindowPos.Call(
			uintptr(hWnd),
			hwndTop,
			0, 0, 0, 0,
			swpNomove|swpNosize)
		if ret != 0 {
			time.Sleep(50 * time.Millisecond)
		}

		// Method 2: Triple thread attach - attach our thread to PIP, then PIP to target
		if pipThreadID != 0 && targetThreadID != 0 && currentThreadID != 0 {
			// First attach our thread to the PIP window's thread
			attach1, _, _ := procAttachThreadInput.Call(currentThreadID, pipThreadID, 1)
			if attach1 != 0 {
				// Then attach PIP thread to target thread
				attach2, _, _ := procAttachThreadInput.Call(pipThreadID, targetThreadID, 1)
				if attach2 != 0 {
					// Now try to set foreground with all threads attached
					_, _, _ = procSetForegroundWindow.Call(uintptr(hWnd))
					time.Sleep(75 * time.Millisecond)

					// Detach in reverse order
					_, _, _ = procAttachThreadInput.Call(pipThreadID, targetThreadID, 0)
				}
				_, _, _ = procAttachThreadInput.Call(currentThreadID, pipThreadID, 0)
			}
		}

		// Method 3: Direct AttachThreadInput between current and target
		if targetThreadID != currentThreadID {
			attachRet, _, _ := procAttachThreadInput.Call(currentThreadID, targetThreadID, 1)
			if attachRet != 0 {
				_, _, _ = procAllowSetForegroundWindow.Call(uintptr(targetPID))
				_, _, _ = procBringWindowToTop.Call(uintptr(hWnd))
				_, _, _ = procSetForegroundWindow.Call(uintptr(hWnd))

				// Always detach
				_, _, _ = procAttachThreadInput.Call(currentThreadID, targetThreadID, 0)

				time.Sleep(75 * time.Millisecond)
			}
		}

		// Check if focus was successfully set
		for i := range foregroundVerificationChecks {
			focusedWindow, _, _ := procGetForegroundWindow.Call()
			if focusedWindow == uintptr(hWnd) {
				tm.logger.Debug("Successfully set focus from PIP window",
					slog.Uint64("hwnd", uint64(uintptr(hWnd))),
					slog.Int("attempt", attempt+1),
					slog.Int("verify_attempt", i+1))
				return nil
			}
			if i < 2 {
				time.Sleep(25 * time.Millisecond)
			}
		}

		// Log failure details for debugging
		if attempt < maxRetries-1 {
			currentFocusWindow, _, _ := procGetForegroundWindow.Call()
			tm.logger.Debug("PIP focus attempt failed, retrying",
				slog.Uint64("hwnd", uint64(uintptr(hWnd))),
				slog.Uint64("current_focus", uint64(currentFocusWindow)),
				slog.Int("attempt", attempt+1))
			time.Sleep(retryDelay)
		}
	}

	return fmt.Errorf("failed to set focus from PIP window after %d attempts", maxRetries)
}

// enhancedAttachFocus attempts a more forceful yet controlled foreground switch using
// staged AttachThreadInput and optional BringWindowToTop only on escalation.
// canEscalateStage3 determines whether stage 3 focus escalation is permitted.
// Returns (allowed, reason). If not allowed, reason is a concise identifier (cooldown, minimized, no_tracking).
func (tm *TeamsManager) canEscalateStage3(hWnd win.HWND) (bool, string) {
	if hWnd == 0 {
		return false, "invalid_hwnd"
	}
	// Check minimized state first (avoid flashing/restoring minimized windows)
	isMinimized, _, _ := procIsIconic.Call(uintptr(hWnd))
	if isMinimized != 0 {
		return false, "minimized"
	}
	if tm.lastEscalation == nil {
		return true, "ok"
	}
	last, ok := tm.lastEscalation[hWnd]
	if !ok {
		return true, "ok"
	}
	now := time.Now()
	if tm.now != nil {
		now = tm.now()
	}
	cd := tm.escalationCooldown
	if cd == 0 {
		cd = escalationCooldownDefault
	}
	if now.Sub(last) < cd {
		return false, "cooldown"
	}
	return true, "ok"
}

func (tm *TeamsManager) enhancedAttachFocus(hWnd win.HWND) error {
	if hWnd == 0 {
		return fmt.Errorf("invalid window handle")
	}
	var targetPID uint32
	targetThreadID, _, _ := procGetWindowThreadProcessID.Call(uintptr(hWnd), uintptr(unsafe.Pointer(&targetPID)))
	currentThreadID, _, _ := procGetCurrentThreadID.Call()
	if targetThreadID == 0 || currentThreadID == 0 {
		return fmt.Errorf("thread id lookup failed")
	}

	// Local constants for staged escalation (kept function‑scoped to avoid polluting package scope)
	const (
		swRestore     = 9
		swMinimize    = 6 // SW_MINIMIZE (different from SW_SHOWMINIMIZED=2 for a harder minimize)
		hwndTop       = uintptr(0)
		hwndTopmost   = ^uintptr(0) // -1
		hwndNotopmost = ^uintptr(1) // -2
		swpNosize     = 0x0001
		swpNomove     = 0x0002
		swpNoactivate = 0x0010
	)

	start := time.Now()

	// -----------------------------
	// Stage 1: Gentle attempt with ShowWindow cycle + AllowSetForeground + SetForegroundWindow
	// Rationale: Some browsers / Electron contexts relinquish focus more reliably after a visibility state toggle.
	isMinimized, _, _ := procIsIconic.Call(uintptr(hWnd))
	if isMinimized != 0 {
		// If already minimized, just restore (avoid extra flicker)
		_, _, _ = procShowWindow.Call(uintptr(hWnd), swRestore)
		time.Sleep(50 * time.Millisecond)
	} else {
		// Minimize + restore cycle to create a recent user-like interaction timestamp
		_, _, _ = procShowWindow.Call(uintptr(hWnd), swMinimize)
		time.Sleep(35 * time.Millisecond)
		_, _, _ = procShowWindow.Call(uintptr(hWnd), swRestore)
		time.Sleep(55 * time.Millisecond)
	}
	_, _, _ = procAllowSetForegroundWindow.Call(uintptr(targetPID))
	_, _, _ = procSetForegroundWindow.Call(uintptr(hWnd))
	time.Sleep(90 * time.Millisecond) // slightly longer than original 60ms to survive foreground lock timing
	fw, _, _ := procGetForegroundWindow.Call()
	if fw == uintptr(hWnd) {
		if tm.logger != nil {
			elapsed := time.Since(start)
			tm.logger.Debug("Focus escalation stage 1 success",
				slog.Uint64("hwnd", uint64(uintptr(hWnd))),
				slog.Int("stage", 1),
				slog.Duration("elapsed", elapsed))
		}
		return nil
	}

	// -----------------------------
	// Stage 2: Thread attach + Z-order nudges (TOP -> TOPMOST -> NOTOPMOST) + foreground set
	attachRet, _, _ := procAttachThreadInput.Call(currentThreadID, targetThreadID, 1)
	if attachRet != 0 {
		// Z-order manipulations without activation to influence foreground eligibility
		_, _, _ = procSetWindowPos.Call(uintptr(hWnd), hwndTop, 0, 0, 0, 0, uintptr(swpNomove|swpNosize|swpNoactivate))
		time.Sleep(25 * time.Millisecond)
		_, _, _ = procSetWindowPos.Call(uintptr(hWnd), hwndTopmost, 0, 0, 0, 0, uintptr(swpNomove|swpNosize|swpNoactivate))
		time.Sleep(25 * time.Millisecond)
		_, _, _ = procSetWindowPos.Call(uintptr(hWnd), hwndNotopmost, 0, 0, 0, 0, uintptr(swpNomove|swpNosize|swpNoactivate))
		// Final foreground attempt for stage 2
		_, _, _ = procSetForegroundWindow.Call(uintptr(hWnd))
		time.Sleep(110 * time.Millisecond)
		fw2, _, _ := procGetForegroundWindow.Call()
		// Always detach after attempt
		_, _, _ = procAttachThreadInput.Call(currentThreadID, targetThreadID, 0)
		if fw2 == uintptr(hWnd) {
			if tm.logger != nil {
				elapsed := time.Since(start)
				tm.logger.Debug("Focus escalation stage 2 success",
					slog.Uint64("hwnd", uint64(uintptr(hWnd))),
					slog.Int("stage", 2),
					slog.Duration("elapsed", elapsed))
			}
			return nil
		}
	}

	// -----------------------------
	// Stage 3 eligibility (cooldown / minimized avoidance)
	if ok, reason := tm.canEscalateStage3(hWnd); !ok {
		if tm.logger != nil {
			elapsed := time.Since(start)
			tm.logger.Debug("Focus escalation stage 3 skipped",
				slog.Uint64("hwnd", uint64(uintptr(hWnd))),
				slog.Int("stage", 3),
				slog.String("reason", reason),
				slog.Duration("elapsed", elapsed))
		}
		return errStage3Skipped
	}

	// -----------------------------
	// Stage 3: Full escalation – reattach, window state cycle (if not minimized), Z-order nudges, BringWindowToTop, foreground
	attachRet2, _, _ := procAttachThreadInput.Call(currentThreadID, targetThreadID, 1)
	if attachRet2 != 0 {
		// Repeat a visibility cycle only if window is currently not minimized (avoid unnecessary flicker of user-minimized windows)
		isMinimized2, _, _ := procIsIconic.Call(uintptr(hWnd))
		if isMinimized2 == 0 {
			_, _, _ = procShowWindow.Call(uintptr(hWnd), swMinimize)
			time.Sleep(40 * time.Millisecond)
			_, _, _ = procShowWindow.Call(uintptr(hWnd), swRestore)
			time.Sleep(60 * time.Millisecond)
		} else {
			// Ensure restored for key processing
			_, _, _ = procShowWindow.Call(uintptr(hWnd), swRestore)
			time.Sleep(50 * time.Millisecond)
		}

		// Aggressive Z-order sequence
		_, _, _ = procSetWindowPos.Call(uintptr(hWnd), hwndTop, 0, 0, 0, 0, uintptr(swpNomove|swpNosize|swpNoactivate))
		time.Sleep(25 * time.Millisecond)
		_, _, _ = procSetWindowPos.Call(uintptr(hWnd), hwndTopmost, 0, 0, 0, 0, uintptr(swpNomove|swpNosize|swpNoactivate))
		time.Sleep(30 * time.Millisecond)
		_, _, _ = procSetWindowPos.Call(uintptr(hWnd), hwndNotopmost, 0, 0, 0, 0, uintptr(swpNomove|swpNosize|swpNoactivate))
		time.Sleep(25 * time.Millisecond)

		// Bring to top then foreground
		_, _, _ = procBringWindowToTop.Call(uintptr(hWnd))
		_, _, _ = procSetForegroundWindow.Call(uintptr(hWnd))
		time.Sleep(150 * time.Millisecond)
		fw3, _, _ := procGetForegroundWindow.Call()
		_, _, _ = procAttachThreadInput.Call(currentThreadID, targetThreadID, 0)
		if fw3 == uintptr(hWnd) {
			if tm.lastEscalation != nil {
				now := time.Now()
				if tm.now != nil {
					now = tm.now()
				}
				tm.lastEscalation[hWnd] = now
			}
			if tm.logger != nil {
				elapsed := time.Since(start)
				tm.logger.Debug("Focus escalation stage 3 success",
					slog.Uint64("hwnd", uint64(uintptr(hWnd))),
					slog.Int("stage", 3),
					slog.Duration("elapsed", elapsed))
			}
			return nil
		}
	}

	if tm.logger != nil {
		elapsed := time.Since(start)
		tm.logger.Debug("Focus escalation failed",
			slog.Uint64("hwnd", uint64(uintptr(hWnd))),
			slog.Duration("elapsed", elapsed))
	}
	return fmt.Errorf("enhanced focus attempts failed")
}

func (tm *TeamsManager) setWindowFocus(hWnd win.HWND) error {
	const (
		maxRetries = 3
		retryDelay = 100 * time.Millisecond
	)

	// First, check if window already has focus
	currentFocus, _, _ := procGetForegroundWindow.Call()
	if currentFocus == uintptr(hWnd) {
		return nil
	}

	// Get thread IDs for AttachThreadInput fallback
	var targetPID uint32
	targetThreadID, _, _ := procGetWindowThreadProcessID.Call(uintptr(hWnd), uintptr(unsafe.Pointer(&targetPID)))
	currentThreadID, _, _ := procGetCurrentThreadID.Call()

	for attempt := range maxRetries {
		// Allow the target process to set foreground window
		allowRet, _, _ := procAllowSetForegroundWindow.Call(uintptr(targetPID))
		if allowRet == 0 {
			tm.logger.Debug("AllowSetForegroundWindow failed (expected on restricted systems)",
				slog.Uint64("pid", uint64(targetPID)))
		}

		// Stealth mode: skip BringWindowToTop to avoid flashing
		// Try normal SetForegroundWindow first
		setFgRet, _, setFgErr := procSetForegroundWindow.Call(uintptr(hWnd))

		// Give Windows more time to process the focus change for Electron apps
		time.Sleep(75 * time.Millisecond)

		// Check multiple times with short intervals to catch delayed focus changes
		for i := range foregroundVerificationChecks {
			focusedWindow, _, _ := procGetForegroundWindow.Call()
			if focusedWindow == uintptr(hWnd) {
				tm.logger.Debug("Focus set successfully with SetForegroundWindow",
					slog.Uint64("hwnd", uint64(uintptr(hWnd))),
					slog.Int("attempt", attempt+1),
					slog.Int("verify_attempt", i+1))
				return nil
			}
			if i < 2 { // Don't sleep on the last check
				time.Sleep(25 * time.Millisecond)
			}
		}

		// If direct approach failed, try AttachThreadInput as fallback
		if targetThreadID != currentThreadID {
			// Attach our thread input to the target window's thread
			attachRet, _, _ := procAttachThreadInput.Call(currentThreadID, targetThreadID, 1)
			if attachRet != 0 {
				// Now try to set focus with attached threads
				attachSetRet, _, _ := procSetForegroundWindow.Call(uintptr(hWnd))

				// Always detach threads, even if focus setting failed
				detachRet, _, _ := procAttachThreadInput.Call(currentThreadID, targetThreadID, 0)
				if detachRet == 0 {
					tm.logger.Debug("Failed to detach thread input",
						slog.Uint64("current_thread", uint64(currentThreadID)),
						slog.Uint64("target_thread", uint64(targetThreadID)))
				}

				if attachSetRet != 0 {
					// Verify focus was set with longer delay and multiple checks
					time.Sleep(75 * time.Millisecond)
					for i := range foregroundVerificationChecks {
						focusedWindow, _, _ := procGetForegroundWindow.Call()
						if focusedWindow == uintptr(hWnd) {
							tm.logger.Debug("Focus set successfully with AttachThreadInput",
								slog.Uint64("hwnd", uint64(uintptr(hWnd))),
								slog.Int("attempt", attempt+1),
								slog.Int("verify_attempt", i+1))
							return nil
						}
						if i < 2 { // Don't sleep on the last check
							time.Sleep(25 * time.Millisecond)
						}
					}
				}
			} else {
				tm.logger.Debug("Failed to attach thread input",
					slog.Uint64("current_thread", uint64(currentThreadID)),
					slog.Uint64("target_thread", uint64(targetThreadID)))
			}
		}

		// Log failure details for debugging, but only if not last attempt
		if attempt < maxRetries-1 {
			currentFocusWindow, _, _ := procGetForegroundWindow.Call()
			tm.logger.Debug("Focus attempt failed, retrying",
				slog.String("setfg_error", setFgErr.Error()),
				slog.Bool("setfg_success", setFgRet != 0),
				slog.Bool("allow_success", allowRet != 0),
				slog.Uint64("hwnd", uint64(uintptr(hWnd))),
				slog.Uint64("current_focus", uint64(currentFocusWindow)),
				slog.Int("attempt", attempt+1))
			time.Sleep(retryDelay)
		}
	}

	// Final attempt: sometimes focus works on the last try even if API calls fail
	finalFocus, _, _ := procGetForegroundWindow.Call()
	if finalFocus == uintptr(hWnd) {
		tm.logger.Debug("Focus was set despite API failures",
			slog.Uint64("hwnd", uint64(uintptr(hWnd))))
		return nil
	}

	return fmt.Errorf("failed to set focus after %d attempts", maxRetries)
}

func (tm *TeamsManager) isPIPWindow(hWnd win.HWND) bool {
	// Add safety check for invalid window handle
	if hWnd == 0 {
		return false
	}

	// Get process information to check if it's a browser
	var pid uint32
	_, _, _ = procGetWindowThreadProcessID.Call(uintptr(hWnd), uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return false
	}

	hProcess, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil || hProcess == 0 {
		return false
	}
	defer func() {
		_ = windows.CloseHandle(hProcess)
	}()

	var exeName [windows.MAX_PATH]uint16
	size := uint32(len(exeName))
	if err := windows.QueryFullProcessImageName(hProcess, 0, &exeName[0], &size); err != nil {
		return false
	}

	exePath := windows.UTF16ToString(exeName[:size])
	lastSlash := strings.LastIndexByte(exePath, '\\')
	var exeBase string
	if lastSlash != -1 {
		exeBase = strings.ToLower(exePath[lastSlash+1:])
	} else {
		exeBase = strings.ToLower(exePath)
	}

	// Known browser executables that can have PIP windows
	browserExecutables := []string{
		"zen.exe",
		"firefox.exe",
		"chrome.exe",
		"msedge.exe",
		"brave.exe",
		"opera.exe",
		"vivaldi.exe",
		"waterfox.exe",
	}

	isBrowser := slices.Contains(browserExecutables, exeBase)

	const (
		gwlExstyle     = uintptr(0xFFFFFFEC) // -20 as unsigned
		wsExTopmost    = 0x00000008
		wsExToolwindow = 0x00000080
	)

	// Get window dimensions first
	var rect win.RECT
	if !win.GetWindowRect(hWnd, &rect) {
		return false
	}

	width := int(rect.Right - rect.Left)
	height := int(rect.Bottom - rect.Top)

	// Check if window has topmost attribute (common for PIP windows)
	exStyle, _, _ := procGetWindowLong.Call(uintptr(hWnd), gwlExstyle)
	hasTopmost := exStyle&wsExTopmost != 0

	// Skip tool windows (they're usually system UI elements)
	if exStyle&wsExToolwindow != 0 {
		return false
	}

	// Enhanced PIP detection logic
	if isBrowser {
		// For browsers, look for small topmost windows (typical PIP behavior)
		if hasTopmost && width > 120 && height > 80 && width < 1000 && height < 800 {
			tm.logger.Debug("Detected browser PIP window by process and attributes",
				slog.String("browser", exeBase),
				slog.Int("width", width),
				slog.Int("height", height),
				slog.Bool("topmost", hasTopmost),
				slog.Uint64("hwnd", uint64(uintptr(hWnd))))
			return true
		}

		// Also check for small floating browser windows that might not be topmost
		// Some browsers don't always set topmost for PIP
		if width > 200 && height > 150 && width < 600 && height < 400 {
			// Additional check: small browser windows are likely PIP
			aspectRatio := float64(width) / float64(height)
			if aspectRatio > 1.2 && aspectRatio < 3.0 { // Typical video aspect ratios
				tm.logger.Debug("Detected likely browser PIP window by size and aspect ratio",
					slog.String("browser", exeBase),
					slog.Int("width", width),
					slog.Int("height", height),
					slog.Float64("aspect_ratio", aspectRatio),
					slog.Bool("topmost", hasTopmost),
					slog.Uint64("hwnd", uint64(uintptr(hWnd))))
				return true
			}
		}
	} else {
		// For non-browser windows, use the original logic (topmost + size)
		if hasTopmost && width > 120 && height > 80 && width < 800 && height < 600 {
			tm.logger.Debug("Detected likely PIP window by size and topmost attribute",
				slog.String("process", exeBase),
				slog.Int("width", width),
				slog.Int("height", height),
				slog.Uint64("hwnd", uint64(uintptr(hWnd))))
			return true
		}
	}

	return false
}

func (tm *TeamsManager) shouldRestoreFocus(hWnd win.HWND) bool {
	if hWnd == 0 {
		return false
	}

	// Add safety check to prevent panic
	defer func() {
		if r := recover(); r != nil {
			tm.logger.Debug("Panic recovered in shouldRestoreFocus",
				slog.Any("panic", r),
				slog.Uint64("hwnd", uint64(uintptr(hWnd))))
		}
	}()

	// Don't restore focus to PIP windows as they can cause focus conflicts
	if tm.isPIPWindow(hWnd) {
		tm.logger.Debug("Skipping focus restoration to PIP window",
			slog.Uint64("hwnd", uint64(uintptr(hWnd))))
		return false
	}

	// Check if window is still visible and valid
	ret, _, _ := procIsWindowVisible.Call(uintptr(hWnd))
	if ret == 0 {
		tm.logger.Debug("Skipping focus restoration to invisible window",
			slog.Uint64("hwnd", uint64(uintptr(hWnd))))
		return false
	}

	return true
}

func (tm *TeamsManager) SendKeysToTeams(ctx context.Context, state *websocket.ServiceState) error {
	// Add panic recovery at the top level
	defer func() {
		if r := recover(); r != nil {
			tm.logger.Error("Panic recovered in SendKeysToTeams",
				slog.Any("panic", r))
		}
	}()

	// Check context first
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Check for user input activity first - detects keyboard, mouse, and touch input
	inputThreshold := 2000 * time.Millisecond
	if tm.config != nil {
		inputThreshold = tm.config.GetInputThreshold()
	}
	inputDetector := NewInputDetectorWithThreshold(tm.logger, inputThreshold)
	if inputDetector.IsUserInputActive() {
		tm.logger.Debug("User input detected, deferring Teams key send to avoid interference")
		return ErrUserInputActive
	}

	// Support global activity mode: send key without any window focus changes
	if tm.config != nil && tm.config.GetActivityMode() == "global" {
		const vkF15 = 0x7E
		if err := sendVirtualKey(uint16(vkF15)); err != nil {
			return fmt.Errorf("global key send failed: %w", err)
		}
		tm.logger.Debug("Sent global F15 key (activity-mode=global)")
		return nil
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

	// Enhanced PIP window detection and logging
	if currentWindow != 0 {
		currentWindowHandle := win.HWND(currentWindow)

		// Log current window details for debugging
		var pid uint32
		_, _, _ = procGetWindowThreadProcessID.Call(currentWindow, uintptr(unsafe.Pointer(&pid)))
		if pid != 0 {
			if hProcess, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid); err == nil && hProcess != 0 {
				var exeName [windows.MAX_PATH]uint16
				size := uint32(len(exeName))
				if err := windows.QueryFullProcessImageName(hProcess, 0, &exeName[0], &size); err == nil {
					exePath := windows.UTF16ToString(exeName[:size])
					lastSlash := strings.LastIndexByte(exePath, '\\')
					var exeBase string
					if lastSlash != -1 {
						exeBase = exePath[lastSlash+1:]
					} else {
						exeBase = exePath
					}
					tm.logger.Debug("Current foreground before focus attempt",
						slog.Uint64("fg_hwnd", uint64(currentWindow)),
						slog.Uint64("fg_pid", uint64(pid)),
						slog.String("fg_exe", exeBase))
				}
				if err := windows.CloseHandle(hProcess); err != nil {
					tm.logger.Debug("Failed to close process handle in current window detection",
						slog.String("error", err.Error()),
						slog.Uint64("pid", uint64(pid)))
				}
			}
		}

		// Log if current window is a PIP window for debugging
		if tm.isPIPWindow(currentWindowHandle) {
			tm.logger.Debug("Detected PIP window has focus, will use specialized focus switching",
				slog.Uint64("pip_hwnd", uint64(currentWindow)))
		}
	}

	successCount := 0
	var lastError error

	for _, hWnd := range hwnds {
		if hWnd == 0 {
			continue
		}

		isMinimized, _, _ := procIsIconic.Call(uintptr(hWnd))
		isMaximized, _, _ := procIsZoomed.Call(uintptr(hWnd))

		currentFocus, _, _ := procGetForegroundWindow.Call()
		// Foreground PID for suppression logic
		var fgPID uint32
		_, _, _ = procGetWindowThreadProcessID.Call(currentFocus, uintptr(unsafe.Pointer(&fgPID)))

		// Suppress intrusive focus attempts if recent repeated failures
		if tm.shouldSuppressFocus(hWnd, fgPID) {
			tm.logger.Debug("Suppressing focus attempts due to recent failures",
				slog.Uint64("hwnd", uint64(uintptr(hWnd))),
				slog.Uint64("current_focus", uint64(currentFocus)),
				slog.Int("failure_streak", tm.focusFailures[hWnd].failCount))
			// Fallback: attempt background key send only
			if err := sendVirtualKey(uint16(vkF15)); err == nil {
				successCount++
				tm.logger.Debug("Sent F15 in suppressed background mode",
					slog.Uint64("hwnd", uint64(uintptr(hWnd))))
			}
			continue
		}

		tm.logger.Debug("Teams window focus check before key send",
			slog.Uint64("teams_hwnd", uint64(uintptr(hWnd))),
			slog.Uint64("current_focus", uint64(currentFocus)),
			slog.Bool("has_focus", currentFocus == uintptr(hWnd)))

		if currentFocus == uintptr(hWnd) {
			if err := sendVirtualKey(uint16(vkF15)); err != nil {
				lastError = err
				tm.recordFocusFailure(hWnd, fgPID)
			} else {
				successCount++
				tm.resetFocusFailure(hWnd)
			}
			continue
		}

		// Track original state for potential restore
		originalState := swShowNormal
		wasMinimized := isMinimized != 0
		if wasMinimized {
			// Restore minimized window briefly (stealth with delay)
			_, _, _ = procShowWindow.Call(uintptr(hWnd), swRestore)
			if restoreDelay > 0 {
				time.Sleep(restoreDelay)
			}
		} else if isMaximized != 0 {
			originalState = swShowMaximized
		}

		currentFocus, _, _ = procGetForegroundWindow.Call()
		isPIPBlocking := tm.isPIPWindow(win.HWND(currentFocus))

		var focusErr error
		var verifyFocus uintptr

		if isPIPBlocking {
			focusErr = tm.setWindowFocusFromPIP(hWnd)
		} else {
			// First a quick standard attempt (single try) to minimize flashing
			_ = tm.setWindowFocus(hWnd)
			verify, _, _ := procGetForegroundWindow.Call()
			if verify != uintptr(hWnd) {
				// Escalate using enhancedAttachFocus
				focusErr = tm.enhancedAttachFocus(hWnd)
			} else {
				focusErr = nil
			}
		}

		if focusErr != nil {
			// Special handling: stage 3 escalation skipped is not a hard failure; treat as background send
			if errors.Is(focusErr, errStage3Skipped) {
				if err := sendVirtualKey(uint16(vkF15)); err == nil {
					successCount++
					if tm.logger != nil {
						tm.logger.Debug("Sent F15 after non-escalated focus attempt",
							slog.Uint64("hwnd", uint64(uintptr(hWnd))),
							slog.String("escalation", "skipped"))
					}
				} else {
					lastError = err
				}
				goto restoreState
			}
			// Record failure and attempt background key fallback after stabilization
			tm.recordFocusFailure(hWnd, fgPID)
			if restoreDelay > 0 {
				time.Sleep(restoreDelay)
			}
			if err := sendVirtualKey(uint16(vkF15)); err == nil {
				successCount++
			} else {
				lastError = err
			}
			goto restoreState
		}

		verifyFocus, _, _ = procGetForegroundWindow.Call()
		if verifyFocus != uintptr(hWnd) {
			// Treat as failure for suppression tracking
			tm.recordFocusFailure(hWnd, fgPID)
			if err := sendVirtualKey(uint16(vkF15)); err == nil {
				successCount++
			} else {
				lastError = err
			}
			goto restoreState
		}

		// Focus succeeded
		tm.resetFocusFailure(hWnd)
		time.Sleep(focusDelay)
		if err := sendVirtualKey(uint16(vkF15)); err != nil {
			lastError = err
		} else {
			successCount++
		}

	restoreState:
		// Re-minimize if we restored from minimized
		if wasMinimized {
			_, _, _ = procShowWindow.Call(uintptr(hWnd), swShowMinimized)
		}
		// Re-maximize not necessary; restoring minimized uses normal show
		_ = originalState
	}

	// Configurable delay before restoring focus to ensure key event is processed by Teams
	time.Sleep(keyProcessDelay)

	// Restore focus to original window, but only if it's safe to do so
	if currentWindow != 0 && tm.shouldRestoreFocus(win.HWND(currentWindow)) {
		ret, _, callErr := procSetForegroundWindow.Call(currentWindow)
		if ret == 0 {
			lastErr := syscall.GetLastError()
			if errnoVal, ok := lastErr.(syscall.Errno); ok && errnoVal != 0 {
				// We have a real Windows error code
				tm.logger.Debug("Failed to restore focus to original window",
					slog.Uint64("hwnd", uint64(currentWindow)),
					slog.Int("last_error_code", int(errnoVal)),
					slog.String("error", errnoVal.Error()))
			} else {
				// Either no error or not an errno; treat as focus blocked by heuristics
				tm.logger.Debug("Restore focus blocked by OS focus rules",
					slog.String("detail", "SetForegroundWindow returned 0 with no actionable last error"),
					slog.Uint64("hwnd", uint64(currentWindow)))
			}
			_ = callErr // suppress unused warning
		} else {
			tm.logger.Debug("Successfully restored focus to original window",
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
