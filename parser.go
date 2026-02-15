package vtinput

import (
	"errors"
	"strconv"
	"strings"
)

var (
	// ErrInvalidSequence indicates the byte slice is not a valid Win32 input sequence.
	ErrInvalidSequence = errors.New("invalid win32 input sequence")
	// ErrIncomplete indicates the sequence might be valid but is incomplete (needs more bytes).
	ErrIncomplete = errors.New("incomplete sequence")
)

// ParseWin32InputEvent attempts to parse a byte slice containing a Win32 Input Mode sequence.
// Format: CSI Vk ; Sc ; Uc ; Kd ; Cs ; Rc _
//
// Returns:
// - event: The parsed InputEvent (pointer).
// - n: The number of bytes consumed from the slice.
// - err: ErrInvalidSequence, ErrIncomplete, or nil.
func ParseWin32InputEvent(data []byte) (*InputEvent, int, error) {
	if len(data) == 0 {
		return nil, 0, ErrIncomplete
	}

	// 1. Check strict prefix "ESC [" (0x1B 0x5B)
	if data[0] != 0x1B {
		return nil, 0, ErrInvalidSequence
	}
	if len(data) < 2 {
		return nil, 0, ErrIncomplete
	}
	if data[1] != '[' {
		return nil, 0, ErrInvalidSequence
	}

	// 2. Scan for the terminator character '_'
	// We start scanning from index 2 to skip "ESC ["
	terminatorIdx := -1
	for i := 2; i < len(data); i++ {
		// Optimization: if we see characters that shouldn't be here, abort early
		// Valid chars are digits 0-9, semicolon ';', and underscore '_'
		b := data[i]
		if b == '_' {
			terminatorIdx = i
			break
		}
		if (b < '0' || b > '9') && b != ';' {
			// Unexpected character found before terminator -> not our sequence
			return nil, 0, ErrInvalidSequence
		}
	}

	if terminatorIdx == -1 {
		// We haven't found the terminator yet, but the buffer looks valid so far.
		// We need more data.
		return nil, 0, ErrIncomplete
	}

	// 3. Extract the parameter string between '[' and '_'
	// data[2 : terminatorIdx] contains "Vk;Sc;Uc;Kd;Cs;Rc"
	paramStr := string(data[2:terminatorIdx])

	// Split by semicolon
	// Note: Strings like "17;;1" result in ["17", "", "1"]
	params := strings.Split(paramStr, ";")

	// 4. Map params to the struct
	// Defaults defined in spec: Vk=0, Sc=0, Uc=0, Kd=0, Cs=0, Rc=1

	event := &InputEvent{
		Type:        KeyEventType,
		RepeatCount: 1, // Default is 1
	}

	// Helper to safely parse uint
	parseUint := func(s string) uint32 {
		if s == "" {
			return 0
		}
		val, _ := strconv.ParseUint(s, 10, 32)
		return uint32(val)
	}

	if len(params) > 0 {
		event.VirtualKeyCode = uint16(parseUint(params[0]))
	}
	if len(params) > 1 {
		event.VirtualScanCode = uint16(parseUint(params[1]))
	}
	if len(params) > 2 {
		event.Char = rune(parseUint(params[2]))
	}
	if len(params) > 3 {
		// Kd: 0 or 1
		if parseUint(params[3]) == 1 {
			event.KeyDown = true
		}
	}
	if len(params) > 4 {
		event.ControlKeyState = parseUint(params[4])
	}
	if len(params) > 5 {
		rc := parseUint(params[5])
		if rc > 0 {
			event.RepeatCount = uint16(rc)
		}
	}

	// Return the event and total length (terminator index + 1 byte for '_')
	return event, terminatorIdx + 1, nil
}