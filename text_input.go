package vtinput

import (
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Committed text includes format characters such as the zero-width joiner used
// in emoji sequences. IsPrint would incorrectly reject those characters.
func isTextRune(char rune) bool {
	return char >= ' ' && utf8.ValidRune(char) && !unicode.IsControl(char)
}

// Windows supplies the character produced by its layout in the input record.
// Right Alt alone or with the synthetic Left Ctrl is AltGr; Left Alt and Right
// Ctrl combinations retain shortcut semantics. Never consult this machine's
// layout: a Win32 protocol record can originate on a different computer.
func markWindowsText(event *InputEvent) {
	mods := event.ControlKeyState
	event.TextInput = event.Type == KeyEventType && event.KeyDown &&
		isTextRune(event.Char) && mods&RightAltPressed != 0 &&
		mods&(LeftAltPressed|RightCtrlPressed) == 0
}

// A native console record (and its Win32 protocol representation) holds one
// UTF-16 code unit. Buffer a high surrogate until the matching low surrogate;
// never let an incomplete character escape as a physical shortcut.
type windowsTextDecoder struct {
	pending [2]*InputEvent
}

func (d *windowsTextDecoder) decode(event *InputEvent) *InputEvent {
	if event.Type != KeyEventType || (event.InputSource != "ConPTY" && event.InputSource != "win32") {
		d.pending = [2]*InputEvent{}
		return event
	}
	index := 0
	if event.KeyDown {
		index = 1
	}
	if event.Char >= 0xD800 && event.Char <= 0xDBFF {
		copy := *event
		d.pending[index] = &copy
		return nil
	}
	pending := d.pending[index]
	d.pending[index] = nil
	if event.Char >= 0xDC00 && event.Char <= 0xDFFF {
		if pending == nil || pending.InputSource != event.InputSource ||
			pending.ControlKeyState != event.ControlKeyState || pending.RepeatCount != event.RepeatCount ||
			pending.VirtualKeyCode != event.VirtualKeyCode || pending.VirtualScanCode != event.VirtualScanCode {
			return nil
		}
		pending.Char = utf16.DecodeRune(pending.Char, event.Char)
		event = pending
	}
	markWindowsText(event)
	return event
}
