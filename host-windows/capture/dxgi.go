package capture

import (
	"errors"
	"fmt"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	dxgiDLL                       = windows.NewLazySystemDLL("dxgi.dll")
	d3d11DLL                      = windows.NewLazySystemDLL("d3d11.dll")
	user32DLL                     = windows.NewLazySystemDLL("user32.dll")
	gdi32DLL                      = windows.NewLazySystemDLL("gdi32.dll")
	procCreateDXGIFactory1        = dxgiDLL.NewProc("CreateDXGIFactory1")
	procD3D11CreateDevice         = d3d11DLL.NewProc("D3D11CreateDevice")
	procSetProcessDpiAwarenessCtx = user32DLL.NewProc("SetProcessDpiAwarenessContext")
	procOpenInputDesktop          = user32DLL.NewProc("OpenInputDesktop")
	procSetThreadDesktop          = user32DLL.NewProc("SetThreadDesktop")
	procCloseDesktop              = user32DLL.NewProc("CloseDesktop")
	procGetDC                     = user32DLL.NewProc("GetDC")
	procReleaseDC                 = user32DLL.NewProc("ReleaseDC")
	procGetCursorInfo             = user32DLL.NewProc("GetCursorInfo")
	procGetIconInfo               = user32DLL.NewProc("GetIconInfo")
	procDrawIconEx                = user32DLL.NewProc("DrawIconEx")
	procCreateCompatibleDC        = gdi32DLL.NewProc("CreateCompatibleDC")
	procDeleteDC                  = gdi32DLL.NewProc("DeleteDC")
	procSelectObject              = gdi32DLL.NewProc("SelectObject")
	procDeleteObject              = gdi32DLL.NewProc("DeleteObject")
	procCreateDIBSection          = gdi32DLL.NewProc("CreateDIBSection")
	procBitBlt                    = gdi32DLL.NewProc("BitBlt")
)

var (
	ErrTimeout    = errors.New("dxgi frame timeout")
	ErrAccessLost = errors.New("dxgi access lost")
)

const (
	dxgiErrorNotFound    uint32 = 0x887A0002
	dxgiErrorAccessLost  uint32 = 0x887A0026
	dxgiErrorWaitTimeout uint32 = 0x887A0027
	eAccessDenied        uint32 = 0x80070005

	d3dDriverTypeUnknown         uintptr = 0
	d3d11CreateDeviceBgraSupport uintptr = 0x20
	d3d11SDKVersion              uintptr = 7

	d3d11UsageDefault            uint32 = 0
	d3d11UsageStaging            uint32 = 3
	d3d11BindShaderResource      uint32 = 0x8
	d3d11BindRenderTarget        uint32 = 0x20
	d3d11CPUAccessRead           uint32 = 0x20000
	d3d11ResourceMiscGDICompat   uint32 = 0x2
	d3d11MapRead                 uint32 = 1
	dxgiFormatB8G8R8A8Unorm      uint32 = 87

	cursorShowing uint32 = 0x00000001
	diNormal      uintptr = 0x0003
)

var (
	iidIDXGIFactory1   = windows.GUID{Data1: 0x770aae78, Data2: 0xf26f, Data3: 0x4dba, Data4: [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}
	iidIDXGIOutput1    = windows.GUID{Data1: 0x00cddea8, Data2: 0x939b, Data3: 0x4b83, Data4: [8]byte{0xa3, 0x40, 0xa6, 0x85, 0x22, 0x66, 0x66, 0xcc}}
	iidID3D11Texture2D = windows.GUID{Data1: 0x6f15aaf2, Data2: 0xd208, Data3: 0x4e89, Data4: [8]byte{0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c}}
	iidIDXGISurface1   = windows.GUID{Data1: 0x4ae63092, Data2: 0x6327, Data3: 0x4c1b, Data4: [8]byte{0x80, 0xae, 0xbf, 0xe1, 0x2e, 0xa3, 0x2b, 0x86}}
)

func vtblCall(obj unsafe.Pointer, slot int, args ...uintptr) uintptr {
	base := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Pointer(uintptr(base) + uintptr(slot)*unsafe.Sizeof(uintptr(0))))
	fullArgs := make([]uintptr, 1+len(args))
	fullArgs[0] = uintptr(obj)
	copy(fullArgs[1:], args)
	r1, _, _ := syscall.SyscallN(fn, fullArgs...)
	return r1
}

