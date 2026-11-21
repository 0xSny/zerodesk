package system_ctrl

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"zerodesk-host/capture"
)

var (
	user32Blackout                = windows.NewLazySystemDLL("user32.dll")
	gdi32Blackout                 = windows.NewLazySystemDLL("gdi32.dll")
	kernel32Blackout              = windows.NewLazySystemDLL("kernel32.dll")
	procRegisterClassExW          = user32Blackout.NewProc("RegisterClassExW")
	procCreateWindowExW           = user32Blackout.NewProc("CreateWindowExW")
	procDestroyWindow             = user32Blackout.NewProc("DestroyWindow")
	procDefWindowProcW            = user32Blackout.NewProc("DefWindowProcW")
	procShowWindow                = user32Blackout.NewProc("ShowWindow")
	procSetWindowPos              = user32Blackout.NewProc("SetWindowPos")
	procSetLayeredWindowAttrs     = user32Blackout.NewProc("SetLayeredWindowAttributes")
	procSetWindowDisplayAffinity  = user32Blackout.NewProc("SetWindowDisplayAffinity")
	procGetMessageW               = user32Blackout.NewProc("GetMessageW")
	procTranslateMessage          = user32Blackout.NewProc("TranslateMessage")
	procDispatchMessageW          = user32Blackout.NewProc("DispatchMessageW")
	procPostMessageW              = user32Blackout.NewProc("PostMessageW")
	procPostQuitMessage           = user32Blackout.NewProc("PostQuitMessage")
	procRegisterHotKey            = user32Blackout.NewProc("RegisterHotKey")
	procUnregisterHotKey          = user32Blackout.NewProc("UnregisterHotKey")
	procGetSystemMetrics          = user32Blackout.NewProc("GetSystemMetrics")
	procCreateSolidBrush          = gdi32Blackout.NewProc("CreateSolidBrush")
	procDeleteObjectBlackout      = gdi32Blackout.NewProc("DeleteObject")
	procGetModuleHandleW          = kernel32Blackout.NewProc("GetModuleHandleW")
)

const (
	wdaExcludeFromCapture uint32 = 0x00000011

	wsPopup          uint32 = 0x80000000
	wsExTopmost      uint32 = 0x00000008
	wsExTransparent  uint32 = 0x00000020
	wsExToolWindow   uint32 = 0x00000080
	wsExLayered      uint32 = 0x00080000
	wsExNoActivate   uint32 = 0x08000000
	lwaAlpha         uint32 = 0x00000002

	swHide           uintptr = 0
	swShowNoActivate uintptr = 4
	hwndTopmost      uintptr = ^uintptr(0)
	swpNoMove        uint32  = 0x0002
	swpNoSize        uint32  = 0x0001
	swpNoActivate    uint32  = 0x0010
	swpShowWindow    uint32  = 0x0040

	wmDestroy     uint32 = 0x0002
	wmClose       uint32 = 0x0010
	wmHotKey      uint32 = 0x0312
	wmBlackoutSet uint32 = 0x8001 // WM_APP + 1

	smXVirtualScreen  uintptr = 76
	smYVirtualScreen  uintptr = 77
	smCXVirtualScreen uintptr = 78
	smCYVirtualScreen uintptr = 79

	modAlt      uint32 = 0x0001
	modControl  uint32 = 0x0002
	modNoRepeat uint32 = 0x4000
	vkB         uint32 = 0x42
	hotkeyID    int32  = 1
)

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type winMsg struct {
	hwnd    uintptr
	message uint32
	_pad0   uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ X, Y int32 }
	_pad1   uint32
}

// BlackoutController manages the physical screen privacy curtain using WDA_EXCLUDEFROMCAPTURE.
// Triple safety: auto-disables on peer disconnect, exits cleanly on shutdown, and supports Ctrl+Alt+B emergency off.
type BlackoutController struct {
	mu       sync.Mutex
	hwnd     uintptr
	enabled  atomic.Bool
	affinity atomic.Bool
	onChange func(bool)
	closed   atomic.Bool
	doneCh   chan struct{}
}

