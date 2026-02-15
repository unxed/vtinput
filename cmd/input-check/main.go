package main

import (
	"fmt"
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

	// Буфер для чтения данных из stdin
	buf := make([]byte, 128)
	// Аккумулятор для сбора неполных последовательностей
	var accumulator []byte

	for {
		// 2. Читаем сырые байты
		n, err := os.Stdin.Read(buf)
		if err != nil {
			break
		}

		// Добавляем прочитанное в хвост аккумулятора
		accumulator = append(accumulator, buf[:n]...)

		// 3. Пытаемся разобрать то, что накопилось
		for len(accumulator) > 0 {
			// Вызываем наш парсер
			event, consumed, err := vtinput.ParseWin32InputEvent(accumulator)

			if err == nil {
				// Успех! Мы распознали событие Win32
				fmt.Printf("Event: %s\r\n", event)

				// Удаляем разобранный кусок из аккумулятора
				accumulator = accumulator[consumed:]

				// Проверка на выход
				if isExitEvent(event) {
					fmt.Print("Exiting...\r\n")
					return
				}
				continue
			}

			// Если ошибка "Incomplete" — значит последовательность не дошла целиком.
			// Прерываем цикл разбора и идем читать из Stdin дальше.
			if err == vtinput.ErrIncomplete {
				break
			}

			// Если ошибка "Invalid" — значит это не Win32 последовательность.
			// Это может быть мусор или обычный ввод, если терминал что-то перепутал.
			// Просто выведем байт как есть и пропустим его.
			if err == vtinput.ErrInvalidSequence {
				b := accumulator[0]

				// HACK: Handle legacy backspace (0x7F) from some terminals
				if b == 0x7F {
					// Manually create the expected KeyDown event for VK_BACK
					backspaceEvent := &vtinput.InputEvent{
						Type:           vtinput.KeyEventType,
						VirtualKeyCode: vtinput.VK_BACK,
						KeyDown:        true,
						// Other fields are zero, which is fine for this case
					}
					fmt.Printf("Event (Normalized): %s\r\n", backspaceEvent)
				} else {
					// Print other raw bytes as before
					if b >= 32 && b <= 126 {
						fmt.Printf("Raw Char: '%c'\r\n", b)
					} else {
						fmt.Printf("Raw Byte: 0x%02X\r\n", b)
					}
				}

				// Сдвигаем аккумулятор на 1 байт вперед
				accumulator = accumulator[1:]
				continue
			}
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