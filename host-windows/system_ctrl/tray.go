package system_ctrl

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
	"zerodesk-host/capture"
)

var (
	shell32Tray              = windows.NewLazySystemDLL("shell32.dll")
	user32Tray               = windows.NewLazySystemDLL("user32.dll")
	kernel32Tray             = windows.NewLazySystemDLL("kernel32.dll")
	gdi32Tray                = windows.NewLazySystemDLL("gdi32.dll")
	procShellNotifyIconW     = shell32Tray.NewProc("Shell_NotifyIconW")
	procShellExecuteW        = shell32Tray.NewProc("ShellExecuteW")
	procGetConsoleWindow     = kernel32Tray.NewProc("GetConsoleWindow")
	procGlobalAlloc          = kernel32Tray.NewProc("GlobalAlloc")
	procGlobalLock           = kernel32Tray.NewProc("GlobalLock")
	procGlobalUnlock         = kernel32Tray.NewProc("GlobalUnlock")
	procGlobalFree           = kernel32Tray.NewProc("GlobalFree")
	procRtlMoveMemory        = kernel32Tray.NewProc("RtlMoveMemory")
	procCreatePopupMenu      = user32Tray.NewProc("CreatePopupMenu")
	procAppendMenuW          = user32Tray.NewProc("AppendMenuW")
	procTrackPopupMenuEx     = user32Tray.NewProc("TrackPopupMenuEx")
	procDestroyMenu          = user32Tray.NewProc("DestroyMenu")
	procGetCursorPos         = user32Tray.NewProc("GetCursorPos")
	procSetForegroundWindow  = user32Tray.NewProc("SetForegroundWindow")
	procRegisterWindowMsgW   = user32Tray.NewProc("RegisterWindowMessageW")
	procLoadIconW            = user32Tray.NewProc("LoadIconW")
	procCreateIconIndirect   = user32Tray.NewProc("CreateIconIndirect")
	procDestroyIcon          = user32Tray.NewProc("DestroyIcon")
	procOpenClipboard        = user32Tray.NewProc("OpenClipboard")
	procEmptyClipboard       = user32Tray.NewProc("EmptyClipboard")
	procSetClipboardData     = user32Tray.NewProc("SetClipboardData")
	procCloseClipboard       = user32Tray.NewProc("CloseClipboard")
	procCreateDIBSectionTray = gdi32Tray.NewProc("CreateDIBSection")
	procCreateBitmapTray     = gdi32Tray.NewProc("CreateBitmap")
)

const (
	nimAdd    uint32 = 0x00000000
	nimDelete uint32 = 0x00000002

	nifMessage uint32 = 0x00000001
	nifIcon    uint32 = 0x00000002
	nifTip     uint32 = 0x00000004

	wmNull         uint32 = 0x0000
	wmCommand      uint32 = 0x0111
	wmRButtonUp    uint32 = 0x0205
	wmLButtonDbl   uint32 = 0x0203
	wmContextMenu  uint32 = 0x007B
	wmTrayCallback uint32 = 0x8002

	mfString    uintptr = 0x00000000
	mfSeparator uintptr = 0x00000800
	tpmReturn   uintptr = 0x0100
	tpmRightBtn uintptr = 0x0002

	cmdCopyInfo     uintptr = 1001
	cmdDisableBlack uintptr = 1002
	cmdOpenLogs     uintptr = 1003
	cmdExitApp      uintptr = 1004
)

type trayBitmapInfoHeader struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

type iconInfoTray struct {
	fIcon    int32
	xHotspot uint32
	yHotspot uint32
	_pad     uint32
	hbmMask  uintptr
	hbmColor uintptr
}

// HideConsoleWindow hides the attached console window cleanly for background tray mode.
func HideConsoleWindow() {
	if procGetConsoleWindow.Find() == nil {
		hwnd, _, _ := procGetConsoleWindow.Call()
		if hwnd != 0 {
			procShowWindow.Call(hwnd, swHide)
		}
	}
}

type TrayConfig struct {
	URL            string
	Pin            string
	LogDir         string
	OnClearBlack   func()
	OnExit         func()
}

type TrayController struct {
	cfg    TrayConfig
	mu     sync.Mutex
	hwnd   uintptr
	hIcon  uintptr
	closed atomic.Bool
	doneCh chan struct{}
}

