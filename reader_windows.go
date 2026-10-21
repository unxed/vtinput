//go:build windows

package vtinput

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

var procReadConsoleInputW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReadConsoleInputW")

// isRetryableReadError reports the console-host error observed while a
// shortcut-configured buffer is being resized. The handle remains usable
// after the host finishes applying the new buffer, so closing EventChan here
// would turn a transient resize race into a process exit.
func isRetryableReadError(err error) bool {
	return errors.Is(err, windows.ERROR_PIPE_NOT_CONNECTED)
}

type inputRecord struct {
	EventType EventType
	_         uint16
	Event     [16]byte
}

func (r *Reader) platformInit(in io.Reader) {
	useConPTY := true
	switch InputMode {
	case "ConPTY":
		useConPTY = true
	case "ansi":
		useConPTY = false
	}

	if useConPTY {
		if f, ok := in.(*os.File); ok {
			handle := windows.Handle(f.Fd())
			var mode uint32
			if err := windows.GetConsoleMode(handle, &mode); err == nil {
				r.conHandle = uintptr(handle)
				r.oldMode = mode
				r.useConPTY = true

				// We need to set some flags and, crucially, CLEAR others that interfere with raw input.
				// Set: WINDOW_INPUT (0x8), MOUSE_INPUT (0x10), EXTENDED_FLAGS (0x80)
				newMode := mode | 0x0008 | 0x0010 | 0x0080

				// Clear:
				// 0x0001: PROCESSED_INPUT (to get raw Ctrl+C)
				// 0x0002: LINE_INPUT (get keys immediately)
				// 0x0004: ECHO_INPUT
				// 0x0040: QUICK_EDIT_MODE (to allow mouse events instead of selection)
				// 0x0200: VIRTUAL_TERMINAL_INPUT (CRITICAL: if this is ON, ReadConsoleInputW gets no keys!)
				newMode &^= (0x0001 | 0x0002 | 0x0004 | 0x0040 | 0x0200)

				if err := windows.SetConsoleMode(handle, newMode); err != nil {
					Log("Reader: ConPTY SetConsoleMode failure: %v", err)
				}

				event, err := windows.CreateEvent(nil, 0, 0, nil)
				if err != nil {
					Log("Reader: CreateEvent failed: %v", err)
				} else {
					r.cancelEvent = uintptr(event)
				}
				return
			}
		}
	}

	r.useConPTY = false
}

func (r *Reader) readBytes(buf []byte, timeout time.Duration) (int, error) {
	if f, ok := r.in.(*os.File); ok && r.usesPump(f) {
		return r.readPumpedBytes(buf, timeout, windows.Handle(f.Fd()))
	}

	if timeout > 0 {
		// usesPump already put every file that cannot time a read out on the
		// pump, so what is left here either has no deadline method at all or
		// honours it.
		if d, ok := r.in.(interface{ SetReadDeadline(time.Time) error }); ok {
			_ = d.SetReadDeadline(time.Now().Add(timeout))
			defer d.SetReadDeadline(time.Time{})
		}
	}
	n, err := r.in.Read(buf)
	if n > 0 {
		if r.MetricsEnabled {
			r.lastReceivedAt = time.Now()
		}
	}
	return n, err
}

// usesPump reports whether this reader reads through the pump, deciding it
// once for the life of the reader. A console handle never takes a deadline,
// and a file that refuses one (an anonymous pipe) would block straight past
// it — both need the pump, where the deadline is a timer.
func (r *Reader) usesPump(f *os.File) bool {
	r.readPathOnce.Do(func() {
		if isConsoleHandle(f) {
			r.pumpedRead = true
			return
		}
		if err := f.SetReadDeadline(time.Now()); err != nil {
			r.pumpedRead = true
			return
		}
		_ = f.SetReadDeadline(time.Time{})
	})
	return r.pumpedRead
}

// isConsoleHandle reports whether f wraps a console handle.
func isConsoleHandle(f *os.File) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}

