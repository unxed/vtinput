package vtinput

import (
	"fmt"
	"strings"
	"testing"
)

func TestWin32AltGrText(t *testing.T) {
	for _, tt := range []struct {
		name string
		char rune
		mods ControlKeyState
		down bool
		text bool
	}{
		{"AltGr", '@', RightAltPressed | LeftCtrlPressed, true, true},
		{"right Alt text", 'ą', RightAltPressed, true, true},
		{"shift AltGr", 'Ą', RightAltPressed | LeftCtrlPressed | ShiftPressed, true, true},
		{"joiner", '\u200d', RightAltPressed | LeftCtrlPressed, true, true},
		{"variation selector", '\ufe0f', RightAltPressed | LeftCtrlPressed, true, true},
		{"plain", 'a', 0, true, false},
		{"Alt", 'a', LeftAltPressed, true, false},
		{"Ctrl Alt", 'a', LeftCtrlPressed | LeftAltPressed, true, false},
		{"right Ctrl Alt", 'a', RightCtrlPressed | RightAltPressed, true, false},
		{"nontext", 0, RightAltPressed | LeftCtrlPressed, true, false},
		{"control character", '\r', RightAltPressed, true, false},
		{"C1 control", '\u0085', RightAltPressed, true, false},
		{"invalid scalar", 0xD83D, RightAltPressed, true, false},
		{"keyup", '@', RightAltPressed | LeftCtrlPressed, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			down := 0
			if tt.down {
				down = 1
			}
			seq := fmt.Sprintf("\x1b[81;16;%d;%d;%d;3_", tt.char, down, tt.mods)
			event, n, err := ParseWin32InputEvent([]byte(seq))
			if err != nil || n != len(seq) {
				t.Fatalf("parse: n=%d err=%v", n, err)
			}
			if event.TextInput != tt.text || event.Char != tt.char || event.KeyDown != tt.down ||
				event.VirtualKeyCode != VK_Q || event.VirtualScanCode != 16 ||
				event.ControlKeyState != tt.mods || event.RepeatCount != 3 || event.InputSource != "win32" {
				t.Fatalf("lost text semantics or physical data: %+v", event)
			}
		})
	}
}

func TestWindowsTextDecoderSurrogates(t *testing.T) {
	for _, source := range []string{"ConPTY", "win32"} {
		t.Run(source, func(t *testing.T) {
			var decoder windowsTextDecoder
			high := &InputEvent{Type: KeyEventType, KeyDown: true, Char: 0xD83D,
				VirtualKeyCode: VK_Q, VirtualScanCode: 16, RepeatCount: 2,
				ControlKeyState: RightAltPressed | LeftCtrlPressed, InputSource: source}
			low := *high
			low.Char = 0xDE00
			if got := decoder.decode(high); got != nil {
				t.Fatalf("incomplete surrogate escaped as a key: %+v", got)
			}
			got := decoder.decode(&low)
			if got == nil || got.Char != '😀' || !got.TextInput || got.VirtualKeyCode != VK_Q ||
				got.VirtualScanCode != 16 || got.ControlKeyState != high.ControlKeyState || got.RepeatCount != 2 {
				t.Fatalf("surrogate pair lost text or physical data: %+v", got)
			}
			if high.Char != 0xD83D || low.Char != 0xDE00 {
				t.Fatal("decoder mutated original records")
			}
			if got := decoder.decode(&low); got != nil {
				t.Fatalf("orphan low surrogate escaped: %+v", got)
			}
			decoder.decode(high)
			shortcut := &InputEvent{Type: KeyEventType, KeyDown: true, VirtualKeyCode: VK_F1, InputSource: source}
			if got := decoder.decode(shortcut); got != shortcut || got.TextInput {
				t.Fatalf("nontext key following incomplete input changed: %+v", got)
			}
			if got := decoder.decode(&low); got != nil {
				t.Fatalf("stale high surrogate reused: %+v", got)
			}
		})
	}
}

func TestReaderWin32SurrogateText(t *testing.T) {
	reader := NewReader(strings.NewReader("\x1b[81;16;55357;1;9;2_\x1b[81;16;56832;1;9;2_"), false)
	defer reader.Close()
	got, err := reader.ReadEvent()
	if err != nil || got == nil || got.Char != '😀' || !got.TextInput || got.RepeatCount != 2 {
		t.Fatalf("reader: %+v, %v", got, err)
	}
}