func comRelease(obj unsafe.Pointer) {
	if obj != nil {
		vtblCall(obj, 2) // IUnknown::Release
	}
}

type rect struct {
	Left, Top, Right, Bottom int32
}

type dxgiOutputDesc struct {
	DeviceName         [32]uint16
	DesktopCoordinates rect
	AttachedToDesktop  int32
	Rotation           uint32
	Monitor            uintptr
}

type dxgiModeDesc struct {
	Width            uint32
	Height           uint32
	RefreshNum       uint32
	RefreshDen       uint32
	Format           uint32
	ScanlineOrdering uint32
	Scaling          uint32
}

type dxgiOutduplDesc struct {
	ModeDesc                   dxgiModeDesc
	Rotation                   uint32
	DesktopImageInSystemMemory int32
}

type dxgiPointerPos struct {
	X, Y    int32
	Visible int32
}

type dxgiOutduplFrameInfo struct {
	LastPresentTime           int64
	LastMouseUpdateTime       int64
	AccumulatedFrames         uint32
	RectsCoalesced            int32
	ProtectedContentMaskedOut int32
	PointerPosition           dxgiPointerPos
	TotalMetadataBufferSize   uint32
	PointerShapeBufferSize    uint32
}

type d3d11Texture2DDesc struct {
	Width          uint32
	Height         uint32
	MipLevels      uint32
	ArraySize      uint32
	Format         uint32
	SampleCount    uint32
	SampleQuality  uint32
	Usage          uint32
	BindFlags      uint32
	CPUAccessFlags uint32
	MiscFlags      uint32
}

type d3d11MappedSubresource struct {
	pData      *byte
	RowPitch   uint32
	DepthPitch uint32
}

type cursorInfo struct {
	cbSize      uint32
	flags       uint32
	hCursor     uintptr
	ptScreenPos struct{ X, Y int32 }
}

type iconInfo struct {
	fIcon    int32
	xHotspot uint32
	yHotspot uint32
	_pad     uint32
	hbmMask  uintptr
	hbmColor uintptr
}

type cursorState struct {
	x       int32
	y       int32
	hCursor uintptr
	visible bool
}

type DXGICapturer struct {
	device         unsafe.Pointer
	context        unsafe.Pointer
	duplication    unsafe.Pointer
	lastDesktopTex unsafe.Pointer // clean desktop VRAM texture without cursor
	composedTex    unsafe.Pointer // GDI-compatible VRAM texture with system cursor drawn
	stagingTex     unsafe.Pointer // CPU-readback staging texture (only mapped on MF fallback)
	Width          int
	Height         int
	outputLeft     int32
	outputTop      int32
	frameBuf       []byte
	hasFrame       bool

	lastCursor      cursorState
	cachedHotCursor uintptr
	cachedHotX      int32
	cachedHotY      int32
	lastComposeDur  time.Duration
}

// AttachToInputDesktop binds the current OS thread to the active interactive desktop (Default/Winlogon/UAC).
func AttachToInputDesktop() {
	const maximumAllowed = 0x02000000
	hDesk, _, _ := procOpenInputDesktop.Call(0, 0, maximumAllowed)
	if hDesk != 0 {
		_, _, _ = procSetThreadDesktop.Call(hDesk)
		_, _, _ = procCloseDesktop.Call(hDesk)
	}
}

func InitDPIAwareness() {
	if procSetProcessDpiAwarenessCtx.Find() == nil {
		_, _, _ = procSetProcessDpiAwarenessCtx.Call(^uintptr(3))
	}
}

func NewDXGICapturer() (*DXGICapturer, error) {
	c := &DXGICapturer{}
	if err := c.initDeviceAndDuplication(); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *DXGICapturer) D3DDevice() unsafe.Pointer {
	return c.device
}

func (c *DXGICapturer) ComposedTexture() unsafe.Pointer {
	return c.composedTex
}

func (c *DXGICapturer) LastComposeDuration() time.Duration {
	return c.lastComposeDur
}

