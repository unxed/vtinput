package vtinput

import (
	"os"

	"golang.org/x/term"
)

// Win32 Input Mode sequences
const (
	seqEnableWin32  = "\x1b[?9001h"
	seqDisableWin32 = "\x1b[?9001l"

	// 1003: Any event mouse (motion + buttons), 1006: SGR extended mode
	seqEnableMouse  = "\x1b[?1003h\x1b[?1006h"
	seqDisableMouse = "\x1b[?1006l\x1b[?1003l"
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

	// 3. Send activation sequences
	if _, err := os.Stdout.WriteString(seqEnableWin32 + seqEnableMouse); err != nil {
		term.Restore(fd, oldState)
		return nil, err
	}

	// 4. Create the restore function (closure)
	restore := func() {
		os.Stdout.WriteString(seqDisableMouse + seqDisableWin32)
		term.Restore(fd, oldState)
	}

	return restore, nil
}