func TestWindowsTextDecoderRejectsUnrelatedSurrogates(t *testing.T) {
	for _, differentScan := range []bool{false, true} {
		var decoder windowsTextDecoder
		high := &InputEvent{Type: KeyEventType, KeyDown: true, Char: 0xD83D,
			VirtualKeyCode: VK_Q, VirtualScanCode: 16, RepeatCount: 1,
			ControlKeyState: RightAltPressed | LeftCtrlPressed, InputSource: "win32"}
		low := *high
		low.Char = 0xDE00
		if differentScan {
			low.VirtualScanCode++
		} else {
			low.VirtualKeyCode++
		}
		decoder.decode(high)
		if got := decoder.decode(&low); got != nil {
			t.Fatalf("joined code units from unrelated physical keys: %+v", got)
		}
	}
}

func TestKittyAssociatedText(t *testing.T) {
	for _, tt := range []struct {
		name string
		seq  string
		char rune
		text bool
		down bool
	}{
		{"physical Ctrl Alt", "\x1b[113;7u", 'Q', false, true},
		{"associated Ctrl Alt", "\x1b[113;7;64u", '@', true, true},
		{"plain", "\x1b[113;1;113u", 'q', false, true},
		{"composed", "\x1b[113;1;261u", 'ą', true, true},
		{"repeat", "\x1b[113;7:2;64u", '@', true, true},
		{"release", "\x1b[113;7:3;64u", 'Q', false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, n, err := ParseKitty([]byte(tt.seq))
			if err != nil || n != len(tt.seq) || got == nil {
				t.Fatalf("parse: %+v, %d, %v", got, n, err)
			}
			if got.Char != tt.char || got.TextInput != tt.text || got.KeyDown != tt.down || got.VirtualKeyCode != VK_Q || got.InputSource != "kitty" {
				t.Fatalf("unexpected event: %+v", got)
			}
		})
	}
}

func TestKittyAssociatedTextBatchAndReader(t *testing.T) {
	seq := "\x1b[113;7;64:261:128512u"
	if _, _, err := ParseKitty([]byte(seq)); err != ErrInvalidSequence {
		t.Fatalf("single-event API silently truncated text: %v", err)
	}
	events, n, err := ParseKittyEvents([]byte(seq))
	if err != nil || n != len(seq) || len(events) != 3 {
		t.Fatalf("batch: %+v, %d, %v", events, n, err)
	}
	reader := NewReader(strings.NewReader(seq+"\x1b[13;1u"), false)
	defer reader.Close()
	for _, ch := range "@ą😀" {
		got, err := reader.ReadEvent()
		if err != nil || got == nil || got.Char != ch || !got.TextInput || got.VirtualKeyCode != VK_Q ||
			got.ControlKeyState != LeftCtrlPressed|LeftAltPressed || got.UnshiftedChar != 'q' {
			t.Fatalf("reader text %q: %+v, %v", ch, got, err)
		}
	}
	got, err := reader.ReadEvent()
	if err != nil || got.VirtualKeyCode != VK_RETURN || got.TextInput {
		t.Fatalf("key after text: %+v, %v", got, err)
	}
}

func TestKittyAssociatedTextRejectsInvalidScalars(t *testing.T) {
	for _, text := range []string{"55357", "1114112", "4294967295", "0", "133", "64:", ""} {
		if _, _, err := ParseKittyEvents([]byte("\x1b[113;7;" + text + "u")); err == nil {
			t.Errorf("accepted invalid associated text %q", text)
		}
	}
}

func TestReaderKittyEmojiComposition(t *testing.T) {
	// WOMAN + ZERO WIDTH JOINER + PERSONAL COMPUTER + VARIATION SELECTOR-16.
	reader := NewReader(strings.NewReader("\x1b[113;7;128105:8205:128187:65039u"), false)
	defer reader.Close()
	for _, ch := range "👩\u200d💻\ufe0f" {
		got, err := reader.ReadEvent()
		if err != nil || got == nil || got.Char != ch || !got.TextInput || got.VirtualKeyCode != VK_Q ||
			got.ControlKeyState != LeftCtrlPressed|LeftAltPressed {
			t.Fatalf("composition rune U+%04X: %+v, %v", ch, got, err)
		}
	}
}
