package main

import (
	"fmt"
	"io"
	"os"

	"github.com/unxed/vtinput"
)

func main() {
	// 1. Включаем Win32 Input Mode и Raw Mode
	restore, err := vtinput.Enable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error enabling input mode: %v\n", err)
		os.Exit(1)
	}
	// Гарантируем, что при выходе терминал вернется в норму
	defer restore()

	fmt.Print("Win32 Input Mode Enabled (vtinput).\r\n")
	fmt.Print("Нажимай клавиши (Ctrl, Alt, стрелки, сочетания).\r\n")
	fmt.Print("Для выхода нажми Ctrl+C или Esc.\r\n")
	fmt.Print("------------------------------------------------\r\n")

	// 2. Создаем Reader для ввода
	reader := vtinput.NewReader(os.Stdin)

	for {
		// 3. Читаем следующее событие (блокирующий вызов)
		event, err := reader.ReadEvent()
		if err != nil {
			if err != io.EOF {
				fmt.Printf("Read error: %v\r\n", err)
			}
			break
		}

		// 4. Выводим распознанное событие
		fmt.Printf("Event: %s\r\n", event)

		// 5. Проверка на выход
		if isExitEvent(event) {
			fmt.Print("Exiting...\r\n")
			return
		}
	}
}

// isExitEvent проверяет, нажали ли Ctrl+C или Esc
func isExitEvent(e *vtinput.InputEvent) bool {
	// Нас интересует только нажатие (KeyDown = true)
	if !e.KeyDown {
		return false
	}

	// Esc
	if e.VirtualKeyCode == vtinput.VK_ESCAPE {
		return true
	}

	// Ctrl + C
	if e.VirtualKeyCode == vtinput.VK_C {
		// Проверяем битовую маску модификаторов
		ctrlPressed := (e.ControlKeyState & (vtinput.LeftCtrlPressed | vtinput.RightCtrlPressed)) != 0
		if ctrlPressed {
			return true
		}
	}

	return false
}