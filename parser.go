package vtinput

import (
	"errors"
	"github.com/emmansun/base64"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
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
func decodeAnsiModifiers(modCode int) ControlKeyState {
	if modCode <= 1 {
		return 0
	}
	actual := ControlKeyState(0)
	bits := modCode - 1
	if (bits & 0x01) != 0 {
		actual |= ShiftPressed
	}
	if (bits & 0x02) != 0 {
		actual |= LeftAltPressed
	}
	if (bits & 0x04) != 0 {
		actual |= LeftCtrlPressed
	}
	if (bits & 0x08) != 0 {
		actual |= EnhancedKey
	} // Super/Win in Kitty
	return actual
}

// mapCSICommandToVK maps CSI terminator characters to Virtual Key Codes.
func mapCSICommandToVK(command byte) uint16 {
	switch command {
	case 'A':
		return VK_UP
	case 'B':
		return VK_DOWN
	case 'C':
		return VK_RIGHT
	case 'D':
		return VK_LEFT
	case 'H':
		return VK_HOME
	case 'F':
		return VK_END
	case 'P':
		return VK_F1
	case 'Q':
		return VK_F2
	case 'R':
		return VK_F3
	case 'S':
		return VK_F4
	case 'Z':
		return VK_TAB
	}
	return 0
}

// isEnhancedVK reports whether vk is one of the Windows "enhanced" keys:
// the navigation cluster (arrows, Home/End/PgUp/PgDn, Ins/Del) to the left
// of the numeric keypad. This is the same set far2l itself uses to decide
// the flag (TTYBackend.cpp and wxWinTranslations.cpp, both named
// IsEnhancedKey) — kept in sync here so a plain terminal, not just far2l's
// own extensions or ConPTY, reports these keys the way Windows console
// input would, which is what farKeyNames-based naming (see f4's
// EventToFarString) assumes throughout.
func isEnhancedVK(vk uint16) bool {
	switch vk {
	case VK_LEFT, VK_RIGHT, VK_UP, VK_DOWN,
		VK_HOME, VK_END, VK_PRIOR, VK_NEXT,
		VK_INSERT, VK_DELETE:
		return true
	}
	return false
}

// mapTildeToVK maps CSI ~ codes to Virtual Key Codes.
func mapTildeToVK(code int) uint16 {
	switch code {
	case 1, 7:
		return VK_HOME
	case 2:
		return VK_INSERT
	case 3:
		return VK_DELETE
	case 4, 8:
		return VK_END
	case 5:
		return VK_PRIOR
	case 6:
		return VK_NEXT
	case 11, 12, 13, 14, 15:
		return uint16(VK_F1 + (code - 11))
	case 17, 18, 19, 20, 21:
		return uint16(VK_F6 + (code - 17))
	case 23, 24:
		return uint16(VK_F11 + (code - 23))
	}
	return 0
}

// scanAPC looks for an APC sequence (ESC _ ... terminator).
func scanAPC(data []byte) (terminatorIdx int, err error) {
	if len(data) < 2 {
		return 0, ErrIncomplete
	}
	if data[0] != 0x1B || data[1] != '_' {
		return 0, ErrInvalidSequence
	}
	lastEsc := false
	for i := 2; i < len(data); i++ {
		if data[i] == 0x07 {
			return i, nil
		}
		if data[i] == '\\' && lastEsc {
			return i, nil // Standard ST terminator
		}
		lastEsc = (data[i] == 0x1B)
	}
	return 0, ErrIncomplete
}

// ParseFar2lAPC parses Far2l specific APC sequences.
func ParseFar2lAPC(data []byte) (*InputEvent, int, error) {
	terminatorIdx, err := scanAPC(data)
	if err != nil {
		return nil, 0, err
	}

	payloadEnd := terminatorIdx
	if data[terminatorIdx] == '\\' {
		payloadEnd-- // Skip the ESC before '\'
	}

	if payloadEnd < 2 {
		return nil, terminatorIdx + 1, nil
	}

	paramStr := string(data[2:payloadEnd])

	// Robust prefix and separator handling
	var b64 string
	var event *InputEvent

	if strings.HasPrefix(paramStr, "f2l") {
		b64 = strings.TrimPrefix(paramStr, "f2l")
		b64 = strings.TrimPrefix(b64, ":")
		event = &InputEvent{Type: Far2lEventType, Far2lCommand: "event", InputSource: "far2l"}
	} else if strings.HasPrefix(paramStr, "far2l") {
		content := strings.TrimPrefix(paramStr, "far2l")
		if content == "" {
			return nil, terminatorIdx + 1, nil
		}
		switch content {
		case "1":
			return &InputEvent{Type: Far2lEventType, Far2lCommand: "enable", InputSource: "far2l"}, terminatorIdx + 1, nil
		case "0":
			return &InputEvent{Type: Far2lEventType, Far2lCommand: "disable", InputSource: "far2l"}, terminatorIdx + 1, nil
		case "ok":
			return &InputEvent{Type: Far2lEventType, Far2lCommand: "ok", InputSource: "far2l"}, terminatorIdx + 1, nil
		}

		b64 = strings.TrimPrefix(content, ":")
		event = &InputEvent{Type: Far2lEventType, Far2lCommand: "reply", InputSource: "far2l"}
		// If it has a colon, it's an interaction request from remote to us
		if strings.HasPrefix(content, ":") {
			event.Far2lCommand = "interact"
		}
	}

	if event != nil {
		if m := len(b64) % 4; m != 0 {
			b64 += strings.Repeat("=", 4-m)
		}
		decoded, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			Log("Reader: Far2l Base64 decode failed for %q: %v", b64, err)
			return nil, terminatorIdx + 1, nil
		}

		event.Far2lData = decoded
		// For 'f2l' prefix, we unpack the stack into a native KeyEvent/MouseEvent
		if event.Far2lCommand == "event" {
			stk := (*Far2lStack)(&decoded)
			cmd := stk.PopU8()
			switch cmd {
			case 'K', 'k':
				event.Type = KeyEventType
				event.Char = rune(stk.PopU32())
				event.ControlKeyState = ControlKeyState(stk.PopU32())
				event.VirtualScanCode = stk.PopU16()
				event.VirtualKeyCode = stk.PopU16()
				event.RepeatCount = stk.PopU16()
				event.KeyDown = (cmd == 'K')
			case 'C', 'c':
				event.Type = KeyEventType
				event.Char = rune(stk.PopU16())
				event.ControlKeyState = ControlKeyState(stk.PopU16())
				event.VirtualKeyCode = uint16(stk.PopU8())
				event.RepeatCount = 1
				event.KeyDown = (cmd == 'C')
			case 'M':
				event.Type = MouseEventType
				event.MouseEventFlags = stk.PopU32()
				event.ControlKeyState = ControlKeyState(stk.PopU32())
				event.ButtonState = stk.PopU32()
				event.MouseY = int16(stk.PopU16())
				event.MouseX = int16(stk.PopU16())
				event.KeyDown = true
				if (event.MouseEventFlags & MouseWheeled) != 0 {
					delta := int16(event.ButtonState >> 16)
					if delta > 0 {
						event.WheelDirection = 1
					} else if delta < 0 {
						event.WheelDirection = -1
					}
				}
			case 'm':
				event.Type = MouseEventType
				event.MouseEventFlags = uint32(stk.PopU8())
				event.ControlKeyState = ControlKeyState(stk.PopU8())
				encBtn := stk.PopU16()
				event.ButtonState = uint32(encBtn&0xFF) | uint32((encBtn&0xFF00)<<8)
				event.MouseY = int16(stk.PopU16())
				event.MouseX = int16(stk.PopU16())
				event.KeyDown = true
				if (event.MouseEventFlags & MouseWheeled) != 0 {
					delta := int16(event.ButtonState >> 16)
					if delta > 0 {
						event.WheelDirection = 1
					} else if delta < 0 {
						event.WheelDirection = -1
					}
				}
			case 'S':
				event.Type = ResizeEventType
			}
		}
		return event, terminatorIdx + 1, nil
	}

	return nil, terminatorIdx + 1, nil // Ignored APC
}