func NewTrayController(cfg TrayConfig) (*TrayController, error) {
	t := &TrayController{
		cfg:    cfg,
		doneCh: make(chan struct{}),
	}
	ready := make(chan error, 1)
	go t.uiThread(ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return t, nil
}

func createTrayBadgeIcon() uintptr {
	const size = 32
	bmi := trayBitmapInfoHeader{
		biSize:     uint32(unsafe.Sizeof(trayBitmapInfoHeader{})),
		biWidth:    size,
		biHeight:   -size,
		biPlanes:   1,
		biBitCount: 32,
	}
	var bits *byte
	hColor, _, _ := procCreateDIBSectionTray.Call(0, uintptr(unsafe.Pointer(&bmi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hColor == 0 || bits == nil {
		hDef, _, _ := procLoadIconW.Call(0, 32512)
		return hDef
	}
	defer procDeleteObjectBlackout.Call(hColor)

	px := unsafe.Slice((*uint32)(unsafe.Pointer(bits)), size*size)
	cx, cy := 15.5, 15.5
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx := float64(x) - cx
			dy := float64(y) - cy
			r2 := dx*dx + dy*dy
			if r2 <= 14.5*14.5 {
				// Crisp indigo-blue rounded badge with white inner 'Z' glyph bar
				if (y >= 9 && y <= 11 && x >= 9 && x <= 22) ||
					(y >= 20 && y <= 22 && x >= 9 && x <= 22) ||
					(y >= 11 && y <= 20 && x+y >= 30 && x+y <= 33) {
					px[y*size+x] = 0xFFFFFFFF
				} else {
					px[y*size+x] = 0xFF3B82F6 // ARGB blue
				}
			} else {
				px[y*size+x] = 0x00000000
			}
		}
	}

	hMask, _, _ := procCreateBitmapTray.Call(size, size, 1, 1, 0)
	if hMask != 0 {
		defer procDeleteObjectBlackout.Call(hMask)
	}
	ii := iconInfoTray{
		fIcon:    1,
		hbmMask:  hMask,
		hbmColor: hColor,
	}
	hIcon, _, _ := procCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))
	if hIcon == 0 {
		hDef, _, _ := procLoadIconW.Call(0, 32512)
		return hDef
	}
	return hIcon
}

func (t *TrayController) addNotifyIcon(hwnd, hIcon uintptr) {
	var nid [976]byte // sizeof(NOTIFYICONDATAW) on x64
	*(*uint32)(unsafe.Pointer(&nid[0])) = 976
	*(*uintptr)(unsafe.Pointer(&nid[8])) = hwnd
	*(*uint32)(unsafe.Pointer(&nid[16])) = 1
	*(*uint32)(unsafe.Pointer(&nid[20])) = nifMessage | nifIcon | nifTip
	*(*uint32)(unsafe.Pointer(&nid[24])) = wmTrayCallback
	*(*uintptr)(unsafe.Pointer(&nid[32])) = hIcon

	tipText := fmt.Sprintf("ZeroDesk (%s) PIN: %s", t.cfg.URL, t.cfg.Pin)
	u16 := utf16.Encode([]rune(tipText))
	if len(u16) > 127 {
		u16 = u16[:127]
	}
	dstTip := unsafe.Slice((*uint16)(unsafe.Pointer(&nid[40])), 128)
	copy(dstTip, u16)
	dstTip[len(u16)] = 0

	procShellNotifyIconW.Call(uintptr(nimAdd), uintptr(unsafe.Pointer(&nid[0])))
}

func (t *TrayController) deleteNotifyIcon(hwnd uintptr) {
	var nid [976]byte
	*(*uint32)(unsafe.Pointer(&nid[0])) = 976
	*(*uintptr)(unsafe.Pointer(&nid[8])) = hwnd
	*(*uint32)(unsafe.Pointer(&nid[16])) = 1
	procShellNotifyIconW.Call(uintptr(nimDelete), uintptr(unsafe.Pointer(&nid[0])))
}

func copyTextToClipboard(hwnd uintptr, text string) {
	u16 := utf16.Encode([]rune(text + "\x00"))
	byteLen := uintptr(len(u16) * 2)
	const gmemMoveable = 0x0002
	const cfUnicodeText = 13

	hMem, _, _ := procGlobalAlloc.Call(gmemMoveable, byteLen)
	if hMem == 0 {
		return
	}
	ptr, _, _ := procGlobalLock.Call(hMem)
	if ptr == 0 {
		procGlobalFree.Call(hMem)
		return
	}
	procRtlMoveMemory.Call(ptr, uintptr(unsafe.Pointer(&u16[0])), byteLen)
	procGlobalUnlock.Call(hMem)

	ok, _, _ := procOpenClipboard.Call(hwnd)
	if ok == 0 {
		procGlobalFree.Call(hMem)
		return
	}
	procEmptyClipboard.Call()
	res, _, _ := procSetClipboardData.Call(cfUnicodeText, hMem)
	procCloseClipboard.Call()
	if res == 0 {
		procGlobalFree.Call(hMem)
	}
}

