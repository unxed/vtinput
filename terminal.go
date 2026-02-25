package vtinput

import (
	"os"

	"golang.org/x/term"
)

// Win32 Input Mode & Kitty Protocol sequences
const (
	seqEnableWin32  = "\x1b[?9001h"
	seqDisableWin32 = "\x1b[?9001l"

	seqEnableKitty  = "\x1b[>15u"
	seqDisableKitty = "\x1b[<1u"

	// 1003: Any event mouse (motion + buttons), 1006: SGR extended mode
	seqEnableMouse  = "\x1b[?1003h\x1b[?1006h"
	seqDisableMouse = "\x1b[?1006l\x1b[?1003l"

	// 1004: Focus tracking, 2004: Bracketed paste
	seqEnableExt  = "\x1b[?1004h\x1b[?2004h"
	seqDisableExt = "\x1b[?2004l\x1b[?1004l"
)
// Protocol flags to selectively enable features.
type Protocol uint32

const (
	Win32InputMode Protocol = 1 << iota
	KittyKeyboard
	MouseSupport
	FocusAndPaste

	// DefaultProtocols enables all supported features.
	DefaultProtocols = Win32InputMode | KittyKeyboard | MouseSupport | FocusAndPaste
)

// Enable puts the terminal into Raw Mode and enables all supported protocols.
func Enable() (func(), error) {
	return EnableProtocols(DefaultProtocols)
}

// EnableProtocols puts the terminal into Raw Mode and enables specific protocols.
func EnableProtocols(p Protocol) (func(), error) {
	// 1. Get the file descriptor of Stdin (usually 0)
	fd := int(os.Stdin.Fd())

	// 2. Put terminal in Raw Mode
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}

	// 3. Build activation and deactivation strings
	var enableSeq, disableSeq string

	if p&KittyKeyboard != 0 {
		enableSeq += seqEnableKitty
		disableSeq = seqDisableKitty + disableSeq // LIFO order for restore is good practice
	}
	if p&Win32InputMode != 0 {
		enableSeq += seqEnableWin32
		disableSeq = seqDisableWin32 + disableSeq
	}
	if p&MouseSupport != 0 {
		enableSeq += seqEnableMouse
		disableSeq = seqDisableMouse + disableSeq
	}
	if p&FocusAndPaste != 0 {
		enableSeq += seqEnableExt
		disableSeq = seqDisableExt + disableSeq
	}

	// 4. Send activation sequences
	if _, err := os.Stdout.WriteString(enableSeq); err != nil {
		term.Restore(fd, oldState)
		return nil, err
	}

	// 5. Create the restore function
	restore := func() {
		os.Stdout.WriteString(disableSeq)
		term.Restore(fd, oldState)
	}

	return restore, nil
}