// ParseWin32InputEvent attempts to parse a byte slice containing a Win32 Input Mode sequence.
// Format: CSI Vk ; Sc ; Uc ; Kd ; Cs ; Rc _
// Unicode characters already present in AltGr key-down records are TextInput.
// The raw protocol can carry individual UTF-16 surrogate code units; use Reader
// to combine those units before dispatching keyboard events.
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
		InputSource: "win32",
	}

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
		if parseUint(params[3]) == 1 {
			event.KeyDown = true
		}
	}
	if len(params) > 4 {
		event.ControlKeyState = ControlKeyState(parseUint(params[4]))
	}
	if len(params) > 5 {
		rc := parseUint(params[5])
		if rc > 0 {
			event.RepeatCount = uint16(rc)
		}
	}

	markWindowsText(event)
	return event, terminatorIdx + 1, nil
}

// ParseLegacyCSI handles standard ANSI sequences (ESC [ ...).
func ParseLegacyCSI(data []byte) (*InputEvent, int, error) {
	terminatorIdx, command, err := scanCSI(data)
	if err != nil {
		return nil, 0, err
	}

	paramStr := string(data[2:terminatorIdx])
	// Reject modern sequences containing colon (sub-parameters) or angle brackets
	if strings.ContainsAny(paramStr, ":<>") {
		return nil, 0, ErrInvalidSequence
	}

	params := strings.Split(paramStr, ";")
	getParam := func(idx int, def int) int {
		if len(params) <= idx || params[idx] == "" {
			return def
		}
		val, err := strconv.Atoi(params[idx])
		if err != nil {
			return def
		}
		return val
	}

	event := &InputEvent{
		Type:            KeyEventType,
		KeyDown:         true,
		ControlKeyState: decodeAnsiModifiers(getParam(1, 1)),
		IsLegacy:        true,
		InputSource:     "legacy_csi",
		RepeatCount:     1,
	}

	if command == '~' {
		event.VirtualKeyCode = mapTildeToVK(getParam(0, 0))
	} else {
		event.VirtualKeyCode = mapCSICommandToVK(command)
		if command == 'Z' {
			event.ControlKeyState |= ShiftPressed
		}
	}
	if isEnhancedVK(event.VirtualKeyCode) {
		event.ControlKeyState |= EnhancedKey
	}
	if event.VirtualKeyCode == 0 {
		// Silence warnings for known modern terminators handled by other parsers (Win32 '_', Kitty 'u')
		if command != '_' && command != 'u' {
			Log("Reader: Warning - CSI command '%c' not mapped. Params: %v", command, params)
		}
	}

	if event.VirtualKeyCode == 0 {
		return nil, 0, ErrInvalidSequence
	}

	return event, terminatorIdx + 1, nil
}

