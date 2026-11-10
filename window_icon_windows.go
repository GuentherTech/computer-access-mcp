//go:build windows && !headless

package main

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const desktopAppID = "GuentherTech.ComputerAccess"

var (
	iconUser32       = windows.NewLazySystemDLL("user32.dll")
	iconLoadImage    = iconUser32.NewProc("LoadImageW")
	iconPostMessage  = iconUser32.NewProc("PostMessageW")
	iconWindowDPI    = iconUser32.NewProc("GetDpiForWindow")
	iconModuleHandle = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW")
	iconAppID        = windows.NewLazySystemDLL("shell32.dll").NewProc("SetCurrentProcessExplicitAppUserModelID")
)

func setDesktopAppIdentity() {
	id, _ := windows.UTF16PtrFromString(desktopAppID)
	iconAppID.Call(uintptr(unsafe.Pointer(id)))
	runtime.KeepAlive(id)
}

// Gio supplies a window class icon. Also set the per-window small and large
// icons explicitly, including when the main window is recreated from the tray.
func setDesktopWindowIcons(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	module, _, _ := iconModuleHandle.Call(0)
	dpi := uintptr(96)
	if iconWindowDPI.Find() == nil {
		if current, _, _ := iconWindowDPI.Call(hwnd); current != 0 {
			dpi = current
		}
	}
	for _, spec := range []struct{ kind, size uintptr }{{0, 16}, {1, 32}} {
		size := (spec.size*dpi + 48) / 96
		// IMAGE_ICON, IDI_APPLICATION, LR_SHARED: Windows owns these handles.
		icon, _, _ := iconLoadImage.Call(module, 32512, 1, size, size, 0x8000)
		if icon != 0 {
			// Post instead of Send: Gio's native thread is awaiting this event.
			iconPostMessage.Call(hwnd, 0x80, spec.kind, icon)
		}
	}
}
