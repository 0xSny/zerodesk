package input

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	procSendInput        = user32.NewProc("SendInput")
	procOpenInputDesktop = user32.NewProc("OpenInputDesktop")
	procSetThreadDesktop = user32.NewProc("SetThreadDesktop")
	procCloseDesktop     = user32.NewProc("CloseDesktop")
)

const (
	inputMouse    uint32 = 0
	inputKeyboard uint32 = 1

	mouseEventMove       uint32 = 0x0001
	mouseEventLeftDown   uint32 = 0x0002
	mouseEventLeftUp     uint32 = 0x0004
	mouseEventRightDown  uint32 = 0x0008
	mouseEventRightUp    uint32 = 0x0010
	mouseEventMiddleDown uint32 = 0x0020
	mouseEventMiddleUp   uint32 = 0x0040
	mouseEventWheel      uint32 = 0x0800
	mouseEventHWheel     uint32 = 0x1000

	keyEventExtendedKey uint32 = 0x0001
	keyEventKeyUp       uint32 = 0x0002
	keyEventUnicode     uint32 = 0x0004
)

// win32Input matches Win32 x64 INPUT layout (40 bytes).
type win32Input struct {
	inputType uint32
	_pad0     uint32
	data      [32]byte
}

type mouseInput struct {
	dx          int32
	dy          int32
	mouseData   uint32
	dwFlags     uint32
	time        uint32
	_pad        uint32
	dwExtraInfo uintptr
}

type keybdInput struct {
	wVk         uint16
	wScan       uint16
	dwFlags     uint32
	time        uint32
	_pad        uint32
	dwExtraInfo uintptr
}

type inputJob struct {
	inputs []win32Input
	done   chan error
}

var (
	inputOnce  sync.Once
	inputJobCh chan inputJob
)

func ensureInputWorker() {
	inputOnce.Do(func() {
		inputJobCh = make(chan inputJob, 64)
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			attachDesktop := func() {
				const maximumAllowed = 0x02000000
				hDesk, _, _ := procOpenInputDesktop.Call(0, 0, maximumAllowed)
				if hDesk != 0 {
					_, _, _ = procSetThreadDesktop.Call(hDesk)
					_, _, _ = procCloseDesktop.Call(hDesk)
				}
			}
			attachDesktop()
			for job := range inputJobCh {
				if len(job.inputs) == 0 {
					job.done <- nil
					continue
				}
				sent, _, err := procSendInput.Call(
					uintptr(len(job.inputs)),
					uintptr(unsafe.Pointer(&job.inputs[0])),
					uintptr(unsafe.Sizeof(win32Input{})),
				)
				if sent == 0 {
					// Desktop may have switched (e.g. UAC); re-attach and retry once
					attachDesktop()
					sent, _, err = procSendInput.Call(
						uintptr(len(job.inputs)),
						uintptr(unsafe.Pointer(&job.inputs[0])),
						uintptr(unsafe.Sizeof(win32Input{})),
					)
				}
				if sent == 0 {
					job.done <- fmt.Errorf("SendInput failed: %w", err)
				} else {
					job.done <- nil
				}
			}
		}()
	})
}

func sendInputs(inputs []win32Input) error {
	if len(inputs) == 0 {
		return nil
	}
	ensureInputWorker()
	done := make(chan error, 1)
	inputJobCh <- inputJob{inputs: inputs, done: done}
	return <-done
}

func makeMouseInput(dx, dy int32, mouseData, flags uint32) win32Input {
	var in win32Input
	in.inputType = inputMouse
	mi := (*mouseInput)(unsafe.Pointer(&in.data[0]))
	mi.dx = dx
	mi.dy = dy
	mi.mouseData = mouseData
	mi.dwFlags = flags
	return in
}

func makeKeyInput(vk uint16, scan uint16, flags uint32) win32Input {
	var in win32Input
	in.inputType = inputKeyboard
	ki := (*keybdInput)(unsafe.Pointer(&in.data[0]))
	ki.wVk = vk
	ki.wScan = scan
	ki.dwFlags = flags
	return in
}

// MoveMouseRelative dispatches relative cursor movement (touchpad mode).
func MoveMouseRelative(dx, dy int32) error {
	if dx == 0 && dy == 0 {
		return nil
	}
	return sendInputs([]win32Input{makeMouseInput(dx, dy, 0, mouseEventMove)})
}