func (c *DXGICapturer) initDeviceAndDuplication() error {
	AttachToInputDesktop()
	var factory unsafe.Pointer
	hr, _, _ := procCreateDXGIFactory1.Call(
		uintptr(unsafe.Pointer(&iidIDXGIFactory1)),
		uintptr(unsafe.Pointer(&factory)),
	)
	if uint32(hr) != 0 {
		return fmt.Errorf("CreateDXGIFactory1 failed: 0x%08x", uint32(hr))
	}
	defer comRelease(factory)

	type candidate struct {
		adapter   unsafe.Pointer
		output    unsafe.Pointer
		isPrimary bool
		left      int32
		top       int32
		width     int
		height    int
	}
	var candidates []candidate

	for ai := uint32(0); ; ai++ {
		var adapter unsafe.Pointer
		r := uint32(vtblCall(factory, 12, uintptr(ai), uintptr(unsafe.Pointer(&adapter)))) // EnumAdapters1
		if r == dxgiErrorNotFound {
			break
		}
		if r != 0 {
			continue
		}
		for oi := uint32(0); ; oi++ {
			var output unsafe.Pointer
			ro := uint32(vtblCall(adapter, 7, uintptr(oi), uintptr(unsafe.Pointer(&output)))) // EnumOutputs
			if ro == dxgiErrorNotFound {
				break
			}
			if ro != 0 {
				continue
			}
			var desc dxgiOutputDesc
			if uint32(vtblCall(output, 7, uintptr(unsafe.Pointer(&desc)))) == 0 && desc.AttachedToDesktop != 0 {
				w := int(desc.DesktopCoordinates.Right - desc.DesktopCoordinates.Left)
				h := int(desc.DesktopCoordinates.Bottom - desc.DesktopCoordinates.Top)
				isPri := desc.DesktopCoordinates.Left == 0 && desc.DesktopCoordinates.Top == 0
				vtblCall(adapter, 1) // AddRef
				candidates = append(candidates, candidate{
					adapter:   adapter,
					output:    output,
					isPrimary: isPri,
					left:      desc.DesktopCoordinates.Left,
					top:       desc.DesktopCoordinates.Top,
					width:     w,
					height:    h,
				})
			} else {
				comRelease(output)
			}
		}
		comRelease(adapter)
	}

	defer func() {
		for _, cand := range candidates {
			comRelease(cand.output)
			comRelease(cand.adapter)
		}
	}()

	if len(candidates) == 0 {
		return errors.New("no attached desktop display output found")
	}

	ordered := make([]candidate, 0, len(candidates))
	for _, cand := range candidates {
		if cand.isPrimary {
			ordered = append(ordered, cand)
		}
	}
	for _, cand := range candidates {
		if !cand.isPrimary {
			ordered = append(ordered, cand)
		}
	}

	var lastErr error
	for _, cand := range ordered {
		var dev, ctx unsafe.Pointer
		var featureLevel uint32
		r, _, _ := procD3D11CreateDevice.Call(
			uintptr(cand.adapter),
			d3dDriverTypeUnknown,
			0,
			d3d11CreateDeviceBgraSupport,
			0,
			0,
			d3d11SDKVersion,
			uintptr(unsafe.Pointer(&dev)),
			uintptr(unsafe.Pointer(&featureLevel)),
			uintptr(unsafe.Pointer(&ctx)),
		)
		if uint32(r) != 0 {
			lastErr = fmt.Errorf("D3D11CreateDevice failed: 0x%08x", uint32(r))
			continue
		}

		var out1 unsafe.Pointer
		rq := uint32(vtblCall(cand.output, 0, uintptr(unsafe.Pointer(&iidIDXGIOutput1)), uintptr(unsafe.Pointer(&out1))))
		if rq != 0 {
			comRelease(ctx)
			comRelease(dev)
			lastErr = fmt.Errorf("QueryInterface IDXGIOutput1 failed: 0x%08x", rq)
			continue
		}

		var dupl unsafe.Pointer
		rd := uint32(vtblCall(out1, 22, uintptr(dev), uintptr(unsafe.Pointer(&dupl)))) // DuplicateOutput
		comRelease(out1)
		if rd != 0 {
			comRelease(ctx)
			comRelease(dev)
			lastErr = fmt.Errorf("DuplicateOutput failed: 0x%08x", rd)
			continue
		}

		var duplDesc dxgiOutduplDesc
		vtblCall(dupl, 7, uintptr(unsafe.Pointer(&duplDesc))) // GetDesc
		w := int(duplDesc.ModeDesc.Width)
		h := int(duplDesc.ModeDesc.Height)
		if w <= 0 || h <= 0 {
			w, h = cand.width, cand.height
		}

		// 1. Clean desktop VRAM cache texture
		desktopTexDesc := d3d11Texture2DDesc{
			Width:       uint32(w),
			Height:      uint32(h),
			MipLevels:   1,
			ArraySize:   1,
			Format:      dxgiFormatB8G8R8A8Unorm,
			SampleCount: 1,
			Usage:       d3d11UsageDefault,
			BindFlags:   d3d11BindShaderResource | d3d11BindRenderTarget,
		}
		var lastTex unsafe.Pointer
		if rt := uint32(vtblCall(dev, 5, uintptr(unsafe.Pointer(&desktopTexDesc)), 0, uintptr(unsafe.Pointer(&lastTex)))); rt != 0 {
			comRelease(dupl)
			comRelease(ctx)
			comRelease(dev)
			lastErr = fmt.Errorf("CreateTexture2D lastDesktopTex failed: 0x%08x", rt)
			continue
		}

		// 2. GDI-compatible VRAM texture for zero-copy hardware cursor composition via IDXGISurface1::GetDC
		compTexDesc := d3d11Texture2DDesc{
			Width:       uint32(w),
			Height:      uint32(h),
			MipLevels:   1,
			ArraySize:   1,
			Format:      dxgiFormatB8G8R8A8Unorm,
			SampleCount: 1,
			Usage:       d3d11UsageDefault,
			BindFlags:   d3d11BindShaderResource | d3d11BindRenderTarget,
			MiscFlags:   d3d11ResourceMiscGDICompat,
		}
		var compTex unsafe.Pointer
		if rt := uint32(vtblCall(dev, 5, uintptr(unsafe.Pointer(&compTexDesc)), 0, uintptr(unsafe.Pointer(&compTex)))); rt != 0 {
			comRelease(lastTex)
			comRelease(dupl)
			comRelease(ctx)
			comRelease(dev)
			lastErr = fmt.Errorf("CreateTexture2D composedTex failed: 0x%08x", rt)
			continue
		}

		// 3. Staging texture (only mapped if CPU fallback encoder is active)
		stageDesc := d3d11Texture2DDesc{
			Width:          uint32(w),
			Height:         uint32(h),
			MipLevels:      1,
			ArraySize:      1,
			Format:         dxgiFormatB8G8R8A8Unorm,
			SampleCount:    1,
			Usage:          d3d11UsageStaging,
			CPUAccessFlags: d3d11CPUAccessRead,
		}
		var staging unsafe.Pointer
		if rt := uint32(vtblCall(dev, 5, uintptr(unsafe.Pointer(&stageDesc)), 0, uintptr(unsafe.Pointer(&staging)))); rt != 0 {
			comRelease(compTex)
			comRelease(lastTex)
			comRelease(dupl)
			comRelease(ctx)
			comRelease(dev)
			lastErr = fmt.Errorf("CreateTexture2D staging failed: 0x%08x", rt)
			continue
		}

		c.device = dev
		c.context = ctx
		c.duplication = dupl
		c.lastDesktopTex = lastTex
		c.composedTex = compTex
		c.stagingTex = staging
		c.Width = w
		c.Height = h
		c.outputLeft = cand.left
		c.outputTop = cand.top
		c.hasFrame = false
		c.lastCursor = cursorState{}
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	return errors.New("failed to initialize DXGI Desktop Duplication")
}

// Reinit destroys current D3D11/DXGI instances and recreates them safely on ACCESS_LOST or mode switch.
func (c *DXGICapturer) Reinit() error {
	c.releaseCOM()
	return c.initDeviceAndDuplication()
}

func (c *DXGICapturer) releaseCOM() {
	if c.stagingTex != nil {
		comRelease(c.stagingTex)
		c.stagingTex = nil
	}
	if c.composedTex != nil {
		comRelease(c.composedTex)
		c.composedTex = nil
	}
	if c.lastDesktopTex != nil {
		comRelease(c.lastDesktopTex)
		c.lastDesktopTex = nil
	}
	if c.duplication != nil {
		comRelease(c.duplication)
		c.duplication = nil
	}
	if c.context != nil {
		comRelease(c.context)
		c.context = nil
	}
	if c.device != nil {
		comRelease(c.device)
		c.device = nil
	}
}

func (c *DXGICapturer) Close() {
	c.releaseCOM()
}

func (c *DXGICapturer) pollCursor() cursorState {
	var ci cursorInfo
	ci.cbSize = uint32(unsafe.Sizeof(ci))
	ok, _, _ := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci)))
	if ok == 0 {
		return cursorState{}
	}
	return cursorState{
		x:       ci.ptScreenPos.X - c.outputLeft,
		y:       ci.ptScreenPos.Y - c.outputTop,
		hCursor: ci.hCursor,
		visible: (ci.flags & cursorShowing) != 0,
	}
}

