//go:build windows

package vtinput

import "golang.org/x/sys/windows"

// clearVTInput clears ENABLE_VIRTUAL_TERMINAL_INPUT (0x0200) on the console
// input handle fd, leaving every other flag -- including the raw-mode ones
// MakeRaw just set -- untouched. term.MakeRaw always enables the flag, but
// the Windows-native input path (Reader.platformInit, EnableProtocols'
// caller chain) never reads VT byte sequences from stdin: it takes console
// records. While the flag is on, the console host instead parses the
// terminal's incoming mouse reports into key-event records carrying the
// report's text, and those characters sit in the input buffer until some
// reader picks them up. The reader clears the flag again at its own
// creation; this call closes the window between MakeRaw and that point,
// where nobody has started reading yet but the garbage already piles up.
func clearVTInput(fd int) {
	handle := windows.Handle(fd)
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		Log("VTINPUT: GetConsoleMode before VT-input clear failed: %v", err)
		return
	}
	if mode&0x0200 == 0 {
		return
	}
	if err := windows.SetConsoleMode(handle, mode&^0x0200); err != nil {
		Log("VTINPUT: clearing ENABLE_VIRTUAL_TERMINAL_INPUT failed: %v", err)
	}
}
