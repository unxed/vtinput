//go:build !windows

package vtinput

// clearVTInput is a no-op outside Windows: ENABLE_VIRTUAL_TERMINAL_INPUT is
// a console-host flag, and term.MakeRaw's termios path never sets it.
func clearVTInput(fd int) {}
