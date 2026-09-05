package vtinput

import (
	"os"
	"runtime"
	"time"

	"golang.org/x/term"
)

// Win32 Input Mode & Kitty Protocol sequences
const (
	seqEnableWin32  = "\x1b[?9001h"
	seqDisableWin32 = "\x1b[?9001l"

	seqEnableKitty  = "\x1b[>15u"
	seqDisableKitty = "\x1b[<1u"

	// 1002: Cell motion mouse, 1003: Any event mouse
	// 1006: SGR extended mode, 1015: URXVT extended mode
	// We enable all of them, the terminal will pick the best supported one.
	seqEnableMouse  = "\x1b[?1002h\x1b[?1003h\x1b[?1015h\x1b[?1006h"
	seqDisableMouse = "\x1b[?1006l\x1b[?1015l\x1b[?1003l\x1b[?1002l"

	// 1004: Focus tracking, 2004: Bracketed paste
	seqEnableExt  = "\x1b[?1004h\x1b[?2004h"
	seqDisableExt = "\x1b[?2004l\x1b[?1004l"

	// Both end with ST, the terminator APC is defined with. Ending the disable
	// with BEL instead leaves a terminal that accepts only ST -- Termux is one --
	// in far2l mode after exit: it keeps encoding input as far2l packets, the
	// shell cannot read them, and reset(1) knows nothing about the mode.
	seqEnableFar2l  = "\x1b_far2l1\x1b\\"
	seqDisableFar2l = "\x1b_far2l0\x1b\\"
)

// Protocol flags to selectively enable features.
type Protocol uint32

const (
	Win32InputMode Protocol = 1 << iota
	KittyKeyboard
	MouseSupport
	FocusAndPaste
	Far2lExtensions

	// DefaultProtocols enables all supported features.
	DefaultProtocols = Win32InputMode | KittyKeyboard | MouseSupport | FocusAndPaste | Far2lExtensions
)

// Enable puts the terminal into Raw Mode and enables all supported protocols.
func Enable() (func(), error) {
	return EnableProtocols(DefaultProtocols)
}

// EnableProtocols puts the terminal into Raw Mode and enables specific protocols.
func EnableProtocols(p Protocol) (func(), error) {
	Log("VTINPUT: EnableProtocols requested, mask: 0x%08X", uint32(p))
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
	if p&Far2lExtensions != 0 {
		// DSR prevents blocking on init by assuring standard terminal response
		enableSeq += seqEnableFar2l + "\x1b[5n"
		disableSeq = seqDisableFar2l + disableSeq
	}

	// On Windows, if we are using the native ConPTY/WinAPI reader (default),
	// we MUST NOT enable redundant ANSI protocols. WinAPI natively provides
	// exact Key, Mouse, and Focus events. Windows also has native clipboard APIs.
	// If we request ANSI protocols, ConPTY will "pulverize" the resulting ESC
	// sequences into VK:0 key events, causing massive duplication and lag.
	isWindowsNative := runtime.GOOS == "windows" && (InputMode == "" || InputMode == "ConPTY")
	if InputMode == "ConPTY" || isWindowsNative {
		Log("VTINPUT: Windows Native mode detected, suppressing redundant ANSI protocols.")
		return func() {
			term.Restore(fd, oldState)
		}, nil
	}

	// 4. Send activation sequences
	if _, err := os.Stdout.WriteString(enableSeq); err != nil {
		term.Restore(fd, oldState)
		return nil, err
	}
	// Give the terminal emulator a moment to process the state changes before
	// the application starts reading from stdin. This can help prevent race conditions
	// where the reader starts consuming input before the terminal has switched protocols.
	time.Sleep(50 * time.Millisecond)

	// 5. Create the restore function
	restore := func() {
		os.Stdout.WriteString(disableSeq)
		term.Restore(fd, oldState)
	}

	return restore, nil
}