func NewBlackoutController(onChange func(bool)) (*BlackoutController, error) {
	b := &BlackoutController{
		onChange: onChange,
		doneCh:   make(chan struct{}),
	}
	ready := make(chan error, 1)
	go b.uiThread(ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return b, nil
}

func (b *BlackoutController) uiThread(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(b.doneCh)

	capture.AttachToInputDesktop()

	hInst, _, _ := procGetModuleHandleW.Call(0)
	hBrush, _, _ := procCreateSolidBrush.Call(0) // pure pitch black RGB(0,0,0)
	if hBrush != 0 {
		defer procDeleteObjectBlackout.Call(hBrush)
	}

	className, _ := windows.UTF16PtrFromString("ZeroDeskBlackoutClass")
	winTitle, _ := windows.UTF16PtrFromString("ZeroDesk Blackout Curtain")

	wndProc := syscall.NewCallback(func(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
		switch msg {
		case wmHotKey:
			if int32(wParam) == hotkeyID {
				b.applyVisible(hwnd, false)
			}
			return 0
		case wmBlackoutSet:
			b.applyVisible(hwnd, wParam != 0)
			return 0
		case wmClose:
			b.applyVisible(hwnd, false)
			procDestroyWindow.Call(hwnd)
			return 0
		case wmDestroy:
			procUnregisterHotKey.Call(hwnd, uintptr(hotkeyID))
			procPostQuitMessage.Call(0)
			return 0
		default:
			r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
			return r
		}
	})

	wc := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   wndProc,
		hInstance:     hInst,
		hbrBackground: hBrush,
		lpszClassName: className,
	}
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	vx, _, _ := procGetSystemMetrics.Call(smXVirtualScreen)
	vy, _, _ := procGetSystemMetrics.Call(smYVirtualScreen)
	vw, _, _ := procGetSystemMetrics.Call(smCXVirtualScreen)
	vh, _, _ := procGetSystemMetrics.Call(smCYVirtualScreen)
	if vw == 0 || vh == 0 {
		vw = 1920
		vh = 1080
	}

	exStyle := wsExTopmost | wsExTransparent | wsExToolWindow | wsExLayered | wsExNoActivate
	hwnd, _, err := procCreateWindowExW.Call(
		uintptr(exStyle),
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(winTitle)),
		uintptr(wsPopup),
		vx, vy, vw, vh,
		0, 0, hInst, 0,
	)
	if hwnd == 0 {
		ready <- fmt.Errorf("CreateWindowExW blackout failed: %w", err)
		return
	}

	// Make completely opaque pure black, click-through via WS_EX_TRANSPARENT | WS_EX_LAYERED
	procSetLayeredWindowAttrs.Call(hwnd, 0, 255, uintptr(lwaAlpha))

	// Critical: Exclude this blackout window from DXGI Desktop Duplication capture
	affOK, _, affErr := procSetWindowDisplayAffinity.Call(hwnd, uintptr(wdaExcludeFromCapture))
	if affOK == 0 {
		procDestroyWindow.Call(hwnd)
		ready <- fmt.Errorf("SetWindowDisplayAffinity(WDA_EXCLUDEFROMCAPTURE) failed: %w", affErr)
		return
	}
	b.affinity.Store(true)

	// Emergency physical keyboard kill switch: Ctrl + Alt + B
	procRegisterHotKey.Call(hwnd, uintptr(hotkeyID), uintptr(modControl|modAlt|modNoRepeat), uintptr(vkB))

	b.mu.Lock()
	b.hwnd = hwnd
	b.mu.Unlock()
	ready <- nil

	var msg winMsg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func (b *BlackoutController) applyVisible(hwnd uintptr, show bool) {
	if show && !b.affinity.Load() {
		return
	}
	if show {
		vx, _, _ := procGetSystemMetrics.Call(smXVirtualScreen)
		vy, _, _ := procGetSystemMetrics.Call(smYVirtualScreen)
		vw, _, _ := procGetSystemMetrics.Call(smCXVirtualScreen)
		vh, _, _ := procGetSystemMetrics.Call(smCYVirtualScreen)
		procSetWindowPos.Call(hwnd, hwndTopmost, vx, vy, vw, vh, uintptr(swpNoActivate|swpShowWindow))
		procShowWindow.Call(hwnd, swShowNoActivate)
	} else {
		procShowWindow.Call(hwnd, swHide)
	}
	prev := b.enabled.Swap(show)
	if prev != show && b.onChange != nil {
		b.onChange(show)
	}
}

func (b *BlackoutController) SetEnabled(enable bool) error {
	if b == nil || b.closed.Load() {
		return nil
	}
	if enable && !b.affinity.Load() {
		return fmt.Errorf("WDA_EXCLUDEFROMCAPTURE unavailable; refusing blackout")
	}
	b.mu.Lock()
	hwnd := b.hwnd
	b.mu.Unlock()
	if hwnd == 0 {
		return fmt.Errorf("blackout window not initialized")
	}
	var arg uintptr
	if enable {
		arg = 1
	}
	procPostMessageW.Call(hwnd, uintptr(wmBlackoutSet), arg, 0)
	return nil
}

func (b *BlackoutController) Toggle() bool {
	next := !b.enabled.Load()
	_ = b.SetEnabled(next)
	return next
}

func (b *BlackoutController) IsEnabled() bool {
	if b == nil {
		return false
	}
	return b.enabled.Load()
}

func (b *BlackoutController) Close() {
	if b == nil || b.closed.Swap(true) {
		return
	}
	b.mu.Lock()
	hwnd := b.hwnd
	b.mu.Unlock()
	if hwnd != 0 {
		procPostMessageW.Call(hwnd, uintptr(wmClose), 0, 0)
	}
	<-b.doneCh
}
