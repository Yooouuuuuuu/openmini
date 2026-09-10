//go:build windows

package ui

import "golang.org/x/sys/windows"

// KeepConsoleAwake turns off the console's QuickEdit mode. With it on, a
// click or a text selection in the window pauses every write the program
// makes until someone presses Enter or Escape there, which looks like a
// frozen server.
func KeepConsoleAwake() {
	h, err := windows.GetStdHandle(windows.STD_INPUT_HANDLE)
	if err != nil {
		return
	}
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return
	}
	mode &^= windows.ENABLE_QUICK_EDIT_MODE
	mode |= windows.ENABLE_EXTENDED_FLAGS
	windows.SetConsoleMode(h, mode)
}