func (c *DXGICapturer) getHotspot(hCursor uintptr) (int32, int32) {
	if hCursor == 0 {
		return 0, 0
	}
	if hCursor == c.cachedHotCursor {
		return c.cachedHotX, c.cachedHotY
	}
	var ii iconInfo
	ok, _, _ := procGetIconInfo.Call(hCursor, uintptr(unsafe.Pointer(&ii)))
	if ok == 0 {
		return 0, 0
	}
	if ii.hbmMask != 0 {
		procDeleteObject.Call(ii.hbmMask)
	}
	if ii.hbmColor != 0 {
		procDeleteObject.Call(ii.hbmColor)
	}
	c.cachedHotCursor = hCursor
	c.cachedHotX = int32(ii.xHotspot)
	c.cachedHotY = int32(ii.yHotspot)
	return c.cachedHotX, c.cachedHotY
}

// composeCursorGPU copies lastDesktopTex -> composedTex and draws the live Windows cursor via IDXGISurface1::GetDC + DrawIconEx.
func (c *DXGICapturer) composeCursorGPU(cur cursorState) {
	t0 := time.Now()
	vtblCall(c.context, 47, uintptr(c.composedTex), uintptr(c.lastDesktopTex)) // CopyResource
	c.lastCursor = cur

	if cur.visible && cur.hCursor != 0 {
		var surf unsafe.Pointer
		if uint32(vtblCall(c.composedTex, 0, uintptr(unsafe.Pointer(&iidIDXGISurface1)), uintptr(unsafe.Pointer(&surf)))) == 0 && surf != nil {
			var hdc uintptr
			if uint32(vtblCall(surf, 11, 0, uintptr(unsafe.Pointer(&hdc)))) == 0 && hdc != 0 { // GetDC(FALSE)
				hx, hy := c.getHotspot(cur.hCursor)
				drawX := uintptr(int(cur.x - hx))
				drawY := uintptr(int(cur.y - hy))
				procDrawIconEx.Call(hdc, drawX, drawY, cur.hCursor, 0, 0, 0, 0, diNormal)
				vtblCall(surf, 12, 0) // ReleaseDC(NULL)
			}
			comRelease(surf)
		}
	}
	c.lastComposeDur = time.Since(t0)
}