// ParseLegacySS3 handles standard SS3 sequences (ESC O ...).
// These are common for F1-F4 and Home/End on some terminals.
// Supports both simple (ESC O P) and modified (ESC O 3 P) formats.
func ParseLegacySS3(data []byte) (*InputEvent, int, error) {
	if len(data) < 2 {
		return nil, 0, ErrIncomplete
	}
	isCSI := false
	if data[0] == 0x1B && len(data) > 2 && data[1] == '[' && data[2] == 'O' {
		isCSI = true
	} else if data[0] != 0x1B || data[1] != 'O' {
		return nil, 0, ErrInvalidSequence
	}

	i := 2
	if isCSI {
		i = 3
	}
	if len(data) <= i {
		return nil, 0, ErrIncomplete
	}
	mod := 1
	// Check for optional modifier digit (e.g. \x1bO3P where 3 is Alt)
	if data[i] >= '0' && data[i] <= '9' {
		mod = int(data[i] - '0')
		i++
		if len(data) <= i {
			return nil, 0, ErrIncomplete
		}
	}

	event := &InputEvent{
		Type:            KeyEventType,
		KeyDown:         true,
		IsLegacy:        true,
		InputSource:     "legacy_ss3",
		ControlKeyState: decodeAnsiModifiers(mod),
		RepeatCount:     1,
	}

	switch data[i] {
	case 'P':
		event.VirtualKeyCode = VK_F1
	case 'Q':
		event.VirtualKeyCode = VK_F2
	case 'R':
		event.VirtualKeyCode = VK_F3
	case 'S':
		event.VirtualKeyCode = VK_F4
	case 'H':
		event.VirtualKeyCode = VK_HOME
	case 'F':
		event.VirtualKeyCode = VK_END
	case 'A':
		event.VirtualKeyCode = VK_UP
		if mod == 1 {
			event.ControlKeyState |= LeftCtrlPressed
		}
	case 'B':
		event.VirtualKeyCode = VK_DOWN
		if mod == 1 {
			event.ControlKeyState |= LeftCtrlPressed
		}
	case 'C':
		event.VirtualKeyCode = VK_RIGHT
		if mod == 1 {
			event.ControlKeyState |= LeftCtrlPressed
		}
	case 'D':
		event.VirtualKeyCode = VK_LEFT
		if mod == 1 {
			event.ControlKeyState |= LeftCtrlPressed
		}
	default:
		return nil, 0, ErrInvalidSequence
	}

	if isEnhancedVK(event.VirtualKeyCode) {
		event.ControlKeyState |= EnhancedKey
	}

	return event, i + 1, nil
}

