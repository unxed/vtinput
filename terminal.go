package vtinput

import (
	"os"

	"golang.org/x/term"
)

// Win32 Input Mode sequences
const (
	seqEnableWin32  = "\x1b[?9001h"
	seqDisableWin32 = "\x1b[?9001l"
)

// Enable puts the terminal into Raw Mode and enables Win32 Input Mode.
// It returns a restore function that MUST be called before the program exits.
//
// Usage:
//   restore, err := vtinput.Enable()
//   if err != nil { panic(err) }
//   defer restore()
func Enable() (func(), error) {
	// 1. Get the file descriptor of Stdin (usually 0)
	fd := int(os.Stdin.Fd())

	// 2. Put terminal in Raw Mode
	// This disables echo and canonical mode (line buffering).
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}

	// 3. Send the escape sequence to enable Win32 Input Mode
	// We write directly to Stdout.
	if _, err := os.Stdout.WriteString(seqEnableWin32); err != nil {
		// If writing fails, try to restore terminal state immediately
		term.Restore(fd, oldState)
		return nil, err
	}

	// 4. Create the restore function (closure)
	// This function captures 'fd' and 'oldState' and can be called later.
	restore := func() {
		// Disable Win32 Input Mode
		os.Stdout.WriteString(seqDisableWin32)
		// Restore original terminal attributes (echo, line buffering, etc.)
		term.Restore(fd, oldState)
	}

	return restore, nil
}