// readPumpedBytes waits for the next result of the pump goroutine and gives
// up at the deadline. The pump owns the only read on the handle; this side
// never blocks on it. A record that yields no bytes (a key-up, a resize)
// never reaches this channel, so it cannot push the deadline out — which it
// used to do: first by signalling a wait around the read, then by blocking
// the read itself, so one ESC press waited for the next press and the two
// merged into a single Double ESC event.
func (r *Reader) readPumpedBytes(buf []byte, timeout time.Duration, handle windows.Handle) (int, error) {
	if len(r.conpend) > 0 {
		n := copy(buf, r.conpend)
		r.conpend = r.conpend[n:]
		if len(r.conpend) == 0 {
			r.conpend = nil
		}
		return n, nil
	}

	select {
	case <-r.done:
		return 0, io.EOF
	default:
	}

	if r.conreads == nil {
		r.conreads = make(chan consoleRead, 1)
		// Closing the reader cancels the pump's blocking read through this
		// handle; without it the read would outlive the reader and pin the
		// input buffer open.
		if r.conHandle == 0 {
			r.conHandle = uintptr(handle)
		}
		go r.conPump()
	}

	var timer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timer = t.C
	}

	select {
	case <-r.done:
		return 0, io.EOF
	case <-timer:
		return 0, nil
	case res, ok := <-r.conreads:
		if !ok {
			if r.conErr != nil {
				return 0, r.conErr
			}
			return 0, io.EOF
		}
		if res.n > 0 {
			n := copy(buf, res.buf[:res.n])
			if n < res.n {
				// The caller's buffer was smaller than what one console
				// read returned; the rest waits in conpend for the next
				// call rather than being dropped.
				r.conpend = append([]byte(nil), res.buf[n:res.n]...)
			}
			if r.MetricsEnabled {
				r.lastReceivedAt = time.Now()
			}
			return n, res.err
		}
		return 0, res.err
	}
}

// conPump owns the blocking read on the input handle. Results go over a
// buffered channel so a read finishing between two readPumpedBytes calls
// waits there instead of writing into a caller's buffer; a terminal error
// closes the channel so later reads see it instead of waiting for a
// goroutine that is gone.
func (r *Reader) conPump() {
	for {
		b := make([]byte, 4096)
		n, err := r.in.Read(b)
		if n == 0 && err == nil {
			// Nothing to hand out; the next read blocks for real input.
			continue
		}
		select {
		case r.conreads <- consoleRead{n: n, err: err, buf: b}:
		case <-r.done:
			return
		}
		if err != nil {
			r.conErr = err
			close(r.conreads)
			return
		}
	}
}