// seedInitialDesktopGPU captures the current static desktop into lastDesktopTex using IDXGISurface1::GetDC + BitBlt.
func (c *DXGICapturer) seedInitialDesktopGPU() bool {
	if c.composedTex == nil || c.lastDesktopTex == nil {
		return false
	}
	AttachToInputDesktop()
	hdcScreen, _, _ := procGetDC.Call(0)
	if hdcScreen == 0 {
		return false
	}
	defer procReleaseDC.Call(0, hdcScreen)

	var surf unsafe.Pointer
	if uint32(vtblCall(c.composedTex, 0, uintptr(unsafe.Pointer(&iidIDXGISurface1)), uintptr(unsafe.Pointer(&surf)))) != 0 || surf == nil {
		return false
	}
	defer comRelease(surf)

	var hdcDest uintptr
	if uint32(vtblCall(surf, 11, 0, uintptr(unsafe.Pointer(&hdcDest)))) != 0 || hdcDest == 0 { // GetDC(FALSE)
		return false
	}
	const srccopy = 0x00CC0020
	ok, _, _ := procBitBlt.Call(hdcDest, 0, 0, uintptr(c.Width), uintptr(c.Height), hdcScreen, uintptr(c.outputLeft), uintptr(c.outputTop), srccopy)
	vtblCall(surf, 12, 0) // ReleaseDC(NULL)
	if ok == 0 {
		return false
	}
	vtblCall(c.context, 47, uintptr(c.lastDesktopTex), uintptr(c.composedTex)) // CopyResource
	c.hasFrame = true
	return true
}