// MouseButton executes down/up/click/dblclick for left/right/middle buttons.
func MouseButton(button, action string) error {
	var downFlag, upFlag uint32
	switch strings.ToLower(button) {
	case "left", "":
		downFlag, upFlag = mouseEventLeftDown, mouseEventLeftUp
	case "right":
		downFlag, upFlag = mouseEventRightDown, mouseEventRightUp
	case "middle":
		downFlag, upFlag = mouseEventMiddleDown, mouseEventMiddleUp
	default:
		return fmt.Errorf("unknown mouse button: %s", button)
	}

	switch strings.ToLower(action) {
	case "down":
		return sendInputs([]win32Input{makeMouseInput(0, 0, 0, downFlag)})
	case "up":
		return sendInputs([]win32Input{makeMouseInput(0, 0, 0, upFlag)})
	case "click":
		return sendInputs([]win32Input{
			makeMouseInput(0, 0, 0, downFlag),
			makeMouseInput(0, 0, 0, upFlag),
		})
	case "dblclick":
		return sendInputs([]win32Input{
			makeMouseInput(0, 0, 0, downFlag),
			makeMouseInput(0, 0, 0, upFlag),
			makeMouseInput(0, 0, 0, downFlag),
			makeMouseInput(0, 0, 0, upFlag),
		})
	default:
		return fmt.Errorf("unknown mouse action: %s", action)
	}
}

// Scroll dispatches smooth mouse wheel events (120 units per standard notch).
func Scroll(dx, dy int32) error {
	var inputs []win32Input
	if dy != 0 {
		inputs = append(inputs, makeMouseInput(0, 0, uint32(dy), mouseEventWheel))
	}
	if dx != 0 {
		inputs = append(inputs, makeMouseInput(0, 0, uint32(dx), mouseEventHWheel))
	}
	return sendInputs(inputs)
}

// InjectUnicodeText injects arbitrary UTF-8 text (including Chinese/Emoji) via KEYEVENTF_UNICODE,
// bypassing local IME state conflicts.
func InjectUnicodeText(text string) error {
	if text == "" {
		return nil
	}
	units := utf16.Encode([]rune(text))
	inputs := make([]win32Input, 0, len(units)*2)
	for _, u := range units {
		inputs = append(inputs,
			makeKeyInput(0, u, keyEventUnicode),
			makeKeyInput(0, u, keyEventUnicode|keyEventKeyUp),
		)
	}
	return sendInputs(inputs)
}

// SendVirtualKey presses and releases a single virtual key code.
func SendVirtualKey(vk uint16, extended bool) error {
	var flags uint32
	if extended {
		flags = keyEventExtendedKey
	}
	return sendInputs([]win32Input{
		makeKeyInput(vk, 0, flags),
		makeKeyInput(vk, 0, flags|keyEventKeyUp),
	})
}

var shortcutMap = map[string][]uint16{
	"esc":       {0x1B},
	"escape":    {0x1B},
	"tab":       {0x09},
	"enter":     {0x0D},
	"backspace": {0x08},
	"space":     {0x20},
	"up":        {0x26},
	"down":      {0x28},
	"left":      {0x25},
	"right":     {0x27},
	"home":      {0x24},
	"end":       {0x23},
	"pgup":      {0x21},
	"pgdn":      {0x22},
	"delete":    {0x2E},
	"ctrl+c":    {0x11, 'C'},
	"ctrl+v":    {0x11, 'V'},
	"ctrl+z":    {0x11, 'Z'},
	"ctrl+a":    {0x11, 'A'},
	"ctrl+w":    {0x11, 'W'},
	"ctrl+x":    {0x11, 'X'},
	"ctrl+s":    {0x11, 'S'},
	"alt+tab":   {0x12, 0x09},
	"alt+f4":    {0x12, 0x73},
	"win+d":     {0x5B, 'D'},
	"win":       {0x5B},
}

func isExtendedVK(vk uint16) bool {
	switch vk {
	case 0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28, 0x2D, 0x2E, 0x5B, 0x5C:
		return true
	default:
		return false
	}
}

// SendShortcut sends a named shortcut or key combo (e.g. "Ctrl+C", "Alt+Tab", "Win+D", "Esc").
func SendShortcut(name string) error {
	key := strings.ToLower(strings.TrimSpace(name))
	vks, ok := shortcutMap[key]
	if !ok {
		return fmt.Errorf("unsupported shortcut: %s", name)
	}
	inputs := make([]win32Input, 0, len(vks)*2)
	for _, vk := range vks {
		var f uint32
		if isExtendedVK(vk) {
			f = keyEventExtendedKey
		}
		inputs = append(inputs, makeKeyInput(vk, 0, f))
	}
	for i := len(vks) - 1; i >= 0; i-- {
		vk := vks[i]
		f := keyEventKeyUp
		if isExtendedVK(vk) {
			f |= keyEventExtendedKey
		}
		inputs = append(inputs, makeKeyInput(vk, 0, f))
	}
	return sendInputs(inputs)
}