// ParseDSRReply parses Device Status Report replies (ESC [ <params> n).
// These are not input events for the application, but must be consumed to keep the buffer clean.
func ParseDSRReply(data []byte) (*InputEvent, int, error) {
	terminatorIdx, command, err := scanCSI(data)
	if err != nil {
		return nil, 0, err
	}

	if command == 'n' {
		// Valid DSR reply, consume and ignore.
		return nil, terminatorIdx + 1, nil
	}
	return nil, 0, ErrInvalidSequence
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

	// SGR protocol is 1-based, we use 0-based coordinates.
	if px > 0 {
		px--
	}
	if py > 0 {
		py--
	}

	event := &InputEvent{
		Type:        MouseEventType,
		MouseX:      int16(px),
		MouseY:      int16(py),
		KeyDown:     (command == 'M'), // 'M' = press/move, 'm' = release
		InputSource: "sgr_mouse",
	}

	// mouse bit shifts
	const (
		bitShift  = 0b0000_0100
		bitAlt    = 0b0000_1000
		bitCtrl   = 0b0001_0000
		bitMotion = 0b0010_0000
		bitWheel  = 0b0100_0000
		bitAdd    = 0b1000_0000 // additional buttons 8-11

		bitsMask = 0b0000_0011
	)

	// Decode Pb bits:
	// 0-1: button (0=Left, 1=Middle, 2=Right, 3=Release/None)
	// 5: motion, 6: wheel
	buttonPart := pb & 0x03
	if (pb & 64) != 0 {
		// Mouse Wheel
		switch buttonPart {
		case 0:
			event.WheelDirection = 1 // Up
		case 1:
			event.WheelDirection = -1 // Down
		}
	} else {
		// Normal buttons
		switch buttonPart {
		case 0:
			event.ButtonState = FromLeft1stButtonPressed
		case 1:
			event.ButtonState = FromLeft2ndButtonPressed
		case 2:
			event.ButtonState = RightmostButtonPressed
		}
	}

	if (pb & 32) != 0 {
		event.MouseEventFlags |= MouseMoved
	}

	// Modifiers
	if (pb & 4) != 0 {
		event.ControlKeyState |= ShiftPressed
	}
	if (pb & 8) != 0 {
		event.ControlKeyState |= LeftAltPressed
	}
	if (pb & 16) != 0 {
		event.ControlKeyState |= LeftCtrlPressed
	}

	return event, terminatorIdx + 1, nil
}

// ParseKitty parses one Kitty keyboard event. Associated text containing more
// than one rune requires ParseKittyEvents; this function rejects that input
// rather than silently discarding part of a committed text string.
func ParseKitty(data []byte) (*InputEvent, int, error) {
	events, n, err := ParseKittyEvents(data)
	if err != nil {
		return nil, n, err
	}
	if len(events) != 1 {
		return nil, 0, ErrInvalidSequence
	}
	return events[0], n, nil
}

