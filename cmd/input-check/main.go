package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/unxed/vtinput"
)

// activeKey holds state for a pressed key
type activeKey struct {
	pressedAt time.Time
	isLegacy  bool
	isDown    bool
}

var (
	mu          sync.Mutex
	pressedKeys = make(map[uint16]activeKey)
	logLines    []string
	logLimit    = 10
	currentMods uint32
)

const (
	_f1   = 0xFF00 + 11
	_f5   = 0xFF00 + 35
	_f9   = 0xFF00 + 59
	_nav  = 0xFF00 + 86
	_up   = 0xFF00 + 95
	_num  = 0xFF00 + 113
	_nDot = 0xFF00 + 127
)

// Keyboard layout rows for visualization (0xFF00+x pads to absolute column x)
var keyRows = [][]uint16{
	{vtinput.VK_ESCAPE, _f1, vtinput.VK_F1, vtinput.VK_F2, vtinput.VK_F3, vtinput.VK_F4, _f5, vtinput.VK_F5, vtinput.VK_F6, vtinput.VK_F7, vtinput.VK_F8, _f9, vtinput.VK_F9, vtinput.VK_F10, vtinput.VK_F11, vtinput.VK_F12, _nav, vtinput.VK_SNAPSHOT, vtinput.VK_SCROLL, vtinput.VK_PAUSE},
	{vtinput.VK_OEM_3, vtinput.VK_1, vtinput.VK_2, vtinput.VK_3, vtinput.VK_4, vtinput.VK_5, vtinput.VK_6, vtinput.VK_7, vtinput.VK_8, vtinput.VK_9, vtinput.VK_0, vtinput.VK_OEM_MINUS, vtinput.VK_OEM_PLUS, vtinput.VK_BACK, _nav, vtinput.VK_INSERT, vtinput.VK_HOME, vtinput.VK_PRIOR, _num, vtinput.VK_NUMLOCK, vtinput.VK_DIVIDE, vtinput.VK_MULTIPLY, vtinput.VK_SUBTRACT},
	{vtinput.VK_TAB, vtinput.VK_Q, vtinput.VK_W, vtinput.VK_E, vtinput.VK_R, vtinput.VK_T, vtinput.VK_Y, vtinput.VK_U, vtinput.VK_I, vtinput.VK_O, vtinput.VK_P, vtinput.VK_OEM_4, vtinput.VK_OEM_6, vtinput.VK_OEM_5, _nav, vtinput.VK_DELETE, vtinput.VK_END, vtinput.VK_NEXT, _num, vtinput.VK_NUMPAD7, vtinput.VK_NUMPAD8, vtinput.VK_NUMPAD9, vtinput.VK_ADD},
	{vtinput.VK_CAPITAL, vtinput.VK_A, vtinput.VK_S, vtinput.VK_D, vtinput.VK_F, vtinput.VK_G, vtinput.VK_H, vtinput.VK_J, vtinput.VK_K, vtinput.VK_L, vtinput.VK_OEM_1, vtinput.VK_OEM_7, vtinput.VK_RETURN, _num, vtinput.VK_NUMPAD4, vtinput.VK_NUMPAD5, vtinput.VK_NUMPAD6},
	{vtinput.VK_LSHIFT, vtinput.VK_OEM_102, vtinput.VK_Z, vtinput.VK_X, vtinput.VK_C, vtinput.VK_V, vtinput.VK_B, vtinput.VK_N, vtinput.VK_M, vtinput.VK_OEM_COMMA, vtinput.VK_OEM_PERIOD, vtinput.VK_OEM_2, vtinput.VK_RSHIFT, _up, vtinput.VK_UP, _num, vtinput.VK_NUMPAD1, vtinput.VK_NUMPAD2, vtinput.VK_NUMPAD3, vtinput.VK_RETURN},
	{vtinput.VK_LCONTROL, vtinput.VK_LWIN, vtinput.VK_LMENU, vtinput.VK_SPACE, vtinput.VK_RMENU, vtinput.VK_RWIN, vtinput.VK_APPS, vtinput.VK_RCONTROL, _nav, vtinput.VK_LEFT, vtinput.VK_DOWN, vtinput.VK_RIGHT, _num, vtinput.VK_NUMPAD0, _nDot, vtinput.VK_DECIMAL},
}

