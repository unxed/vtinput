package vtinput

import (
	"io"
	"sync"
	"time"
	"unicode/utf8"
)

// Reader reads input synchronously without a background goroutine.
// It measures the time from receiving raw bytes to generating an InputEvent.
// plan9Read carries one completed read from the Plan 9 pump goroutine. The
// type is declared here rather than in reader_plan9.go because Reader has a
// field of this type and Reader is compiled on every platform.
type plan9Read struct {
	n   int
	err error
	buf []byte
}

// consoleRead carries one completed read from the Windows console pump
// goroutine, for the same reason plan9Read exists for Plan 9.
type consoleRead struct {
	n   int
	err error
	buf []byte
}

type Reader struct {
	in                     io.Reader
	buf                    []byte
	done                   chan struct{}
	useConPTY              bool // Windows only
	far2lExtensionsEnabled bool
	conHandle              uintptr // Windows only: input handle (native console path, pump otherwise)
	cancelEvent            uintptr // Windows only: event handle for cancellation
	oldMode                uint32  // Windows only: saved console mode
	stopPipe               [2]int  // Unix only: pipe for interrupting Poll

	// Plan 9 only: it has no poll and no way to interrupt a blocked read,
	// so reads happen in a goroutine that reports over a channel.
	p9reads chan plan9Read
	p9stop  chan struct{}
	p9once  sync.Once

	// Windows only: a console input buffer, and a file that refuses a
	// deadline (an anonymous pipe), are not pollable — a read on them either
	// never ends or swallows any deadline arranged from the outside: a record
	// that yields no bytes (a key-up) signals the handle, the read starts,
	// and only the next byte-producing record ends it. So those inputs get
	// the same shape as Plan 9: one goroutine owns the read and reports over
	// a channel, and the deadline is the select on that channel.
	conreads chan consoleRead
	conpend  []byte // bytes read by the pump but not handed out yet
	conErr   error  // terminal pump error, returned once conreads is closed

	// Windows only: whether this reader pumps is a property of its input and
	// is decided once, on the first read.
	readPathOnce sync.Once
	pumpedRead   bool

	mu             sync.Mutex
	lastLatency    time.Duration
	totalLatency   time.Duration
	eventCount     int64
	lastReceivedAt time.Time

	EventChan     chan *InputEvent // need to be public for vtui
	onceEventChan sync.Once

	bypassReadEvent bool // for use by GUI backends (e.g. X11) to inject events directly

	MetricsEnabled bool

	// State for the wezterm + Win32InputMode double-wrap mouse
	// workaround (see win_double_unwrap.go).
	winDoubleBuf []byte
	winWrapping  bool

	pendingTextEvents []*InputEvent
	consoleText       windowsTextDecoder
}

// NewReader creates a synchronous input reader.
func NewReader(in io.Reader, bypassReadEvent bool) *Reader {
	r := &Reader{
		in:              in,
		buf:             make([]byte, 0, 128),
		done:            make(chan struct{}),
		EventChan:       make(chan *InputEvent, 1024),
		bypassReadEvent: bypassReadEvent,
	}
	r.platformInit(in)
	return r
}

// Close stops reading.
func (r *Reader) Close() {
	select {
	case <-r.done:
		return
	default:
		close(r.done)
		if r.bypassReadEvent {
			close(r.EventChan)
		}

		r.platformClose()
	}
}

const (
	// Some Windows console hosts briefly report ERROR_PIPE_NOT_CONNECTED while
	// applying a buffer/window resize. Treating that single read failure as EOF
	// makes vtui leave its main loop and makes f4 look like it crashed.
	readEventRetryDelay     = 25 * time.Millisecond
	maxTransientReadRetries = 80 // keep a genuinely disconnected console bounded
)

// GetEventChan returns a channel that yields input events.
// It starts a background goroutine that calls ReadEvent() in a loop.
// The channel is closed when the reader is closed or an unrecoverable error occurs.
func (r *Reader) GetEventChan() chan *InputEvent {
	if !r.bypassReadEvent {
		r.onceEventChan.Do(func() {
			go func() {
				defer close(r.EventChan)
				transientReadRetries := 0
				for {
					e, err := r.ReadEvent()
					if err != nil {
						if isRetryableReadError(err) && transientReadRetries < maxTransientReadRetries {
							transientReadRetries++
							Log("ReadEvent transient error: %v; retry %d/%d", err, transientReadRetries, maxTransientReadRetries)
							timer := time.NewTimer(readEventRetryDelay)
							select {
							case <-timer.C:
								continue
							case <-r.done:
								if !timer.Stop() {
									select {
									case <-timer.C:
									default:
									}
								}
								return
							}
						}
						Log("ReadEvent error: %v", err)
						return
					}
					transientReadRetries = 0

					select {
					case r.EventChan <- e:
					case <-r.done:
						return
					}
				}
			}()
		})
	}

	return r.EventChan
}