// ParseKittyEvents preserves all runes of the protocol's associated-text field.
// Each resulting text event retains the physical key and modifier information.
// A keycode by itself is not evidence of text input: only associated text marks
// a modified key as TextInput. Plain one-rune typing keeps its legacy semantics.
func ParseKittyEvents(data []byte) ([]*InputEvent, int, error) {
	end, command, err := scanCSI(data)
	if err != nil {
		return nil, 0, err
	}
	fields := strings.Split(string(data[2:end]), ";")
	if len(fields) <= 2 {
		event, n, err := parseKittyKey(data)
		if err != nil {
			return nil, n, err
		}
		return []*InputEvent{event}, n, nil
	}
	if len(fields) != 3 || fields[2] == "" {
		return nil, 0, ErrInvalidSequence
	}
	keySequence := []byte("\x1b[" + fields[0] + ";" + fields[1] + string(command))
	event, _, err := parseKittyKey(keySequence)
	if err != nil {
		return nil, 0, err
	}
	text := strings.Split(fields[2], ":")
	runes := make([]rune, len(text))
	for i, value := range text {
		code, err := strconv.ParseUint(value, 10, 32)
		if err != nil || !isTextRune(rune(code)) {
			return nil, 0, ErrInvalidSequence
		}
		runes[i] = rune(code)
	}
	if !event.KeyDown {
		// A release never commits text, even if a sender includes the field.
		return []*InputEvent{event}, end + 1, nil
	}
	events := make([]*InputEvent, len(runes))
	for i, ch := range runes {
		copy := *event
		copy.Char = ch
		copy.TextInput = len(runes) > 1 || ch != event.Char || event.ControlKeyState&(LeftAltPressed|RightAltPressed|LeftCtrlPressed|RightCtrlPressed) != 0
		events[i] = &copy
	}
	return events, end + 1, nil
}

// parseKittyKey implements the physical-key portion, without associated text.