func main() {
	useWin32 := flag.Bool("win32", true, "Enable Win32 Input Mode")
	useKitty := flag.Bool("kitty", true, "Enable Kitty Keyboard Protocol")
	useMouse := flag.Bool("mouse", true, "Enable Mouse Support")
	useExt := flag.Bool("ext", true, "Enable Focus and Bracketed Paste")
	flag.Parse()

	var mask vtinput.Protocol
	if *useWin32 { mask |= vtinput.Win32InputMode }
	if *useKitty { mask |= vtinput.KittyKeyboard }
	if *useMouse { mask |= vtinput.MouseSupport }
	if *useExt { mask |= vtinput.FocusAndPaste }

	restore, err := vtinput.EnableProtocols(mask)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer restore()

	// Clear screen and hide cursor
	fmt.Print("\033[2J\033[?25l")
	defer fmt.Print("\033[?25h") // Show cursor on exit

	reader := vtinput.NewReader(os.Stdin)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	// Initial draw
	drawUI()

	// Event channel to bridge reader and select loop
	eventChan := make(chan *vtinput.InputEvent)
	go func() {
		for {
			e, err := reader.ReadEvent()
			if err != nil {
				if err != io.EOF {
					// In a real app we might handle error, here just exit loop
				}
				return
			}
			eventChan <- e
		}
	}()

	for {
		select {
		case e := <-eventChan:
			handleEvent(e)
			if isExitEvent(e) {
				return
			}
			drawUI()

			case <-ticker.C:
				mu.Lock()
				changed := false
				now := time.Now()
				for k, v := range pressedKeys {
					// Remove legacy keys after 150ms timeout
					if v.isLegacy && now.Sub(v.pressedAt) > 150*time.Millisecond {
						delete(pressedKeys, k)
						changed = true
					// Remove modern keys only if released AND 100ms passed (to make fast typing visible)
					} else if !v.isLegacy && !v.isDown && now.Sub(v.pressedAt) > 100*time.Millisecond {
						delete(pressedKeys, k)
						changed = true
					}
				}
				mu.Unlock()
				if changed {
					drawUI()
				}
		}
	}
}

func handleEvent(e *vtinput.InputEvent) {
	mu.Lock()
	defer mu.Unlock()

	// Terminal emulators often send stale modifier states for lock keys on key release.
	// We predict the new lock state on key down (inverting the pre-press state),
	// and ignore the event's lock bits for these keys to prevent visual sticking.
	if e.Type == vtinput.KeyEventType && (e.VirtualKeyCode == vtinput.VK_CAPITAL || e.VirtualKeyCode == vtinput.VK_NUMLOCK || e.VirtualKeyCode == vtinput.VK_SCROLL) {
		if e.KeyDown {
			if e.VirtualKeyCode == vtinput.VK_CAPITAL {
				if (e.ControlKeyState & vtinput.CapsLockOn) == 0 {
					currentMods |= vtinput.CapsLockOn
				} else {
					currentMods &= ^uint32(vtinput.CapsLockOn)
				}
			} else if e.VirtualKeyCode == vtinput.VK_NUMLOCK {
				if (e.ControlKeyState & vtinput.NumLockOn) == 0 {
					currentMods |= vtinput.NumLockOn
				} else {
					currentMods &= ^uint32(vtinput.NumLockOn)
				}
			} else if e.VirtualKeyCode == vtinput.VK_SCROLL {
				if (e.ControlKeyState & vtinput.ScrollLockOn) == 0 {
					currentMods |= vtinput.ScrollLockOn
				} else {
					currentMods &= ^uint32(vtinput.ScrollLockOn)
				}
			}
		}
		lockMask := uint32(vtinput.CapsLockOn | vtinput.NumLockOn | vtinput.ScrollLockOn)
		currentMods = (currentMods & lockMask) | (e.ControlKeyState & ^lockMask)
	} else {
		currentMods = e.ControlKeyState
	}

	// Log message
	msg := fmt.Sprintf("Event: %s", e)
	logLines = append(logLines, msg)
	if len(logLines) > logLimit {
		logLines = logLines[1:]
	}

	if e.Type == vtinput.KeyEventType {
		vk := e.VirtualKeyCode
		// Disambiguate Shift keys based on ScanCode (common in Win32/Kitty)
		if vk == vtinput.VK_SHIFT {
			if e.VirtualScanCode == vtinput.ScanCodeLeftShift {
				vk = vtinput.VK_LSHIFT
			} else if e.VirtualScanCode == vtinput.ScanCodeRightShift {
				vk = vtinput.VK_RSHIFT
			}
		}

		if e.KeyDown {
			pressedKeys[vk] = activeKey{
				pressedAt: time.Now(),
				isLegacy:  e.IsLegacy,
				isDown:    true,
			}
		} else {
			if ak, ok := pressedKeys[vk]; ok {
				ak.isDown = false
				pressedKeys[vk] = ak
			}
		}
	}
}