// AcquireGPUFrame captures the next desktop frame and composites the real hardware cursor on GPU VRAM.
// Even when DXGI returns WAIT_TIMEOUT (static screen), if the cursor moved or changed shape it immediately
// recomposites from lastDesktopTex so mouse movement streams at full 60 FPS.
func (c *DXGICapturer) AcquireGPUFrame(timeoutMs uint32, forceKeepalive bool) (tex unsafe.Pointer, w, h int, cursorMoved bool, err error) {
	if c.duplication == nil {
		return nil, 0, 0, false, ErrAccessLost
	}

	var frameInfo dxgiOutduplFrameInfo
	var desktopRes unsafe.Pointer
	hr := uint32(vtblCall(
		c.duplication,
		8, // AcquireNextFrame
		uintptr(timeoutMs),
		uintptr(unsafe.Pointer(&frameInfo)),
		uintptr(unsafe.Pointer(&desktopRes)),
	))

	cur := c.pollCursor()
	cursorChanged := cur != c.lastCursor

	if hr == dxgiErrorAccessLost || hr == eAccessDenied {
		return nil, 0, 0, false, ErrAccessLost
	}
	if hr == dxgiErrorWaitTimeout {
		if !c.hasFrame {
			if !c.seedInitialDesktopGPU() {
				return nil, c.Width, c.Height, false, ErrTimeout
			}
			c.composeCursorGPU(cur)
			return c.composedTex, c.Width, c.Height, cursorChanged, nil
		}
		if cursorChanged || forceKeepalive {
			c.composeCursorGPU(cur)
			return c.composedTex, c.Width, c.Height, cursorChanged, nil
		}
		return nil, c.Width, c.Height, false, ErrTimeout
	}
	if hr != 0 {
		return nil, 0, 0, false, fmt.Errorf("AcquireNextFrame error: 0x%08x", hr)
	}

	var acquiredTex unsafe.Pointer
	rq := uint32(vtblCall(desktopRes, 0, uintptr(unsafe.Pointer(&iidID3D11Texture2D)), uintptr(unsafe.Pointer(&acquiredTex))))
	comRelease(desktopRes)
	if rq != 0 {
		vtblCall(c.duplication, 14) // ReleaseFrame
		return nil, 0, 0, false, fmt.Errorf("QueryInterface ID3D11Texture2D failed: 0x%08x", rq)
	}

	vtblCall(c.context, 47, uintptr(c.lastDesktopTex), uintptr(acquiredTex)) // CopyResource clean desktop
	comRelease(acquiredTex)
	vtblCall(c.duplication, 14) // ReleaseFrame
	c.hasFrame = true

	c.composeCursorGPU(cur)
	return c.composedTex, c.Width, c.Height, cursorChanged, nil
}

// ReadComposedToCPU copies composedTex (desktop + cursor) to stagingTex and maps BGRA pixels for CPU fallback encoders.
func (c *DXGICapturer) ReadComposedToCPU() ([]byte, int, int, error) {
	if c.composedTex == nil || c.stagingTex == nil {
		return nil, 0, 0, ErrAccessLost
	}
	needed := c.Width * c.Height * 4
	if cap(c.frameBuf) < needed {
		c.frameBuf = make([]byte, needed)
	} else {
		c.frameBuf = c.frameBuf[:needed]
	}

	vtblCall(c.context, 47, uintptr(c.stagingTex), uintptr(c.composedTex)) // CopyResource

	var mapped d3d11MappedSubresource
	rm := uint32(vtblCall(
		c.context,
		14, // Map
		uintptr(c.stagingTex),
		0,
		uintptr(d3d11MapRead),
		0,
		uintptr(unsafe.Pointer(&mapped)),
	))
	if rm != 0 {
		return nil, 0, 0, fmt.Errorf("ID3D11DeviceContext::Map failed: 0x%08x", rm)
	}

	rowBytes := c.Width * 4
	pitch := int(mapped.RowPitch)
	srcSlice := unsafe.Slice(mapped.pData, pitch*c.Height)

	if pitch == rowBytes {
		copy(c.frameBuf, srcSlice[:rowBytes*c.Height])
	} else {
		for y := 0; y < c.Height; y++ {
			copy(c.frameBuf[y*rowBytes:(y+1)*rowBytes], srcSlice[y*pitch:y*pitch+rowBytes])
		}
	}

	vtblCall(c.context, 15, uintptr(c.stagingTex), 0) // Unmap
	return c.frameBuf, c.Width, c.Height, nil
}