// ReadEvent reads the next input event.
func (r *Reader) ReadEvent() (*InputEvent, error) {
	return r.ReadEventTimeout(0)
}

// ReadEventTimeout reads the next input event with an optional timeout.
func (r *Reader) ReadEventTimeout(timeout time.Duration) (*InputEvent, error) {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		event, err := r.readEventTimeout(timeout)
		if err != nil || event == nil {
			return event, err
		}
		if event = r.consoleText.decode(event); event != nil {
			return event, nil
		}
		if !deadline.IsZero() {
			timeout = time.Until(deadline)
			if timeout <= 0 {
				return nil, nil
			}
		}
	}
}

func (r *Reader) readEventTimeout(timeout time.Duration) (*InputEvent, error) {
	if len(r.pendingTextEvents) > 0 {
		event := r.pendingTextEvents[0]
		r.pendingTextEvents = r.pendingTextEvents[1:]
		return event, nil
	}
	// Fast path for Windows ConPTY
	if r.useConPTY {
		return r.readConPTYEventTimeout(timeout)
	}

	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}

	tmp := make([]byte, 1024)

	for {
		select {
		case <-r.done:
			return nil, io.EOF
		default:
		}

		if !deadline.IsZero() && time.Now().After(deadline) {
			return nil, nil
		}

		if len(r.buf) > 0 {
			if r.buf[0] == 0x1B {
				altOffset := 0
				parseBuf := r.buf
				if len(r.buf) >= 3 && r.buf[0] == 0x1B && r.buf[1] == 0x1B {
					c := r.buf[2]
					if c == '[' || c == 'O' || c == '<' || c == '_' {
						altOffset = 1
						parseBuf = r.buf[1:]
					}
				}

				// 0. Wezterm + Win32InputMode workaround. When both are on,
				// wezterm delivers SGR mouse events by wrapping each byte
				// into its own Win32 keystroke event. Detect the signature
				// (buffer starts with \x1b[0;0;27 — a Win32 event whose Uc
				// is 0x1b/ESC) and start peeling; once the recovered bytes
				// form a real escape sequence, prepend them to r.buf so the
				// normal parsers below pick it up next iteration. See
				// win_double_unwrap.go for the mechanics.
				if isWinWrappedStart(parseBuf) || r.winWrapping {
					if peeled, ok := peelWinWrapped(parseBuf, &r.winDoubleBuf); ok {
						r.winWrapping = true
						r.buf = r.buf[peeled+altOffset:]
						if isWinDoubleComplete(r.winDoubleBuf) {
							r.buf = append(append([]byte{}, r.winDoubleBuf...), r.buf...)
							r.winDoubleBuf = r.winDoubleBuf[:0]
							r.winWrapping = false
						}
						continue
					}
					if len(parseBuf) < 32 {
						goto waitForMore
					}
					// Runaway accumulation: give up and let the normal
					// chain try to make sense of whatever is in the buffer.
					r.winDoubleBuf = r.winDoubleBuf[:0]
					r.winWrapping = false
				}

				// 1. Focus
				if len(parseBuf) >= 3 && parseBuf[1] == '[' && (parseBuf[2] == 'I' || parseBuf[2] == 'O') {
					isVteBrokenSS3 := false
					if parseBuf[2] == 'O' {
						if len(parseBuf) <= 3 {
							// ESC [ O is the prefix of both focus-out and the
							// broken-SS3 function keys (ESC [ O 3 P is Alt+F1
							// from VTE and from the FreeBSD console). Three
							// bytes are not enough to tell them apart, and
							// guessing focus here swallows the ESC [ O and
							// leaves "3P" to be typed as text. Wait instead;
							// waitForMore re-checks and emits the focus event
							// if no fourth byte ever arrives.
							goto waitForMore
						}
						c := parseBuf[3]
						if (c >= '0' && c <= '9') || c == 'P' || c == 'Q' || c == 'R' || c == 'S' {
							isVteBrokenSS3 = true
						}
					}
					if !isVteBrokenSS3 {
						event := &InputEvent{Type: FocusEventType, SetFocus: parseBuf[2] == 'I'}
						r.buf = r.buf[3+altOffset:]
						r.recordLatency(time.Since(r.lastReceivedAt))
						Log("Reader: Parsed Focus %v.", event.SetFocus)
						return event, nil
					}
				}

				// 2. DSR Replies (ESC [ ... n)
				if len(parseBuf) > 2 && parseBuf[1] == '[' {
					if terminatorIdx, cmd, err := scanCSI(parseBuf); err == nil {
						if cmd == 'n' {
							r.buf = r.buf[terminatorIdx+1+altOffset:]
							continue
						}
					} else if err == ErrIncomplete {
						goto waitForMore
					}
				}

				// 3. APC (Far2l)
				if len(parseBuf) > 1 && parseBuf[1] == '_' {
					if event, consumed, err := ParseFar2lAPC(parseBuf); err == nil {
						r.buf = r.buf[consumed+altOffset:]
						if event != nil {
							if event.Type == Far2lEventType && event.Far2lCommand == "ok" {
								r.far2lExtensionsEnabled = true
							}
							r.recordLatency(time.Since(r.lastReceivedAt))
							Log("Reader: Parsed far2l event: %s", event.String())
							return event, nil
						}
						continue
					} else if err == ErrIncomplete {
						goto waitForMore
					}
				}

				// 4. Bracketed Paste (ESC [ 2 0 0 ~ / ESC [ 2 0 1 ~)
				if len(parseBuf) >= 6 && parseBuf[1] == '[' && parseBuf[2] == '2' && parseBuf[3] == '0' && (parseBuf[4] == '0' || parseBuf[4] == '1') && parseBuf[5] == '~' {
					event := &InputEvent{Type: PasteEventType, PasteStart: parseBuf[4] == '0'}
					r.buf = r.buf[6+altOffset:]
					r.recordLatency(time.Since(r.lastReceivedAt))
					return event, nil
				}

				// 5. SGR Mouse
				if len(parseBuf) > 3 && parseBuf[1] == '[' && parseBuf[2] == '<' {
					if event, consumed, err := ParseMouseSGR(parseBuf); err == nil {
						r.buf = r.buf[consumed+altOffset:]
						if altOffset > 0 {
							event.ControlKeyState |= LeftAltPressed
						}
						r.recordLatency(time.Since(r.lastReceivedAt))
						return event, nil
					} else if err == ErrIncomplete {
						goto waitForMore
					}
				}

				// 5.5. Legacy Mouse (ESC [ M Cb Cx Cy)
				if len(parseBuf) >= 3 && parseBuf[1] == '[' && parseBuf[2] == 'M' {
					if event, consumed, err := ParseMouseLegacy(parseBuf); err == nil {
						r.buf = r.buf[consumed+altOffset:]
						if altOffset > 0 {
							event.ControlKeyState |= LeftAltPressed
						}
						r.recordLatency(time.Since(r.lastReceivedAt))
						return event, nil
					} else if err == ErrIncomplete {
						goto waitForMore
					}
				}

				// 5.6. URXVT Mouse (ESC [ Cb ; Cx ; Cy M)
				if len(parseBuf) > 3 && parseBuf[1] == '[' {
					if terminatorIdx, cmd, err := scanCSI(parseBuf); err == nil && cmd == 'M' && terminatorIdx > 2 {
						if event, consumed, err := ParseMouseURXVT(parseBuf); err == nil {
							r.buf = r.buf[consumed+altOffset:]
							if altOffset > 0 {
								event.ControlKeyState |= LeftAltPressed
							}
							r.recordLatency(time.Since(r.lastReceivedAt))
							return event, nil
						}
					} else if err == ErrIncomplete {
						goto waitForMore
					}
				}

				// 6. SS3 Sequences (ESC O ... or broken VTE ESC [ O ...)
				if (len(parseBuf) > 1 && parseBuf[1] == 'O') || (len(parseBuf) > 2 && parseBuf[1] == '[' && parseBuf[2] == 'O') {
					if event, consumed, err := ParseLegacySS3(parseBuf); err == nil {
						r.buf = r.buf[consumed+altOffset:]
						if altOffset > 0 {
							event.ControlKeyState |= LeftAltPressed
						}
						r.recordLatency(time.Since(r.lastReceivedAt))
						return event, nil
					} else if err == ErrIncomplete {
						goto waitForMore
					}
				}

				// 6.5. DA (Device Attributes) - consume \x1b[?...c
				if len(parseBuf) > 2 && parseBuf[1] == '[' && parseBuf[2] == '?' {
					if terminatorIdx, cmd, err := scanCSI(parseBuf); err == nil && cmd == 'c' {
						r.buf = r.buf[terminatorIdx+1+altOffset:]
						continue
					} else if err == ErrIncomplete {
						goto waitForMore
					}
				}

				// 7. Other CSI Sequences (Legacy, Win32, Kitty)
				if len(parseBuf) > 1 && parseBuf[1] == '[' {
					if terminatorIdx, cmd, err := scanCSI(parseBuf); err == nil {
						var event *InputEvent
						var consumed int
						var pErr error

						event, consumed, pErr = ParseLegacyCSI(parseBuf)

						if pErr == nil && event != nil && r.far2lExtensionsEnabled {
							r.buf = r.buf[consumed+altOffset:]
							continue
						}

						if pErr == ErrInvalidSequence || event == nil {
							// Modern sequences (Win32/Kitty) are always allowed, even if
							// Far2l is on, because they don't collide and are used for
							// high-precision input in nested sessions.
							if cmd == '_' {
								event, consumed, pErr = ParseWin32InputEvent(parseBuf)
							} else {
								var events []*InputEvent
								events, consumed, pErr = ParseKittyEvents(parseBuf)
								if pErr == nil && len(events) > 0 {
									event = events[0]
									r.pendingTextEvents = events[1:]
								}
							}
						}

						if pErr == nil && event != nil {
							r.buf = r.buf[consumed+altOffset:]
							if altOffset > 0 {
								event.ControlKeyState |= LeftAltPressed
							}
							r.recordLatency(time.Since(r.lastReceivedAt))
							Log("Reader: Returning CSI event: %s", event.String())
							return event, nil
						} else if pErr == ErrInvalidSequence {
							r.buf = r.buf[terminatorIdx+1+altOffset:]
							continue
						}
					} else if err == ErrIncomplete {
						goto waitForMore
					}
				}

				// 8. Double ESC
				if len(r.buf) >= 2 && r.buf[1] == 0x1B && altOffset == 0 {
					r.buf = r.buf[2:]
					r.recordLatency(time.Since(r.lastReceivedAt))
					return &InputEvent{Type: KeyEventType, VirtualKeyCode: VK_ESCAPE, KeyDown: true, InputSource: "legacy_esc"}, nil
				}

				// 9. Legacy Alt (ESC + Char)
				if len(r.buf) >= 2 && utf8.FullRune(r.buf[1:]) {
					r.buf = r.buf[1:]
					character, size := utf8.DecodeRune(r.buf)
					r.buf = r.buf[size:]

					if legacyEvt := translateLegacyByte(character); legacyEvt != nil {
						legacyEvt.ControlKeyState |= LeftAltPressed
						legacyEvt.InputSource = "legacy_alt_ctrl"
						r.recordLatency(time.Since(r.lastReceivedAt))
						return legacyEvt, nil
					}

					r.recordLatency(time.Since(r.lastReceivedAt))
					return &InputEvent{
						Type:            KeyEventType,
						Char:            character,
						ControlKeyState: LeftAltPressed,
						KeyDown:         true,
						IsLegacy:        true,
						InputSource:     "legacy_alt",
					}, nil
				}

			waitForMore:
				waitTimeout := 100 * time.Millisecond
				if !deadline.IsZero() {
					if rem := time.Until(deadline); rem < waitTimeout {
						waitTimeout = rem
						if waitTimeout <= 0 {
							r.buf = r.buf[1:]
							r.recordLatency(time.Since(r.lastReceivedAt))
							return &InputEvent{Type: KeyEventType, VirtualKeyCode: VK_ESCAPE, KeyDown: true}, nil
						}
					}
				}
				n, err := r.readBytes(tmp, waitTimeout)
				if n > 0 {
					r.buf = append(r.buf, tmp[:n]...)
					continue
				}
				if err != nil {
					if len(r.buf) == 0 {
						return nil, err
					}
				}
				// Nothing followed: an ESC [ I / ESC [ O that stayed three
				// bytes long really was focus tracking, not a truncated
				// function key.
				if len(parseBuf) == 3 && parseBuf[1] == '[' && (parseBuf[2] == 'I' || parseBuf[2] == 'O') {
					event := &InputEvent{Type: FocusEventType, SetFocus: parseBuf[2] == 'I'}
					r.buf = r.buf[3+altOffset:]
					r.recordLatency(time.Since(r.lastReceivedAt))
					Log("Reader: Parsed Focus %v (after wait).", event.SetFocus)
					return event, nil
				}
				r.buf = r.buf[1:]
				r.recordLatency(time.Since(r.lastReceivedAt))
				return &InputEvent{Type: KeyEventType, VirtualKeyCode: VK_ESCAPE, KeyDown: true}, nil
			}

			// Handle standalone BACK (0x7F)
			if r.buf[0] == 0x7F {
				r.buf = r.buf[1:]
				r.recordLatency(time.Since(r.lastReceivedAt))
				return &InputEvent{Type: KeyEventType, VirtualKeyCode: VK_BACK, KeyDown: true, IsLegacy: true, InputSource: "legacy_char", RepeatCount: 1}, nil
			}

			// Handle regular UTF-8 characters
			if utf8.FullRune(r.buf) {
				character, size := utf8.DecodeRune(r.buf)
				consumed := size

				// Far2l workaround: some versions of far2l's terminal emulator
				// send an extra space after the '=' character.
				if character == '=' && len(r.buf) > size && r.buf[size] == ' ' {
					consumed++
					Log("Reader: Applied Far2l '=' workaround, consumed extra space.")
				}
				r.buf = r.buf[consumed:]
				if event := translateLegacyByte(character); event != nil {
					event.InputSource = "legacy_ctrl"
					r.recordLatency(time.Since(r.lastReceivedAt))
					return event, nil
				}
				r.recordLatency(time.Since(r.lastReceivedAt))
				return &InputEvent{Type: KeyEventType, Char: character, KeyDown: true, IsLegacy: true, InputSource: "legacy_char", RepeatCount: 1}, nil
			}
		}

		readTimeout := time.Duration(0)
		if !deadline.IsZero() {
			readTimeout = time.Until(deadline)
			if readTimeout <= 0 {
				return nil, nil
			}
		}
		n, err := r.readBytes(tmp, readTimeout)
		if err != nil {
			if len(r.buf) == 0 {
				return nil, err
			}
			continue
		}
		if n > 0 {
			r.buf = append(r.buf, tmp[:n]...)
		}
	}
}

