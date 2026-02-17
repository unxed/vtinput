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
// scanCSI looks for a CSI sequence (ESC [ ... terminator).
func scanCSI(data []byte) (terminatorIdx int, command byte, err error) {
	if len(data) < 2 {
		return 0, 0, ErrIncomplete
	}
	if data[0] != 0x1B || data[1] != '[' {
		return 0, 0, ErrInvalidSequence
	}

	// ECMA-48: parameters are 0x30-0x3F, terminator is 0x40-0x7E.
	for i := 2; i < len(data); i++ {
		b := data[i]
		if b >= 0x40 && b <= 0x7E {
			// Found the terminator!
			return i, b, nil
		}
		if b < 0x20 || b > 0x3F {
			return 0, 0, ErrInvalidSequence
		}
	}

	return 0, 0, ErrIncomplete
}
// decodeAnsiModifiers converts TUI modifier codes (1 + bitmask) to vtinput flags.
// Supported by Kitty and modern Legacy CSI.
func decodeAnsiModifiers(modCode int) uint32 {
	actual := uint32(0)
	bits := modCode - 1
	if (bits & 0x01) != 0 { actual |= ShiftPressed }
	if (bits & 0x02) != 0 { actual |= LeftAltPressed }
	if (bits & 0x04) != 0 { actual |= LeftCtrlPressed }
	if (bits & 0x08) != 0 { actual |= EnhancedKey } // Super/Win in Kitty
	return actual
}

// mapCSICommandToVK maps CSI terminator characters to Virtual Key Codes.
func mapCSICommandToVK(command byte) uint16 {
	switch command {
	case 'A': return VK_UP
	case 'B': return VK_DOWN
	case 'C': return VK_RIGHT
	case 'D': return VK_LEFT
	case 'H': return VK_HOME
	case 'F': return VK_END
	case 'P': return VK_F1
	case 'Q': return VK_F2
	case 'R': return VK_F3
	case 'S': return VK_F4
	case 'Z': return VK_TAB
	}
	return 0
}

// mapTildeToVK maps CSI ~ codes to Virtual Key Codes.
func mapTildeToVK(code int) uint16 {
	switch code {
	case 1, 7: return VK_HOME
	case 2:    return VK_INSERT
	case 3:    return VK_DELETE
	case 4, 8: return VK_END
	case 5:    return VK_PRIOR
	case 6:    return VK_NEXT
	case 11, 12, 13, 14, 15: return uint16(VK_F1 + (code - 11))
	case 17, 18, 19, 20, 21: return uint16(VK_F6 + (code - 17))
	case 23, 24:             return uint16(VK_F11 + (code - 23))
	}
	return 0
}

// ParseWin32InputEvent attempts to parse a byte slice containing a Win32 Input Mode sequence.
// Format: CSI Vk ; Sc ; Uc ; Kd ; Cs ; Rc _
//
// Returns:
// - event: The parsed InputEvent (pointer).
// - n: The number of bytes consumed from the slice.
// - err: ErrInvalidSequence, ErrIncomplete, or nil.
func ParseWin32InputEvent(data []byte) (*InputEvent, int, error) {
	terminatorIdx, command, err := scanCSI(data)
	if err != nil {
		return nil, 0, err
	}

	if command != '_' {
		return nil, 0, ErrInvalidSequence
	}

	paramStr := string(data[2:terminatorIdx])
	params := strings.Split(paramStr, ";")

	event := &InputEvent{
		Type:        KeyEventType,
		RepeatCount: 1,
	}

	parseUint := func(s string) uint32 {
		if s == "" { return 0 }
		val, _ := strconv.ParseUint(s, 10, 32)
		return uint32(val)
	}

	if len(params) > 0 { event.VirtualKeyCode = uint16(parseUint(params[0])) }
	if len(params) > 1 { event.VirtualScanCode = uint16(parseUint(params[1])) }
	if len(params) > 2 { event.Char = rune(parseUint(params[2])) }
	if len(params) > 3 {
		if parseUint(params[3]) == 1 { event.KeyDown = true }
	}
	if len(params) > 4 { event.ControlKeyState = parseUint(params[4]) }
	if len(params) > 5 {
		rc := parseUint(params[5])
		if rc > 0 { event.RepeatCount = uint16(rc) }
	}

	return event, terminatorIdx + 1, nil
}

