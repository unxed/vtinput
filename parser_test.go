package vtinput

import (
	"bytes"
	"reflect"
	"testing"
)

func TestScanCSI(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected int
		cmd      byte
		err      error
	}{
		{"Valid Arrow", []byte("\x1b[A"), 2, 'A', nil},
		{"Valid Win32", []byte("\x1b[17;29;0;1;8;1_"), 15, '_', nil},
		{"Incomplete", []byte("\x1b[1;5"), 0, 0, ErrIncomplete},
		{"Invalid Start", []byte("ABC"), 0, 0, ErrInvalidSequence},
		{"Invalid Middle", []byte("\x1b[1\x07A"), 0, 0, ErrInvalidSequence},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx, cmd, err := scanCSI(tt.data)
			if err != tt.err || idx != tt.expected || cmd != tt.cmd {
				t.Errorf("got (%d, %c, %v), want (%d, %c, %v)", idx, cmd, err, tt.expected, tt.cmd, tt.err)
			}
		})
	}
}

func TestParseWin32InputEvent(t *testing.T) {
	data := []byte("\x1b[112;59;0;1;8;1_")
	expected := &InputEvent{
		Type:            KeyEventType,
		VirtualKeyCode:  0x70, // VK_F1
		VirtualScanCode: 0x3B,
		Char:            0,
		KeyDown:         true,
		ControlKeyState: 0x08, // LeftCtrl
		RepeatCount:     1,
	}

	event, consumed, err := ParseWin32InputEvent(data)
	if err != nil || consumed != len(data) || !reflect.DeepEqual(event, expected) {
		t.Errorf("failed to parse Win32: got %+v, err %v", event, err)
	}
}

func TestParseLegacyCSI(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected uint16
		mods     uint32
	}{
		{"Up Arrow", []byte("\x1b[A"), VK_UP, 0},
		{"Ctrl+Up", []byte("\x1b[1;5A"), VK_UP, LeftCtrlPressed},
		{"Shift+Alt+Down", []byte("\x1b[1;4B"), VK_DOWN, ShiftPressed | LeftAltPressed},
		{"F5", []byte("\x1b[15~"), VK_F5, 0},
		{"Ctrl+Delete", []byte("\x1b[3;5~"), VK_DELETE, LeftCtrlPressed},
		{"Shift+Tab", []byte("\x1b[Z"), VK_TAB, ShiftPressed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, _, err := ParseLegacyCSI(tt.data)
			if err != nil || event.VirtualKeyCode != tt.expected || event.ControlKeyState != tt.mods {
				t.Errorf("%s: got (VK:0x%X, Mods:0x%X), want (0x%X, 0x%X)", tt.name, event.VirtualKeyCode, event.ControlKeyState, tt.expected, tt.mods)
			}
		})
	}
}

func TestParseLegacySS3(t *testing.T) {
	data := []byte("\x1bOR")
	event, consumed, err := ParseLegacySS3(data)
	if err != nil || consumed != 3 || event.VirtualKeyCode != VK_F3 {
		t.Errorf("failed to parse SS3: got %+v, err %v", event, err)
	}
}

func TestParseMouseSGR(t *testing.T) {
	data := []byte("\x1b[<0;10;20M")
	event, _, err := ParseMouseSGR(data)
	if err != nil || event.MouseX != 10 || event.MouseY != 20 || event.ButtonState != FromLeft1stButtonPressed || !event.KeyDown {
		t.Errorf("failed to parse Mouse SGR: got %+v, err %v", event, err)
	}
}

func TestReadEvent_AltCyrillic(t *testing.T) {
	input := []byte{0x1B, 0xD0, 0xB0}
	r := NewReader(bytes.NewReader(input))

	event, err := r.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent failed: %v", err)
	}

	if event.Char != 'а' || event.ControlKeyState != LeftAltPressed {
		t.Errorf("expected Alt+а, got Char:%c Mods:0x%X", event.Char, event.ControlKeyState)
	}
}
func TestReadEvent_Mixed(t *testing.T) {
	// 1. Ctrl+C (0x03), 2. Shift+Tab (ESC [ Z), 3. Double ESC (1B 1B)
	input := []byte{0x03, 0x1B, '[', 'Z', 0x1B, 0x1B}
	r := NewReader(bytes.NewReader(input))

	// Check Ctrl+C
	e, _ := r.ReadEvent()
	if e.VirtualKeyCode != VK_C || (e.ControlKeyState&LeftCtrlPressed) == 0 {
		t.Errorf("Expected Ctrl+C, got %+v", e)
	}

	// Check Shift+Tab
	e, _ = r.ReadEvent()
	if e.VirtualKeyCode != VK_TAB || (e.ControlKeyState&ShiftPressed) == 0 {
		t.Errorf("Expected Shift+Tab, got %+v", e)
	}

	// Check Double ESC
	e, _ = r.ReadEvent()
	if e.VirtualKeyCode != VK_ESCAPE {
		t.Errorf("Expected VK_ESCAPE from double ESC, got %+v", e)
	}
}