func (t *TrayController) showContextMenu(hwnd uintptr) {
	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	defer procDestroyMenu.Call(hMenu)

	sCopy, _ := windows.UTF16PtrFromString("复制访问地址与 PIN")
	sBlack, _ := windows.UTF16PtrFromString("强制关闭物理黑幕")
	sLogs, _ := windows.UTF16PtrFromString("打开日志目录")
	sExit, _ := windows.UTF16PtrFromString("退出 ZeroDesk")

	procAppendMenuW.Call(hMenu, mfString, cmdCopyInfo, uintptr(unsafe.Pointer(sCopy)))
	procAppendMenuW.Call(hMenu, mfString, cmdDisableBlack, uintptr(unsafe.Pointer(sBlack)))
	procAppendMenuW.Call(hMenu, mfString, cmdOpenLogs, uintptr(unsafe.Pointer(sLogs)))
	procAppendMenuW.Call(hMenu, mfSeparator, 0, 0)
	procAppendMenuW.Call(hMenu, mfString, cmdExitApp, uintptr(unsafe.Pointer(sExit)))

	var pt struct{ X, Y int32 }
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(hwnd)
	cmd, _, _ := procTrackPopupMenuEx.Call(
		hMenu,
		tpmReturn|tpmRightBtn,
		uintptr(pt.X),
		uintptr(pt.Y),
		hwnd,
		0,
	)
	procPostMessageW.Call(hwnd, uintptr(wmNull), 0, 0)
	t.handleCommand(hwnd, cmd)
}

func (t *TrayController) handleCommand(hwnd uintptr, cmd uintptr) {
	switch cmd {
	case cmdCopyInfo:
		copyTextToClipboard(hwnd, fmt.Sprintf("%s | PIN: %s", t.cfg.URL, t.cfg.Pin))
	case cmdDisableBlack:
		if t.cfg.OnClearBlack != nil {
			t.cfg.OnClearBlack()
		}
	case cmdOpenLogs:
		op, _ := windows.UTF16PtrFromString("open")
		dir, _ := windows.UTF16PtrFromString(t.cfg.LogDir)
		procShellExecuteW.Call(0, uintptr(unsafe.Pointer(op)), uintptr(unsafe.Pointer(dir)), 0, 0, 1)
	case cmdExitApp:
		if t.cfg.OnExit != nil {
			t.cfg.OnExit()
		}
	}
}

func (t *TrayController) uiThread(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(t.doneCh)

	capture.AttachToInputDesktop()

	hInst, _, _ := procGetModuleHandleW.Call(0)
	hIcon := createTrayBadgeIcon()
	t.hIcon = hIcon
	defer func() {
		if hIcon != 0 {
			procDestroyIcon.Call(hIcon)
		}
	}()

	taskbarStr, _ := windows.UTF16PtrFromString("TaskbarCreated")
	wmTaskbarCreated, _, _ := procRegisterWindowMsgW.Call(uintptr(unsafe.Pointer(taskbarStr)))

	className, _ := windows.UTF16PtrFromString("ZeroDeskTrayClass")
	winTitle, _ := windows.UTF16PtrFromString("ZeroDesk Tray")

	wndProc := syscall.NewCallback(func(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
		if wmTaskbarCreated != 0 && uintptr(msg) == wmTaskbarCreated {
			t.addNotifyIcon(hwnd, hIcon)
			return 0
		}
		switch msg {
		case wmTrayCallback:
			switch uint32(lParam) {
			case wmRButtonUp, wmContextMenu:
				t.showContextMenu(hwnd)
			case wmLButtonDbl:
				t.handleCommand(hwnd, cmdCopyInfo)
			}
			return 0
		case wmCommand:
			t.handleCommand(hwnd, wParam&0xFFFF)
			return 0
		case wmClose:
			t.deleteNotifyIcon(hwnd)
			procDestroyWindow.Call(hwnd)
			return 0
		case wmDestroy:
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
		lpszClassName: className,
	}
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(winTitle)),
		0,
		0, 0, 0, 0,
		0, 0, hInst, 0,
	)
	if hwnd == 0 {
		ready <- fmt.Errorf("CreateWindowExW tray failed: %w", err)
		return
	}

	t.addNotifyIcon(hwnd, hIcon)
	t.mu.Lock()
	t.hwnd = hwnd
	t.mu.Unlock()
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

func (t *TrayController) Close() {
	if t == nil || t.closed.Swap(true) {
		return
	}
	t.mu.Lock()
	hwnd := t.hwnd
	t.mu.Unlock()
	if hwnd != 0 {
		procPostMessageW.Call(hwnd, uintptr(wmClose), 0, 0)
	}
	<-t.doneCh
}