func parseKittyKey(data []byte) (*InputEvent, int, error) {
	terminatorIdx, command, err := scanCSI(data)
	if err != nil {
		return nil, 0, err
	}

	validTerm := false
	switch command {
	case 'u', '~', 'A', 'B', 'C', 'D', 'E', 'F', 'H', 'P', 'Q', 'R', 'S':
		validTerm = true
	}
	if !validTerm {
		return nil, 0, ErrInvalidSequence
	}

	paramStr := string(data[2:terminatorIdx])

	var params [2][3]int
	firstCount := 0
	secondCount := 0

	i := 0
	for i < len(paramStr) {
		if paramStr[i] == ';' {
			secondCount = 0
			firstCount++
			if firstCount >= 2 {
				return nil, 0, ErrInvalidSequence
			}
			i++
		} else if paramStr[i] == ':' {
			secondCount++
			if secondCount >= 3 {
				return nil, 0, ErrInvalidSequence
			}
			i++
		} else if paramStr[i] >= '0' && paramStr[i] <= '9' {
			val := 0
			for i < len(paramStr) && paramStr[i] >= '0' && paramStr[i] <= '9' {
				val = val*10 + int(paramStr[i]-'0')
				i++
			}
			params[firstCount][secondCount] = val
		} else {
			return nil, 0, ErrInvalidSequence
		}
	}

	eventType := params[1][1]
	modifState := params[1][0]

	event := &InputEvent{
		Type:        KeyEventType,
		InputSource: "kitty",
	}

	if params[0][0] > 0 {
		unc := params[0][0]
		if unc < 32 || unc == 127 || (unc >= 57358 && unc <= 57454) {
			unc = 0
		}
		if unc > 0 && utf8.ValidRune(rune(unc)) {
			event.UnshiftedChar = rune(unc)
		}
	}

	if modifState > 0 {
		modifState--
		if (modifState & 1) != 0 {
			event.ControlKeyState |= ShiftPressed
		}
		if (modifState & 2) != 0 {
			event.ControlKeyState |= LeftAltPressed
		}
		if (modifState & 4) != 0 {
			event.ControlKeyState |= LeftCtrlPressed
		}
		if (modifState & 8) != 0 {
			event.ControlKeyState |= LeftCtrlPressed
		} // Super -> Ctrl (macOS compat)
		if (modifState & 64) != 0 {
			event.ControlKeyState |= CapsLockOn
		}
		if (modifState & 128) != 0 {
			event.ControlKeyState |= NumLockOn
		}
	}

	baseChar := params[0][2]
	if baseChar == 0 {
		baseChar = params[0][0]
	}

	// fix for xterm in ModifyOtherKeys=2 formatOtherKeys=1 mode
	if baseChar <= 255 && baseChar >= 'A' && baseChar <= 'Z' {
		baseChar = int(unicode.ToLower(rune(baseChar)))
	}

	if baseChar <= 255 && baseChar >= 'a' && baseChar <= 'z' {
		event.VirtualKeyCode = uint16(baseChar - 'a' + 0x41)
	}
	if baseChar <= 255 && baseChar >= '0' && baseChar <= '9' {
		event.VirtualKeyCode = uint16(baseChar - '0' + 0x30)
	}

	switch baseChar {
	case '`':
		event.VirtualKeyCode = VK_OEM_3
	case '-':
		event.VirtualKeyCode = VK_OEM_MINUS
	case '=':
		event.VirtualKeyCode = VK_OEM_PLUS
	case '[':
		event.VirtualKeyCode = VK_OEM_4
	case ']':
		event.VirtualKeyCode = VK_OEM_6
	case '\\':
		event.VirtualKeyCode = VK_OEM_5
	case ';':
		event.VirtualKeyCode = VK_OEM_1
	case '\'':
		event.VirtualKeyCode = VK_OEM_7
	case ',':
		event.VirtualKeyCode = VK_OEM_COMMA
	case '.':
		event.VirtualKeyCode = VK_OEM_PERIOD
	case '/':
		event.VirtualKeyCode = VK_OEM_2
	case 9:
		event.VirtualKeyCode = VK_TAB
	case 27:
		event.VirtualKeyCode = VK_ESCAPE
	case 13:
		if command == '~' {
			event.VirtualKeyCode = VK_F3 // workaround for wezterm #3473
		} else {
			event.VirtualKeyCode = VK_RETURN
		}
	case 127:
		event.VirtualKeyCode = VK_BACK
	case 2:
		if command == '~' {
			event.VirtualKeyCode = VK_INSERT
			event.ControlKeyState |= EnhancedKey
		}
	case 3:
		if command == '~' {
			event.VirtualKeyCode = VK_DELETE
			event.ControlKeyState |= EnhancedKey
		}
	case 5:
		if command == '~' {
			event.VirtualKeyCode = VK_PRIOR
			event.ControlKeyState |= EnhancedKey
		}
	case 6:
		if command == '~' {
			event.VirtualKeyCode = VK_NEXT
			event.ControlKeyState |= EnhancedKey
		}
	case 8:
		if command == 'u' {
			event.VirtualKeyCode = VK_BACK
		} // workaround for wezterm #3594
	case 11:
		if command == '~' {
			event.VirtualKeyCode = VK_F1
		}
	case 12:
		if command == '~' {
			event.VirtualKeyCode = VK_F2
		}
	case 14:
		if command == '~' {
			event.VirtualKeyCode = VK_F4
		}
	case 15:
		if command == '~' {
			event.VirtualKeyCode = VK_F5
		}
	case 17:
		if command == '~' {
			event.VirtualKeyCode = VK_F6
		}
	case 18:
		if command == '~' {
			event.VirtualKeyCode = VK_F7
		}
	case 19:
		if command == '~' {
			event.VirtualKeyCode = VK_F8
		}
	case 20:
		if command == '~' {
			event.VirtualKeyCode = VK_F9
		}
	case 21:
		if command == '~' {
			event.VirtualKeyCode = VK_F10
		}
	case 23:
		if command == '~' {
			event.VirtualKeyCode = VK_F11
		}
	case 24:
		if command == '~' {
			event.VirtualKeyCode = VK_F12
		}
	case 32:
		event.VirtualKeyCode = VK_SPACE
	case 57399, 57425:
		event.VirtualKeyCode = VK_NUMPAD0
	case 57400, 57424:
		event.VirtualKeyCode = VK_NUMPAD1
	case 57401, 57420:
		event.VirtualKeyCode = VK_NUMPAD2
	case 57402, 57422:
		event.VirtualKeyCode = VK_NUMPAD3
	case 57403, 57417:
		event.VirtualKeyCode = VK_NUMPAD4
	case 57404, 57427:
		event.VirtualKeyCode = VK_NUMPAD5
	case 57405, 57418:
		event.VirtualKeyCode = VK_NUMPAD6
	case 57406, 57423:
		event.VirtualKeyCode = VK_NUMPAD7
	case 57407, 57419:
		event.VirtualKeyCode = VK_NUMPAD8
	case 57408, 57421:
		event.VirtualKeyCode = VK_NUMPAD9
	case 57409, 57426:
		event.VirtualKeyCode = VK_DECIMAL
	case 57410:
		event.VirtualKeyCode = VK_DIVIDE
	case 57411:
		event.VirtualKeyCode = VK_MULTIPLY
	case 57412:
		event.VirtualKeyCode = VK_SUBTRACT
	case 57413:
		event.VirtualKeyCode = VK_ADD
	case 57414:
		event.VirtualKeyCode = VK_RETURN
	case 57444:
		event.VirtualKeyCode = VK_LWIN
	case 57450:
		event.VirtualKeyCode = VK_RWIN
	case 57363:
		event.VirtualKeyCode = VK_APPS
	case 57448: // Right Ctrl
		event.VirtualKeyCode = VK_CONTROL
		if eventType != 3 {
			event.ControlKeyState |= RightCtrlPressed | EnhancedKey
			// Generic modifiers logic defaults to LeftCtrl; clear it since we know it's Right.
			event.ControlKeyState &= ^ControlKeyState(LeftCtrlPressed)
		}
	case 57442: // Left Ctrl
		event.VirtualKeyCode = VK_CONTROL
		if eventType != 3 {
			event.ControlKeyState |= LeftCtrlPressed
		}
	case 57443: // Left Alt
		event.VirtualKeyCode = VK_MENU
		if eventType != 3 {
			event.ControlKeyState |= LeftAltPressed
		}
	case 57449: // Right Alt
		event.VirtualKeyCode = VK_MENU
		if eventType != 3 {
			event.ControlKeyState |= RightAltPressed | EnhancedKey
			// Generic modifiers logic defaults to LeftAlt; clear it since we know it's Right.
			event.ControlKeyState &= ^ControlKeyState(LeftAltPressed)
		}
	case 57441: // Left Shift
		event.VirtualKeyCode = VK_SHIFT
		event.VirtualScanCode = ScanCodeLeftShift
		if eventType != 3 {
			event.ControlKeyState |= ShiftPressed
		}
	case 57447: // Right Shift
		event.VirtualKeyCode = VK_SHIFT
		event.VirtualScanCode = ScanCodeRightShift
		if eventType != 3 {
			event.ControlKeyState |= ShiftPressed
		}
	case 57360:
		event.VirtualKeyCode = VK_NUMLOCK
	case 57358:
		event.VirtualKeyCode = VK_CAPITAL
	}

	switch command {
	case 'A':
		event.VirtualKeyCode = VK_UP
		event.ControlKeyState |= EnhancedKey
	case 'B':
		event.VirtualKeyCode = VK_DOWN
		event.ControlKeyState |= EnhancedKey
	case 'C':
		event.VirtualKeyCode = VK_RIGHT
		event.ControlKeyState |= EnhancedKey
	case 'D':
		event.VirtualKeyCode = VK_LEFT
		event.ControlKeyState |= EnhancedKey
	case 'E':
		event.VirtualKeyCode = VK_CLEAR // NumPad center (5)
	case 'H':
		event.VirtualKeyCode = VK_HOME
		event.ControlKeyState |= EnhancedKey
	case 'F':
		event.VirtualKeyCode = VK_END
		event.ControlKeyState |= EnhancedKey
	case 'P':
		event.VirtualKeyCode = VK_F1
	case 'Q':
		event.VirtualKeyCode = VK_F2
	case 'R':
		event.VirtualKeyCode = VK_F3
	case 'S':
		event.VirtualKeyCode = VK_F4
	}

	uc := params[0][1]
	if uc == 0 {
		uc = params[0][0]
	}

	if uc < 32 || uc == 127 || (uc >= 57358 && uc <= 57454) {
		uc = 0
	}

	if uc > 0 && utf8.ValidRune(rune(uc)) {
		event.Char = rune(uc)
	} else {
		event.Char = 0
	}

	if event.Char > 0 && event.VirtualKeyCode == 0 {
		event.VirtualKeyCode = VK_UNASSIGNED
	}

	if (event.ControlKeyState&CapsLockOn) != 0 && (event.ControlKeyState&ShiftPressed) == 0 {
		event.Char = unicode.ToUpper(event.Char)
	}

	event.KeyDown = (eventType != 3)
	event.RepeatCount = 1

	if (event.ControlKeyState&LeftAltPressed) != 0 || (event.ControlKeyState&RightAltPressed) != 0 {
		switch event.VirtualKeyCode {
		case VK_ESCAPE, VK_DELETE, VK_BACK, VK_TAB, VK_RETURN, VK_SPACE:
		default:
			if event.Char > 0 {
				event.Char = unicode.ToUpper(event.Char)
			}
		}
	}

	return event, terminatorIdx + 1, nil
}

