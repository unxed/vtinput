//go:build windows

package vtinput

import (
	"encoding/binary"
	"fmt"
	"os"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procWriteConsoleInputW = windows.NewLazySystemDLL("kernel32.dll").NewProc("WriteConsoleInputW")

func TestConsoleKeyEventAltGrText(t *testing.T) {
	var rec inputRecord
	rec.EventType = KeyEventType
	le := binary.LittleEndian
	le.PutUint32(rec.Event[0:4], 1)
	le.PutUint16(rec.Event[4:6], 3)
	le.PutUint16(rec.Event[6:8], VK_Q)
	le.PutUint16(rec.Event[8:10], 16)
	le.PutUint16(rec.Event[10:12], '@')
	le.PutUint32(rec.Event[12:16], uint32(LeftCtrlPressed|RightAltPressed|ShiftPressed))
	var decoder windowsTextDecoder
	got := decoder.decode(consoleKeyEvent(rec))
	if got == nil || !got.TextInput || got.Char != '@' || got.VirtualKeyCode != VK_Q ||
		got.VirtualScanCode != 16 || got.RepeatCount != 3 || got.InputSource != "ConPTY" ||
		got.ControlKeyState != LeftCtrlPressed|RightAltPressed|ShiftPressed {
		t.Fatalf("native record lost semantics or physical fields: %+v", got)
	}
	le.PutUint32(rec.Event[0:4], 0)
	got = decoder.decode(consoleKeyEvent(rec))
	if got.KeyDown || got.TextInput || got.Char != '@' {
		t.Fatalf("release classified as committed text: %+v", got)
	}
}

// escKeyRecord is the KEY_EVENT a physical ESC press produces: key-down or
// key-up, same layout ReadConsoleInputW parses in readConPTYEventTimeout.
func escKeyRecord(keyDown bool) inputRecord {
	var rec inputRecord
	rec.EventType = 0x0001 // KEY_EVENT
	if keyDown {
		rec.Event[0] = 1
	}
	le := binary.LittleEndian
	le.PutUint16(rec.Event[4:6], 1)      // repeat count
	le.PutUint16(rec.Event[6:8], 0x1B)   // VK_ESCAPE
	le.PutUint16(rec.Event[8:10], 0x01)  // scan code, arbitrary
	le.PutUint16(rec.Event[10:12], 0x1B) // UnicodeChar
	le.PutUint32(rec.Event[12:16], 0)    // no modifier flags
	return rec
}

func writeConsoleInput(handle windows.Handle, recs []inputRecord) error {
	var written uint32
	ret, _, err := procWriteConsoleInputW.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&recs[0])),
		uintptr(len(recs)),
		uintptr(unsafe.Pointer(&written)),
	)
	if ret == 0 {
		return err
	}
	return nil
}

// The console pump relies on CancelIoEx to break its blocking read on close.
// CloseHandle with a read still pending blocks, so if this fails the reader
// cannot be closed safely at all.
func TestCancelIoExUnblocksConsoleRead(t *testing.T) {
	f, err := os.OpenFile("CONIN$", os.O_RDONLY, 0)
	if err != nil {
		t.Skipf("no console attached: %v", err)
	}
	handle := windows.Handle(f.Fd())
	defer f.Close()

	var pending uint32
	if err := windows.GetNumberOfConsoleInputEvents(handle, &pending); err != nil {
		t.Skipf("console input is not readable: %v", err)
	}
	if pending > 0 {
		t.Skipf("console already has %d pending input events", pending)
	}

	type readResult struct {
		n   int
		err error
	}
	done := make(chan readResult, 1)
	go func() {
		b := make([]byte, 16)
		n, err := f.Read(b)
		done <- readResult{n, err}
	}()

	// Give the read time to reach the console host and block.
	time.Sleep(200 * time.Millisecond)

	if err := windows.CancelIoEx(handle, nil); err != nil {
		t.Fatalf("CancelIoEx: %v", err)
	}

	select {
	case got := <-done:
		t.Logf("read returned after cancel: n=%d err=%v", got.n, got.err)
	case <-time.After(2 * time.Second):
		t.Fatal("CancelIoEx did not unblock the pending console read")
	}
}

type transientReadInput struct {
	remainingErrors int
	reads           int
}

func (r *transientReadInput) Read(p []byte) (int, error) {
	r.reads++
	if r.remainingErrors > 0 {
		r.remainingErrors--
		return 0, windows.ERROR_PIPE_NOT_CONNECTED
	}
	p[0] = 'x'
	return 1, nil
}