// ParseLegacyCSI handles standard ANSI sequences (ESC [ ...).
func ParseLegacyCSI(data []byte) (*InputEvent, int, error) {
	terminatorIdx, command, err := scanCSI(data)
	if err != nil {
		return nil, 0, err
	}

	params := strings.Split(string(data[2:terminatorIdx]), ";")
	getParam := func(idx int, def int) int {
		if len(params) <= idx || params[idx] == "" { return def }
		val, _ := strconv.Atoi(params[idx])
		return val
	}

	event := &InputEvent{
		Type:            KeyEventType,
		KeyDown:         true,
		ControlKeyState: decodeAnsiModifiers(getParam(1, 1)),
	}

	if command == '~' {
		event.VirtualKeyCode = mapTildeToVK(getParam(0, 0))
	} else {
		event.VirtualKeyCode = mapCSICommandToVK(command)
		if command == 'Z' {
			event.ControlKeyState |= ShiftPressed
		}
	}

	if event.VirtualKeyCode == 0 {
		return nil, 0, ErrInvalidSequence
	}

	return event, terminatorIdx + 1, nil
}
// ParseLegacySS3 handles standard SS3 sequences (ESC O ...).
// These are common for F1-F4 and Home/End on some terminals.
func ParseLegacySS3(data []byte) (*InputEvent, int, error) {
	if len(data) < 2 {
		return nil, 0, ErrIncomplete
	}
	if data[0] != 0x1B || data[1] != 'O' {
		return nil, 0, ErrInvalidSequence
	}
	if len(data) < 3 {
		return nil, 0, ErrIncomplete
	}

	event := &InputEvent{
		Type:    KeyEventType,
		KeyDown: true,
	}

	switch data[2] {
	case 'P': event.VirtualKeyCode = VK_F1
	case 'Q': event.VirtualKeyCode = VK_F2
	case 'R': event.VirtualKeyCode = VK_F3
	case 'S': event.VirtualKeyCode = VK_F4
	case 'H': event.VirtualKeyCode = VK_HOME
	case 'F': event.VirtualKeyCode = VK_END
	default:
		return nil, 0, ErrInvalidSequence
	}

	return event, 3, nil
}
// ParseMouseSGR handles modern SGR mouse sequences (ESC [ < ...).
func ParseMouseSGR(data []byte) (*InputEvent, int, error) {
	terminatorIdx, command, err := scanCSI(data)
	if err != nil {
		return nil, 0, err
	}

	// Must start with '<' for SGR
	if len(data) < 3 || data[2] != '<' {
		return nil, 0, ErrInvalidSequence
	}

	// Extract everything between '<' and the terminator (M/m)
	paramStr := string(data[3:terminatorIdx])
	params := strings.Split(paramStr, ";")
	if len(params) < 3 {
		return nil, 0, ErrInvalidSequence
	}

	atoi := func(s string) int {
		v, _ := strconv.Atoi(s)
		return v
	}

	pb := atoi(params[0]) // Button and flags
	px := atoi(params[1]) // X
	py := atoi(params[2]) // Y

	event := &InputEvent{
		Type:    MouseEventType,
		MouseX:  uint16(px),
		MouseY:  uint16(py),
		KeyDown: (command == 'M'), // 'M' = press/move, 'm' = release
	}

	// Decode Pb bits:
	// 0-1: button (0=Left, 1=Middle, 2=Right, 3=Release/None)
	// 5: motion, 6: wheel
	buttonPart := pb & 0x03
	if (pb & 64) != 0 {
		// Mouse Wheel
		if buttonPart == 0 {
			event.WheelDirection = 1 // Up
		} else if buttonPart == 1 {
			event.WheelDirection = -1 // Down
		}
	} else {
		// Normal buttons
		switch buttonPart {
		case 0: event.ButtonState = FromLeft1stButtonPressed
		case 1: event.ButtonState = FromLeft2ndButtonPressed
		case 2: event.ButtonState = RightmostButtonPressed
		}
	}

	if (pb & 32) != 0 {
		event.MouseEventFlags |= MouseMoved
	}

	// Modifiers
	if (pb & 4) != 0 { event.ControlKeyState |= ShiftPressed }
	if (pb & 8) != 0 { event.ControlKeyState |= LeftAltPressed }
	if (pb & 16) != 0 { event.ControlKeyState |= LeftCtrlPressed }

	return event, terminatorIdx + 1, nil
}