// ParseMouseLegacy handles standard X10 mouse sequences (ESC [ M Cb Cx Cy).
func ParseMouseLegacy(data []byte) (*InputEvent, int, error) {
	if len(data) < 6 {
		return nil, 0, ErrIncomplete
	}
	if data[0] != 0x1B || data[1] != '[' || data[2] != 'M' {
		return nil, 0, ErrInvalidSequence
	}

	cb := int(data[3]) - 32
	cx := int(data[4]) - 32
	cy := int(data[5]) - 32

	if cx < 1 {
		cx = 1
	}
	if cy < 1 {
		cy = 1
	}

	event := &InputEvent{
		Type:        MouseEventType,
		MouseX:      int16(cx) - 1, // 0-based
		MouseY:      int16(cy) - 1, // 0-based
		InputSource: "legacy_mouse",
	}

	buttonPart := cb & 3
	if (cb & 64) != 0 {
		// Mouse Wheel
		event.KeyDown = true
		if buttonPart == 0 {
			event.WheelDirection = 1 // Up
		} else if buttonPart == 1 {
			event.WheelDirection = -1 // Down
		}
	} else {
		// Normal buttons
		event.KeyDown = buttonPart != 3
		switch buttonPart {
		case 0:
			event.ButtonState = FromLeft1stButtonPressed
		case 1:
			event.ButtonState = FromLeft2ndButtonPressed
		case 2:
			event.ButtonState = RightmostButtonPressed
		}
	}

	if (cb & 32) != 0 {
		event.MouseEventFlags |= MouseMoved
	}

	// Modifiers
	if (cb & 4) != 0 {
		event.ControlKeyState |= ShiftPressed
	}
	if (cb & 8) != 0 {
		event.ControlKeyState |= LeftAltPressed
	}
	if (cb & 16) != 0 {
		event.ControlKeyState |= LeftCtrlPressed
	}

	return event, 6, nil
}

