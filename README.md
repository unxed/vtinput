# vtinput

![](https://raw.githubusercontent.com/unxed/vtinput/refs/heads/main/screenshot.png)

**Advanced Terminal Input Parsing for Go**

`vtinput` is a standalone, cross-platform Go library designed to solve the historical mess of terminal keyboard input. It provides robust parsing for modern terminal input protocols, allowing TUI applications to handle complex key combinations, mouse events, and bracketed paste with absolute precision.

This library was initially developed for the [f4](https://github.com/unxed/f4) project (a Far Manager clone in Go) because standard Go terminal libraries (like `term` or `tcell`) often rely on legacy `terminfo` mappings only and cannot reliably distinguish between combinations like `Enter` and `Ctrl+Enter`, or `Tab` and `Shift+Tab`.

## Why vtinput?

For decades, terminals have communicated keystrokes using ambiguous ANSI escape sequences. A classic terminal emulator sends the exact same bytes for `Ctrl+I` as it does for `Tab`, and the same bytes for `Ctrl+M` as for `Enter`.

Modern terminal emulators have solved this by introducing advanced input protocols. `vtinput` speaks these protocols natively:

*   **kitty keyboard protocol:** Fully supported. Provides granular modifier states (Shift, Ctrl, Alt, Super, CapsLock, NumLock) and differentiates all keystrokes.
*   **win32 input mode:** Fully supported. Used by modern Windows Terminal and some Unix terminals to pass exact Windows Virtual Key Codes and states.
*   **SGR 1006 Mouse Protocol:** For high-coordinate and sub-cell mouse tracking, including scroll wheels.
*   **Bracketed Paste (2004) & Focus Tracking (1004):** Native support for detecting when the terminal gains/loses focus and for fast accepting large blocks of pasted text.
*   **Legacy CSI / SS3 Fallback:** If the terminal does not support modern protocols, `vtinput` gracefully falls back to parsing standard VT100/xterm sequences with high accuracy and a built-in timeout mechanism for the `ESC` key.

## Key Features

- **No CGO:** 100% pure Go.
- **Protocol Agnostic Interface:** Your application receives a unified `InputEvent` struct, regardless of whether the terminal used kitty, win32, or legacy protocols.
- **Accurate Modifiers:** Accurately reports `LeftCtrl` vs `RightCtrl`, `Alt`, `Shift`, and the state of lock keys.
- **Zero-Dependency Core:** Only depends on `golang.org/x/sys` and `golang.org/x/term` for putting the terminal into raw mode.

## Usage

`vtinput` handles both the "activation" of these protocols (sending the correct initialization sequences to the terminal) and the parsing of the incoming byte stream.

```go
package main

import (
	"fmt"
	"os"
	"github.com/unxed/vtinput"
)

func main() {
	// 1. Put terminal in Raw Mode and enable Kitty, Win32, Mouse, and Paste protocols
	restore, err := vtinput.Enable()
	if err != nil {
		panic(err)
	}
	defer restore() // Crucial: restores terminal to normal state on exit

	// 2. Create a reader wrapped around Stdin
	reader := vtinput.NewReader(os.Stdin)

	fmt.Print("\033[2J\033[H") // Clear screen
	fmt.Println("Listening for events. Press Ctrl+C to exit.")

	for {
		// 3. Block and wait for the next parsed event
		event, err := reader.ReadEvent()
		if err != nil {
			break
		}

		fmt.Printf("Received: %s\r\n", event.String())

		// Exit condition
		if event.Type == vtinput.KeyEventType && event.VirtualKeyCode == vtinput.VK_C && 
		   (event.ControlKeyState & vtinput.LeftCtrlPressed) != 0 {
			break
		}
	}
}
```

## Testing & Diagnostics

The repository includes a diagnostic tool. Run it to see exactly what `vtinput` sees when you type:

```bash
go run ./cmd/input-check
```
