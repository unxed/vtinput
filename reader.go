package vtinput

import (
	"io"
	"time"
	"unicode/utf8"
)

// Reader wraps an io.Reader (like os.Stdin) and parses input events.
// It buffers input internally to handle incomplete escape sequences.
type Reader struct {
	in       io.Reader
	buf      []byte
	dataChan chan byte
	errChan  chan error
}

// NewReader creates a new Reader instance and starts a background pumper.
func NewReader(in io.Reader) *Reader {
	r := &Reader{
		in:       in,
		buf:      make([]byte, 0, 128),
		dataChan: make(chan byte, 1024),
		errChan:  make(chan error, 1),
	}

	// Background goroutine: read from io.Reader and send to dataChan
	go func() {
		tmp := make([]byte, 256)
		for {
			n, err := r.in.Read(tmp)
			if n > 0 {
				for i := 0; i < n; i++ {
					r.dataChan <- tmp[i]
				}
			}
			if err != nil {
				r.errChan <- err
				return
			}
		}
	}()

	return r
}

// ReadEvent reads the next input event.
func (r *Reader) ReadEvent() (*InputEvent, error) {
	for {
		if len(r.buf) > 0 {
			// Optimization: Only attempt to parse sequences if the buffer starts with ESC.
			// This prevents normal characters (1, 2, a, b...) from triggering "Incomplete"
			// errors in parsers, which caused the "off-by-one" lag.
			if r.buf[0] == 0x1B {
				// 1. Handle SS3 sequences (ESC O ...)
				if event, consumed, err := ParseLegacySS3(r.buf); err == nil {
					r.buf = r.buf[consumed:]
					return event, nil
				} else if err == ErrIncomplete {
					goto waitForMore
				}

				// 2. Handle CSI sequences (ESC [ ...)
				if _, command, err := scanCSI(r.buf); err == nil {
					var event *InputEvent
					var consumed int
					var pErr error

					switch command {
					case '_': // Win32 Input Mode
						event, consumed, pErr = ParseWin32InputEvent(r.buf)
					case 'u': // Future Kitty Keyboard Protocol
						// event, consumed, pErr = ParseKitty(r.buf)
						event, consumed, pErr = ParseLegacyCSI(r.buf)
					case 'M', 'm': // SGR Mouse
						event, consumed, pErr = ParseMouseSGR(r.buf)
					default: // Standard Arrows, F-keys, etc.
						event, consumed, pErr = ParseLegacyCSI(r.buf)
					}

					if pErr == nil && event != nil {
						r.buf = r.buf[consumed:]
						return event, nil
					}
					// If parser failed, we'll treat it as possible Alt or just Esc below
				} else if err == ErrIncomplete {
					goto waitForMore
				}

				// 3. Handle Double ESC
				if len(r.buf) >= 2 && r.buf[1] == 0x1B {
					r.buf = r.buf[1:] // Consume one ESC
					return &InputEvent{Type: KeyEventType, VirtualKeyCode: VK_ESCAPE, KeyDown: true}, nil
				}

				// 5. Handle Legacy Alt (ESC + Char)
				if len(r.buf) >= 2 && utf8.FullRune(r.buf[1:]) {
					r.buf = r.buf[1:] // Consume ESC
					character, size := utf8.DecodeRune(r.buf)
					r.buf = r.buf[size:]
					return &InputEvent{
						Type:            KeyEventType,
						Char:            character,
						ControlKeyState: LeftAltPressed,
						KeyDown:         true,
					}, nil
				}

			waitForMore:
				// If we have just [0x1B], wait for more data with a 100ms timeout.
				select {
				case b := <-r.dataChan:
					r.buf = append(r.buf, b)
					continue
				case <-time.After(100 * time.Millisecond):
					r.buf = r.buf[1:]
					return &InputEvent{Type: KeyEventType, VirtualKeyCode: VK_ESCAPE, KeyDown: true}, nil
				case err := <-r.errChan:
					// Before returning error, try a non-blocking read to see if data arrived
					select {
					case b := <-r.dataChan:
						r.buf = append(r.buf, b)
						continue
					default:
						return nil, err
					}
				}
			}

			// 6. Single byte / UTF-8 / Ctrl-keys / Backspace (0x7F)
			if r.buf[0] == 0x7F {
				r.buf = r.buf[1:]
				return &InputEvent{Type: KeyEventType, VirtualKeyCode: VK_BACK, KeyDown: true}, nil
			}

			if utf8.FullRune(r.buf) {
				character, size := utf8.DecodeRune(r.buf)
				r.buf = r.buf[size:]
				if event := translateLegacyByte(character); event != nil {
					return event, nil
				}
				return &InputEvent{Type: KeyEventType, Char: character, KeyDown: true}, nil
			}
		}

		// wait for at least one byte to arrive
		select {
		case b := <-r.dataChan:
			r.buf = append(r.buf, b)
		case err := <-r.errChan:
			// Check for data one last time before returning the error
			select {
			case b := <-r.dataChan:
				r.buf = append(r.buf, b)
			default:
				return nil, err
			}
		}
	}
}
// translateLegacyByte converts C0 control characters (0x01-0x1A)
// to InputEvents with VirtualKeyCodes and Ctrl modifier.
func translateLegacyByte(r rune) *InputEvent {
	switch r {
	case 0x00: // Ctrl + Space
		return &InputEvent{Type: KeyEventType, VirtualKeyCode: VK_SPACE, Char: ' ', ControlKeyState: LeftCtrlPressed, KeyDown: true}
	case 0x08: // Backspace (Ctrl + H)
		return &InputEvent{Type: KeyEventType, VirtualKeyCode: VK_BACK, KeyDown: true}
	case 0x09: // Tab
		return &InputEvent{Type: KeyEventType, VirtualKeyCode: VK_TAB, Char: '\t', KeyDown: true}
	case 0x0D: // Enter
		return &InputEvent{Type: KeyEventType, VirtualKeyCode: VK_RETURN, Char: '\r', KeyDown: true}
	case 0x1B: // Esc
		return &InputEvent{Type: KeyEventType, VirtualKeyCode: VK_ESCAPE, KeyDown: true}
	case 0x1C: // Ctrl + \
		return &InputEvent{Type: KeyEventType, VirtualKeyCode: VK_OEM_5, ControlKeyState: LeftCtrlPressed, KeyDown: true}
	case 0x1D: // Ctrl + ]
		return &InputEvent{Type: KeyEventType, VirtualKeyCode: VK_OEM_6, ControlKeyState: LeftCtrlPressed, KeyDown: true}
	case 0x1E: // Ctrl + ^ (6)
		return &InputEvent{Type: KeyEventType, VirtualKeyCode: VK_6, ControlKeyState: LeftCtrlPressed, KeyDown: true}
	case 0x1F: // Ctrl + _ (-)
		return &InputEvent{Type: KeyEventType, VirtualKeyCode: VK_OEM_MINUS, ControlKeyState: LeftCtrlPressed, KeyDown: true}
	}

	// Ctrl+A is 1, Ctrl+Z is 26 (excluding handled above)
	if r >= 1 && r <= 26 {
		return &InputEvent{
			Type:            KeyEventType,
			VirtualKeyCode:  uint16(VK_A + (r - 1)),
			ControlKeyState: LeftCtrlPressed,
			KeyDown:         true,
		}
	}
	return nil
}