func (r *Reader) readConPTYEventTimeout(timeout time.Duration) (*InputEvent, error) {
	if r.conHandle == 0 {
		return nil, io.ErrClosedPipe
	}

	var timeoutMs uint32 = windows.INFINITE
	if timeout > 0 {
		ms := uint32(timeout.Milliseconds())
		if ms > 0 {
			timeoutMs = ms
		}
	}

	handles := []windows.Handle{windows.Handle(r.conHandle)}
	if r.cancelEvent != 0 {
		handles = append(handles, windows.Handle(r.cancelEvent))
	}

	ret, err := windows.WaitForMultipleObjects(handles, false, timeoutMs)
	if err != nil {
		return nil, err
	}
	if ret == 0x00000102 { // WAIT_TIMEOUT
		return nil, nil
	}
	if len(handles) > 1 && ret == windows.WAIT_OBJECT_0+1 {
		return nil, io.EOF
	}

	var numRead uint32
	var rec inputRecord
	ret2, _, err := procReadConsoleInputW.Call(
		uintptr(r.conHandle),
		uintptr(unsafe.Pointer(&rec)),
		1,
		uintptr(unsafe.Pointer(&numRead)),
	)
	if ret2 == 0 {
		return nil, err
	}
	if numRead == 0 {
		return nil, nil
	}

	if r.MetricsEnabled {
		r.lastReceivedAt = time.Now()
	}

	switch rec.EventType {
	case 0x0001: // KEY_EVENT
		ev := consoleKeyEvent(rec)
		// f4 WINE.md §2k.3: under wineconsole, a Cyrillic keypress produced no
		// visible reaction at all -- the debug.log showed only a bare KeyUp
		// with Char=0, no KeyDown, no EDIT_TRACE. That leaves two candidates:
		// either ReadConsoleInputW itself never hands back a KEY_DOWN record
		// for the non-Latin layout under Wine, or one gets dropped somewhere
		// between here and f4's dispatcher. This is the earliest possible
		// point to look -- straight off the raw Win32 record, before any
		// f4 or vtui logic runs -- so logging every record here (not just
		// ones that pass some later filter) settles which side of that split
		// the record is lost on.
		Log("RAWKEY: down=%v vk=0x%X scan=0x%X char=%d(%q) mods=0x%X",
			ev.KeyDown, ev.VirtualKeyCode, ev.VirtualScanCode, ev.Char, ev.Char, uint32(ev.ControlKeyState))
		r.recordLatency(time.Since(r.lastReceivedAt))
		return ev, nil

	case 0x0002: // MOUSE_EVENT
		ev := &InputEvent{
			Type:            MouseEventType,
			MouseX:          int16(binary.LittleEndian.Uint16(rec.Event[0:2])),
			MouseY:          int16(binary.LittleEndian.Uint16(rec.Event[2:4])),
			ButtonState:     binary.LittleEndian.Uint32(rec.Event[4:8]),
			ControlKeyState: ControlKeyState(binary.LittleEndian.Uint32(rec.Event[8:12])),
			MouseEventFlags: binary.LittleEndian.Uint32(rec.Event[12:16]),
			InputSource:     "ConPTY",
			KeyDown:         true,
		}
		if (ev.MouseEventFlags&MouseWheeled > 0) || (ev.MouseEventFlags&MouseHWheeled > 0) {
			if int16(highWord(ev.ButtonState)) > 0 {
				ev.WheelDirection = 1
			} else {
				ev.WheelDirection = -1
			}
		}
		r.recordLatency(time.Since(r.lastReceivedAt))
		return ev, nil

	case 0x0004: // WINDOW_BUFFER_SIZE_EVENT
		r.recordLatency(time.Since(r.lastReceivedAt))
		return &InputEvent{Type: ResizeEventType, InputSource: "ConPTY"}, nil

	case 0x0010: // FOCUS_EVENT
		setFocus := binary.LittleEndian.Uint32(rec.Event[0:4]) > 0
		r.recordLatency(time.Since(r.lastReceivedAt))
		return &InputEvent{Type: FocusEventType, SetFocus: setFocus, InputSource: "ConPTY"}, nil

	default:
		return nil, nil
	}
}

// consoleKeyEvent preserves the raw native record before Reader combines UTF-16
// pairs and classifies the committed character.
func consoleKeyEvent(rec inputRecord) *InputEvent {
	return &InputEvent{
		Type:            KeyEventType,
		KeyDown:         binary.LittleEndian.Uint32(rec.Event[0:4]) > 0,
		RepeatCount:     binary.LittleEndian.Uint16(rec.Event[4:6]),
		VirtualKeyCode:  binary.LittleEndian.Uint16(rec.Event[6:8]),
		VirtualScanCode: binary.LittleEndian.Uint16(rec.Event[8:10]),
		Char:            rune(binary.LittleEndian.Uint16(rec.Event[10:12])),
		ControlKeyState: ControlKeyState(binary.LittleEndian.Uint32(rec.Event[12:16])),
		InputSource:     "ConPTY",
	}
}

func (r *Reader) platformClose() {
	if r.cancelEvent != 0 {
		windows.SetEvent(windows.Handle(r.cancelEvent))
	}
	if r.conHandle != 0 {
		windows.CancelIoEx(windows.Handle(r.conHandle), nil)
		// Hand back the mode MakeRaw saw before anything of ours touched the
		// console, not r.oldMode: that snapshot was taken at reader creation,
		// i.e. after term.MakeRaw had already set ENABLE_VIRTUAL_TERMINAL_INPUT,
		// and leaving it in place after exit makes the console host parse the
		// terminal's mouse reports as VT sequences and feed them to the shell
		// as key events -- mouse coordinates echoed at the prompt.
		if s := homeTerminalState(); s != nil {
			if err := term.Restore(int(r.conHandle), s); err != nil {
				Log("Reader: restoring terminal home state failed: %v", err)
			}
		} else if r.oldMode != 0 {
			windows.SetConsoleMode(windows.Handle(r.conHandle), r.oldMode)
		}
	}
}

func highWord(data uint32) uint16 {
	return uint16((data & 0xFFFF0000) >> 16)
}