func drawUI() {
	mu.Lock()
	defer mu.Unlock()

	// Move to top-left
	fmt.Print("\033[H")

	fmt.Print("--- vtinput input visualizer (press Ctrl+C/Esc to exit) ---\r\n\r\n")

	// Determine if we have specific shift keys pressed to avoid generic modifier fallback
	shiftInMap := false
	if _, ok := pressedKeys[vtinput.VK_LSHIFT]; ok { shiftInMap = true }
	if _, ok := pressedKeys[vtinput.VK_RSHIFT]; ok { shiftInMap = true }

	for _, row := range keyRows {
		col := 0
		for _, vk := range row {
			if vk >= 0xFF00 {
				targetCol := int(vk - 0xFF00)
				for col < targetCol {
					fmt.Print(" ")
					col++
				}
				continue
			}

			name, ok := vkNames[vk]
			if !ok {
				name = fmt.Sprintf("0x%X", vk)
			}

			// Check if pressed directly
			isPressed := false
			if _, pressed := pressedKeys[vk]; pressed {
				isPressed = true
			}

			// Check modifiers from global state
			if vk == vtinput.VK_LCONTROL && (currentMods&vtinput.LeftCtrlPressed) != 0 { isPressed = true }
			if vk == vtinput.VK_RCONTROL && (currentMods&vtinput.RightCtrlPressed) != 0 { isPressed = true }
			if vk == vtinput.VK_LMENU && (currentMods&vtinput.LeftAltPressed) != 0 { isPressed = true }
			if vk == vtinput.VK_RMENU && (currentMods&vtinput.RightAltPressed) != 0 { isPressed = true }

			// For Shift, only use generic modifier if no specific shift key is detected in pressedKeys.
			// This allows distinguishing LShift/RShift in modern protocols, while keeping support for legacy.
			if !shiftInMap {
				if vk == vtinput.VK_LSHIFT && (currentMods&vtinput.ShiftPressed) != 0 { isPressed = true }
				if vk == vtinput.VK_RSHIFT && (currentMods&vtinput.ShiftPressed) != 0 { isPressed = true }
			}

			if vk == vtinput.VK_CAPITAL && (currentMods&vtinput.CapsLockOn) != 0 { isPressed = true }
			if vk == vtinput.VK_NUMLOCK && (currentMods&vtinput.NumLockOn) != 0 { isPressed = true }

			if isPressed {
				// Green background for pressed keys
				fmt.Printf("\033[42;30m %s \033[0m ", name)
			} else {
				fmt.Printf("[%s] ", name)
			}
			col += len(name) + 3
		}
		fmt.Print("\r\n\r\n") // Double spacing for clarity
	}

	fmt.Print("---------------- Log ----------------\r\n")
	for _, line := range logLines {
		// Clear line before printing
		fmt.Printf("\033[K%s\r\n", line)
	}
	// Clear rest of screen below
	fmt.Print("\033[J")
}

func isExitEvent(e *vtinput.InputEvent) bool {
	if !e.KeyDown { return false }
	if e.VirtualKeyCode == vtinput.VK_ESCAPE { return true }
	if e.VirtualKeyCode == vtinput.VK_C && (e.ControlKeyState&vtinput.LeftCtrlPressed) != 0 { return true }
	return false
}