func (r *Reader) recordLatency(latency time.Duration) {
	if !r.MetricsEnabled {
		return
	}
	r.mu.Lock()
	r.lastLatency = latency
	r.totalLatency += latency
	r.eventCount++
	r.mu.Unlock()
}

// Metrics returns the last event latency, average latency, and total event count.
func (r *Reader) Metrics() (last time.Duration, avg time.Duration, count int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.eventCount > 0 {
		avg = r.totalLatency / time.Duration(r.eventCount)
	}
	return r.lastLatency, avg, r.eventCount
}

func translateLegacyByte(r rune) *InputEvent {
	evt := &InputEvent{Type: KeyEventType, KeyDown: true, IsLegacy: true, RepeatCount: 1}
	switch r {
	case 0x00:
		evt.VirtualKeyCode = VK_SPACE
		evt.Char = ' '
		evt.ControlKeyState = LeftCtrlPressed
		return evt
	case 0x08:
		evt.VirtualKeyCode = VK_BACK
		return evt
	case 0x09:
		evt.VirtualKeyCode = VK_TAB
		evt.Char = '\t'
		return evt
	case 0x0D:
		evt.VirtualKeyCode = VK_RETURN
		evt.Char = '\r'
		return evt
	case 0x1B:
		evt.VirtualKeyCode = VK_ESCAPE
		return evt
	case 0x1C:
		evt.VirtualKeyCode = VK_OEM_5
		evt.ControlKeyState = LeftCtrlPressed
		return evt
	case 0x1D:
		evt.VirtualKeyCode = VK_OEM_6
		evt.ControlKeyState = LeftCtrlPressed
		return evt
	case 0x1E:
		evt.VirtualKeyCode = VK_6
		evt.ControlKeyState = LeftCtrlPressed
		return evt
	case 0x1F:
		evt.VirtualKeyCode = VK_OEM_MINUS
		evt.ControlKeyState = LeftCtrlPressed
		return evt
	}
	if r >= 1 && r <= 26 {
		evt.VirtualKeyCode = uint16(VK_A + (r - 1))
		evt.ControlKeyState = LeftCtrlPressed
		return evt
	}
	return nil
}