func TestGetEventChanRetriesTransientReadError(t *testing.T) {
	in := &transientReadInput{remainingErrors: 2}
	r := NewReader(in, false)
	defer r.Close()

	select {
	case event := <-r.GetEventChan():
		if event == nil || event.Char != 'x' {
			t.Fatalf("event = %#v, want character x after retry", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event after transient read errors")
	}
	if in.reads < 3 {
		t.Fatalf("reader calls = %d, want at least 3", in.reads)
	}
}

func TestRetryableReadError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "pipe disconnected", err: windows.ERROR_PIPE_NOT_CONNECTED, want: true},
		{name: "wrapped pipe disconnected", err: fmt.Errorf("resize: %w", windows.ERROR_PIPE_NOT_CONNECTED), want: true},
		{name: "operation aborted", err: windows.ERROR_OPERATION_ABORTED, want: false},
		{name: "broken pipe", err: windows.ERROR_BROKEN_PIPE, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryableReadError(tt.err); got != tt.want {
				t.Fatalf("isRetryableReadError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// The console is the case the pump exists for: SetReadDeadline refuses
// console handles, and a read arranged outside the deadline blocks until
// the next byte instead of until the timeout — one ESC press then waited
// for the next press, and the two were merged into a single Double ESC
// event. The pump's channel keeps that from happening: with no input it
// stays silent and the timer wins.
// Guarded: there is no console to read from in CI.
func TestReadBytesTimeoutOnConsole(t *testing.T) {
	f, err := os.OpenFile("CONIN$", os.O_RDONLY, 0)
	if err != nil {
		t.Skipf("no console attached: %v", err)
	}
	defer f.Close()

	var pending uint32
	if err := windows.GetNumberOfConsoleInputEvents(windows.Handle(f.Fd()), &pending); err != nil {
		t.Skipf("console input is not readable: %v", err)
	}
	if pending > 0 {
		t.Skipf("console already has %d pending input events", pending)
	}

	oldMode := InputMode
	InputMode = "ansi" // the nested reader's mode; leaves the console alone
	defer func() { InputMode = oldMode }()

	r := NewReader(f, false)
	defer r.Close()
	if !isConsoleHandle(f) {
		t.Skip("CONIN$ did not yield a console handle")
	}

	start := time.Now()
	n, err := r.readBytes(make([]byte, 16), 100*time.Millisecond)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("readBytes: %v", err)
	}
	if n > 0 {
		t.Skipf("input arrived during the test (%d bytes)", n)
	}
	if elapsed < 90*time.Millisecond {
		t.Errorf("returned after %v, want to wait out the timeout", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Errorf("returned after %v, the timeout was not honoured", elapsed)
	}
}

// TestConsoleEscPressTranslation pins down how the console host translates
// one ESC press in the nested f4's mode (VT input on): the key-down record
// yields the ESC byte, and the key-up record of the same press yields
// nothing. A zero-byte record used to be the death of the lone-ESC timeout:
// it signalled the wait, the read started, and blocked past the deadline,
// so the press only completed on the next one. The pump must let the timer
// win instead.
func TestConsoleEscPressTranslation(t *testing.T) {
	f, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no console attached: %v", err)
	}
	defer f.Close()
	handle := windows.Handle(f.Fd())

	var oldMode uint32
	if err := windows.GetConsoleMode(handle, &oldMode); err != nil {
		t.Skipf("stdin is not a console: %v", err)
	}

	const (
		conInProcessedInput = 0x0001
		conInLineInput      = 0x0002
		conInEchoInput      = 0x0004
		conInVTInput        = 0x0200
	)
	if err := windows.SetConsoleMode(handle, (oldMode|conInVTInput)&^(conInProcessedInput|conInLineInput|conInEchoInput)); err != nil {
		t.Skipf("cannot switch to nested input mode: %v", err)
	}
	defer windows.SetConsoleMode(handle, oldMode)

	var pending uint32
	if err := windows.GetNumberOfConsoleInputEvents(handle, &pending); err != nil {
		t.Skipf("input not enumerable: %v", err)
	}
	if pending > 0 {
		t.Skipf("%d input events already pending", pending)
	}

	oldInput := InputMode
	InputMode = "ansi"
	defer func() { InputMode = oldInput }()

	r := NewReader(f, false)
	defer r.Close()

	readBudget := func(stage string, budget, watchdog time.Duration) int {
		t.Helper()
		type result struct {
			n   int
			err error
		}
		done := make(chan result, 1)
		buf := make([]byte, 32)
		go func() {
			n, err := r.readBytes(buf, budget)
			done <- result{n, err}
		}()
		start := time.Now()
		select {
		case got := <-done:
			t.Logf("%s: n=%d elapsed=%v err=%v bytes=%q", stage, got.n, time.Since(start), got.err, buf[:got.n])
			if got.err != nil {
				t.Fatalf("%s: readBytes: %v", stage, got.err)
			}
			return got.n
		case <-time.After(watchdog):
			t.Fatalf("%s: readBytes still blocked after %v although the budget was %v", stage, watchdog, budget)
			return -1
		}
	}

	// Key-down: must yield the ESC byte.
	if err := writeConsoleInput(handle, []inputRecord{escKeyRecord(true)}); err != nil {
		t.Fatalf("WriteConsoleInputW (down): %v", err)
	}
	if n := readBudget("key-down", 500*time.Millisecond, 3*time.Second); n == 0 {
		t.Fatal("the key-down record produced no bytes")
	}

	// Key-up of the same press: conhost translates it to no bytes, so the
	// deadline has to run out instead — the record must not swallow it.
	if err := writeConsoleInput(handle, []inputRecord{escKeyRecord(false)}); err != nil {
		t.Fatalf("WriteConsoleInputW (up): %v", err)
	}
	upStart := time.Now()
	if n := readBudget("key-up", 500*time.Millisecond, 3*time.Second); n != 0 {
		t.Errorf("the key-up record produced %d bytes, want none", n)
	}
	if upElapsed := time.Since(upStart); upElapsed < 450*time.Millisecond {
		t.Errorf("key-up returned after %v, want the full 500ms budget", upElapsed)
	}
}

// newDeadlinelessPipe returns an anonymous pipe whose read end refuses a
// deadline — the other input the pump exists for. Skips where the platform
// times pipe reads out on its own, which is the case on Unix.
func newDeadlinelessPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Skipf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { pr.Close(); pw.Close() })

	if err := pr.SetReadDeadline(time.Now()); err == nil {
		_ = pr.SetReadDeadline(time.Time{})
		t.Skipf("this pipe honours read deadlines (%v); nothing to test", err)
	}
	return pr, pw
}

// A pipe takes no SetReadDeadline, so a lone-ESC wait on it used to block
// until the next byte arrived and the two presses merged into one event.
// The pump's timer has to end the wait instead.
func TestReadBytesTimeoutOnAnonymousPipe(t *testing.T) {
	pr, _ := newDeadlinelessPipe(t)

	r := NewReader(pr, false)
	defer r.Close()

	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		n, err := r.readBytes(make([]byte, 4), 100*time.Millisecond)
		done <- result{n, err}
	}()

	select {
	case got := <-done:
		elapsed := time.Since(start)
		if got.err != nil {
			t.Fatalf("readBytes: %v", got.err)
		}
		if got.n != 0 {
			t.Errorf("readBytes returned %d bytes with no writer, want none", got.n)
		}
		if elapsed < 90*time.Millisecond {
			t.Errorf("readBytes returned after %v, want the full 100ms timeout", elapsed)
		}
		if elapsed > 5*time.Second {
			t.Errorf("readBytes took %v, want about the 100ms timeout", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readBytes blocked past its deadline; the pump did not take over")
	}

	// The pump owns a pending read now; closing must not wait for it.
	closed := make(chan struct{})
	go func() { r.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked behind the pump's pending read")
	}
}

// Two ESC presses 300ms apart must produce two events. Before the pump the
// first wait swallowed the pipe's missing deadline, never returned, and the
// second press was consumed as a Double ESC continuation of the first.
func TestLoneEscKeepsTwoPressesApartOnPipe(t *testing.T) {
	pr, pw := newDeadlinelessPipe(t)

	r := NewReader(pr, false)
	defer r.Close()

	go func() {
		_, _ = pw.Write([]byte{0x1B})
		time.Sleep(300 * time.Millisecond)
		_, _ = pw.Write([]byte{0x1B})
	}()

	readEvent := func(limit time.Duration) *InputEvent {
		t.Helper()
		type result struct {
			ev  *InputEvent
			err error
		}
		done := make(chan result, 1)
		go func() {
			ev, err := r.ReadEventTimeout(limit)
			done <- result{ev, err}
		}()
		select {
		case got := <-done:
			if got.err != nil {
				t.Fatalf("ReadEventTimeout: %v", got.err)
			}
			return got.ev
		case <-time.After(3 * time.Second):
			t.Fatalf("ReadEventTimeout blocked for 3s although its deadline was %v", limit)
			return nil
		}
	}

	first := readEvent(150 * time.Millisecond)
	if first == nil {
		t.Fatal("the first press produced no event")
	}
	if first.VirtualKeyCode != VK_ESCAPE {
		t.Fatalf("the first press produced %v, want VK_ESCAPE", first)
	}

	second := readEvent(2 * time.Second)
	if second == nil {
		t.Fatal("the second press produced no event")
	}
	if second.VirtualKeyCode != VK_ESCAPE {
		t.Fatalf("the second press produced %v, want VK_ESCAPE", second)
	}
}
