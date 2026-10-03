package vtinput

import (
	"os"
	"runtime"
	"sync"
	"time"

	"golang.org/x/term"
)

// homeState is the console input mode MakeRaw saw before the last
// EnableProtocols call -- the mode the shell left behind, with
// ENABLE_VIRTUAL_TERMINAL_INPUT off. Both the restore closure returned to the
// caller and Reader.platformClose must hand the console back in this state:
// the mode snapshot the reader takes at its own creation is the raw mode
// MakeRaw just built, and restoring *that* on close leaves VT input enabled,
// which makes the console host deliver the terminal's mouse reports to the
// next reader as key events instead of mouse events.
var (
	homeMu    sync.Mutex
	homeState *term.State
)

func setHomeState(s *term.State) {
	homeMu.Lock()
	homeState = s
	homeMu.Unlock()
}

// homeTerminalState returns the last state MakeRaw was called with, or nil if
// EnableProtocols has not run in this process.
func homeTerminalState() *term.State {
	homeMu.Lock()
	defer homeMu.Unlock()
	return homeState
}

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
	setHomeState(oldState)

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
		// MakeRaw just turned ENABLE_VIRTUAL_TERMINAL_INPUT on, but nothing
		// on this path reads VT byte sequences -- the native reader takes
		// console records -- and while the flag stays set the console host
		// converts the terminal's mouse reports into key-event characters
		// that queue up as visible text for the next reader (an application
		// started with mouse reports still arriving shows them typed into
		// its first line editor). Drop the flag right here; the state
		// Restore hands back was captured before MakeRaw and does not
		// depend on it.
		clearVTInput(fd)
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
