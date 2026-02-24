package vtinput

import "fmt"

// ControlKeyState flags (dwControlKeyState)
const (
	RightAltPressed  = 0x0001
	LeftAltPressed   = 0x0002
	RightCtrlPressed = 0x0004
	LeftCtrlPressed  = 0x0008
	ShiftPressed     = 0x0010
	NumLockOn        = 0x0020
	ScrollLockOn     = 0x0040
	CapsLockOn       = 0x0080
	EnhancedKey      = 0x0100
)

// Mouse Button States (dwButtonState)
const (
	FromLeft1stButtonPressed = 0x0001
	RightmostButtonPressed   = 0x0002
	FromLeft2ndButtonPressed = 0x0004
	FromLeft3rdButtonPressed = 0x0008
	FromLeft4thButtonPressed = 0x0010
)

// Mouse Event Flags (dwEventFlags)
const (
	MouseMoved      = 0x0001
	DoubleClick     = 0x0002
	MouseWheeled    = 0x0004
	MouseHWheeled   = 0x0008
)

// EventType constants
type EventType uint16

const (
	KeyEventType   EventType = 0x0001
	MouseEventType EventType = 0x0002
	FocusEventType EventType = 0x0010
)

// InputEvent is a generic container for any event (Key, Mouse, Focus).
// Currently, our parser only produces Key events, but the structure is ready for more.
type InputEvent struct {
	Type EventType

	// Key Event Data
	VirtualKeyCode  uint16
	VirtualScanCode uint16
	Char            rune
	KeyDown         bool
	RepeatCount     uint16

	// Mouse Event Data (Future proofing)
	MouseX          uint16
	MouseY          uint16
	ButtonState     uint32
	MouseEventFlags uint32
	WheelDirection  int // 1 (forward/right), -1 (backward/left)

	// Focus Event Data
	SetFocus bool

	// Shared
	ControlKeyState uint32

	// IsLegacy indicates that this event comes from a protocol that does not support
	// explicit KeyUp events (e.g. standard ANSI). The application may need to
	// simulate KeyUp after a timeout.
	IsLegacy bool
}

// String implements the Stringer interface for easy debugging.
func (e InputEvent) String() string {
	legacyStr := ""
	if e.IsLegacy {
		legacyStr = " [Legacy]"
	}

	if e.Type == KeyEventType {
		state := "UP"
		if e.KeyDown {
			state = "DOWN"
		}
		charStr := ""
		if e.Char > 0 {
			if e.Char < 32 {
				charStr = fmt.Sprintf(" Char:\\x%02X", e.Char)
			} else {
				charStr = fmt.Sprintf(" Char:'%c'", e.Char)
			}
		}
		return fmt.Sprintf("Key{VK:0x%X Scan:0x%X%s %s Mods:0x%X}%s",
			e.VirtualKeyCode, e.VirtualScanCode, charStr, state, e.ControlKeyState, legacyStr)
	}

	if e.Type == MouseEventType {
		btn := "None"
		switch e.ButtonState {
		case FromLeft1stButtonPressed:
			btn = "Left"
		case FromLeft2ndButtonPressed:
			btn = "Middle"
		case RightmostButtonPressed:
			btn = "Right"
		}

		action := "UP"
		if e.KeyDown {
			action = "DOWN"
		}
		if (e.MouseEventFlags & MouseMoved) != 0 {
			action = "MOVE"
		}

		wheel := ""
		if e.WheelDirection > 0 {
			wheel = " WHEEL_UP"
		}
		if e.WheelDirection < 0 {
			wheel = " WHEEL_DOWN"
		}

		return fmt.Sprintf("Mouse{Pos:%d,%d Btn:%s %s%s Mods:0x%X}%s",
			e.MouseX, e.MouseY, btn, action, wheel, e.ControlKeyState, legacyStr)
	}

	return fmt.Sprintf("Event{Type:%d Mods:0x%X}%s", e.Type, e.ControlKeyState, legacyStr)
}