// ParseMouseURXVT handles URXVT 1015 mouse sequences (ESC [ Cb ; Cx ; Cy M).
func ParseMouseURXVT(data []byte) (*InputEvent, int, error) {
	terminatorIdx, command, err := scanCSI(data)
	if err != nil {
		return nil, 0, err
	}

	if command != 'M' {
		return nil, 0, ErrInvalidSequence
	}

	paramStr := string(data[2:terminatorIdx])
	params := strings.Split(paramStr, ";")
	if len(params) != 3 {
		return nil, 0, ErrInvalidSequence
	}

	atoi := func(s string) int {
		v, _ := strconv.Atoi(s)
		return v
	}

	cb := atoi(params[0]) - 32
	cx := atoi(params[1])
	cy := atoi(params[2])

	event := &InputEvent{
		Type:        MouseEventType,
		MouseX:      int16(cx) - 1, // 0-based
		MouseY:      int16(cy) - 1, // 0-based
		InputSource: "urxvt_mouse",
	}

	buttonPart := cb & 3
	if (cb & 64) != 0 {
		// Mouse Wheel
		event.KeyDown = true
		if buttonPart == 0 {
			event.WheelDirection = 1 // Up
		} else if buttonPart == 1 {
			event.WheelDirection = -1 // Down
		}
	} else {
		// Normal buttons
		event.KeyDown = buttonPart != 3
		switch buttonPart {
		case 0:
			event.ButtonState = FromLeft1stButtonPressed
		case 1:
			event.ButtonState = FromLeft2ndButtonPressed
		case 2:
			event.ButtonState = RightmostButtonPressed
		}
	}

	if (cb & 32) != 0 {
		event.MouseEventFlags |= MouseMoved
	}

	// Modifiers
	if (cb & 4) != 0 {
		event.ControlKeyState |= ShiftPressed
	}
	if (cb & 8) != 0 {
		event.ControlKeyState |= LeftAltPressed
	}
	if (cb & 16) != 0 {
		event.ControlKeyState |= LeftCtrlPressed
	}

	return event, terminatorIdx + 1, nil
}
