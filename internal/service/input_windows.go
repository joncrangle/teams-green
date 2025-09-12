package service

import (
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

// sendVirtualKey uses the Windows SendInput API to synthesize a key press/release pair for the
// specified virtual key code. This is a replacement for the legacy keybd_event usage.
func sendVirtualKey(vk uint16) error {
	user32 := syscall.NewLazyDLL("user32.dll")
	procSendInput := user32.NewProc("SendInput")

	type keyboardInput struct {
		Type      uint32
		_         [4]byte // padding to align to 8-byte boundary like C INPUT union
		Vk        uint16
		Scan      uint16
		Flags     uint32
		Time      uint32
		ExtraInfo uintptr
		_         [8]byte // tail padding so total size is 40 bytes on amd64 (matches Win32 INPUT)
	}

	const expectedKeyboardInputSize = 40

	const (
		inputKeyboard  = 1
		keyeventfKeyup = 0x0002
	)

	// Validate struct size at runtime; mismatch causes ERROR_INVALID_PARAMETER
	if int(unsafe.Sizeof(keyboardInput{})) != expectedKeyboardInputSize {
		return fmt.Errorf("keyboardInput struct size=%d expected=%d", unsafe.Sizeof(keyboardInput{}), expectedKeyboardInputSize)
	}

	// Key down
	inputs := []keyboardInput{{
		Type:  inputKeyboard,
		Vk:    vk,
		Scan:  0,
		Flags: 0,
		Time:  0,
	}}

	n, _, err := procSendInput.Call(
		uintptr(len(inputs)),
		uintptr(unsafe.Pointer(&inputs[0])),
		unsafe.Sizeof(inputs[0]),
	)
	if n == 0 {
		lastErr := syscall.GetLastError()
		return fmt.Errorf("SendInput key down failed: %v (lastError=%v structSize=%d)", err, lastErr, unsafe.Sizeof(inputs[0]))
	}

	// Small delay to mimic natural key press
	time.Sleep(1 * time.Millisecond)

	// Key up
	inputs[0].Flags = keyeventfKeyup
	n, _, err = procSendInput.Call(
		uintptr(len(inputs)),
		uintptr(unsafe.Pointer(&inputs[0])),
		unsafe.Sizeof(inputs[0]),
	)
	if n == 0 {
		lastErr := syscall.GetLastError()
		return fmt.Errorf("SendInput key up failed: %v (lastError=%v structSize=%d)", err, lastErr, unsafe.Sizeof(inputs[0]))
	}
	return nil
}
