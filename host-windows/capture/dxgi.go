package capture

import (
	"errors"
	"fmt"
	"math"
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
	procGetCursorPos              = user32DLL.NewProc("GetCursorPos")
	procGetIconInfo               = user32DLL.NewProc("GetIconInfo")
	procDrawIconEx                = user32DLL.NewProc("DrawIconEx")
	procGetSystemMetrics          = user32DLL.NewProc("GetSystemMetrics")
	procCreateCompatibleDC        = gdi32DLL.NewProc("CreateCompatibleDC")
	procDeleteDC                  = gdi32DLL.NewProc("DeleteDC")
	procSelectObject              = gdi32DLL.NewProc("SelectObject")
	procDeleteObject              = gdi32DLL.NewProc("DeleteObject")
	procCreateDIBSection          = gdi32DLL.NewProc("CreateDIBSection")
	procBitBlt                    = gdi32DLL.NewProc("BitBlt")
	procGetObjectW                = gdi32DLL.NewProc("GetObjectW")
	procGdiFlush                  = gdi32DLL.NewProc("GdiFlush")
	kernel32DLL                   = windows.NewLazySystemDLL("kernel32.dll")
	procSetThreadExecutionState   = kernel32DLL.NewProc("SetThreadExecutionState")
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

	d3d11UsageDefault       uint32 = 0
	d3d11UsageStaging       uint32 = 3
	d3d11BindShaderResource uint32 = 0x8
	d3d11BindRenderTarget   uint32 = 0x20
	d3d11CPUAccessRead      uint32 = 0x20000
	d3d11MapRead            uint32 = 1
	dxgiFormatB8G8R8A8Unorm uint32 = 87

	cursorShowing  uint32  = 0x00000001
	diNormal       uintptr = 0x0003
	smCXCursor     uintptr = 13
	smCYCursor     uintptr = 14
	cursorPatchMax int     = 128
	cursorScale            = 1.35
)

var (
	iidIDXGIFactory1   = windows.GUID{Data1: 0x770aae78, Data2: 0xf26f, Data3: 0x4dba, Data4: [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}
	iidIDXGIOutput1    = windows.GUID{Data1: 0x00cddea8, Data2: 0x939b, Data3: 0x4b83, Data4: [8]byte{0xa3, 0x40, 0xa6, 0x85, 0x22, 0x66, 0x66, 0xcc}}
	iidID3D11Texture2D = windows.GUID{Data1: 0x6f15aaf2, Data2: 0xd208, Data3: 0x4e89, Data4: [8]byte{0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c}}
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

type d3d11Box struct {
	left   uint32
	top    uint32
	front  uint32
	right  uint32
	bottom uint32
	back   uint32
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

type gdiBitmap struct {
	bmType       int32
	bmWidth      int32
	bmHeight     int32
	bmWidthBytes int32
	bmPlanes     uint16
	bmBitsPixel  uint16
	bmBits       uintptr
}

type bitmapInfoHeader struct {
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

type cursorState struct {
	x       int32
	y       int32
	hCursor uintptr
	visible bool
}

const (
	pixelTransparent uint8 = 0
	pixelOpaque      uint8 = 1
	pixelAlpha       uint8 = 2
	pixelXOR         uint8 = 3
)

type cursorPixel struct {
	kind       uint8
	b, g, r, a uint8
}

// CursorSprite caches a decoded, scaled Windows cursor (supporting opaque, alpha-blended, and XOR inverted pixels).
type CursorSprite struct {
	width  int
	height int
	hotX   int32
	hotY   int32
	minX   int
	minY   int
	maxX   int
	maxY   int
	pixels []cursorPixel
}

type DXGICapturer struct {
	device          unsafe.Pointer
	context         unsafe.Pointer
	duplication     unsafe.Pointer
	lastDesktopTex  unsafe.Pointer // clean desktop VRAM texture without cursor
	composedTex     unsafe.Pointer // pure D3D11 VRAM texture with cursor composited (zero GDI lock conflict)
	cursorBgStaging unsafe.Pointer // 128x128 staging texture to read background under cursor
	cursorPatchGPU  unsafe.Pointer // 128x128 default texture to upload blended cursor patch
	stagingTex      unsafe.Pointer // full-frame staging texture (only mapped on CPU fallback)
	Width           int
	Height          int
	outputLeft      int32
	outputTop       int32
	frameBuf        []byte
	patchBuf        []byte
	hasFrame        bool

	lastCursor     cursorState
	cursorCache    map[uintptr]*CursorSprite
	fallbackSprite *CursorSprite
	lastComposeDur time.Duration
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

// WakeDisplay signals Windows to wake/keep the display active when a remote bedside client connects.
func WakeDisplay() {
	const (
		esSystemRequired  uintptr = 0x00000001
		esDisplayRequired uintptr = 0x00000002
	)
	if procSetThreadExecutionState.Find() == nil {
		procSetThreadExecutionState.Call(esSystemRequired | esDisplayRequired)
	}
}

func InitDPIAwareness() {
	if procSetProcessDpiAwarenessCtx.Find() == nil {
		_, _, _ = procSetProcessDpiAwarenessCtx.Call(^uintptr(3))
	}
}

func NewDXGICapturer() (*DXGICapturer, error) {
	c := &DXGICapturer{
		patchBuf:       make([]byte, cursorPatchMax*cursorPatchMax*4),
		cursorCache:    make(map[uintptr]*CursorSprite),
		fallbackSprite: buildFallbackArrowSprite(),
	}
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

		createTex := func(tw, th int, usage, bind, cpuAccess uint32) (unsafe.Pointer, error) {
			td := d3d11Texture2DDesc{
				Width:          uint32(tw),
				Height:         uint32(th),
				MipLevels:      1,
				ArraySize:      1,
				Format:         dxgiFormatB8G8R8A8Unorm,
				SampleCount:    1,
				Usage:          usage,
				BindFlags:      bind,
				CPUAccessFlags: cpuAccess,
				MiscFlags:      0, // Strictly NO GDI_COMPATIBLE flag to avoid CUDA/NVENC interop lock
			}
			var tex unsafe.Pointer
			if rt := uint32(vtblCall(dev, 5, uintptr(unsafe.Pointer(&td)), 0, uintptr(unsafe.Pointer(&tex)))); rt != 0 {
				return nil, fmt.Errorf("CreateTexture2D(%dx%d, usage=%d) failed: 0x%08x", tw, th, usage, rt)
			}
			return tex, nil
		}

		lastTex, err := createTex(w, h, d3d11UsageDefault, d3d11BindShaderResource|d3d11BindRenderTarget, 0)
		if err != nil {
			comRelease(dupl)
			comRelease(ctx)
			comRelease(dev)
			lastErr = err
			continue
		}

		compTex, err := createTex(w, h, d3d11UsageDefault, d3d11BindShaderResource|d3d11BindRenderTarget, 0)
		if err != nil {
			comRelease(lastTex)
			comRelease(dupl)
			comRelease(ctx)
			comRelease(dev)
			lastErr = err
			continue
		}

		bgStage, err := createTex(cursorPatchMax, cursorPatchMax, d3d11UsageStaging, 0, d3d11CPUAccessRead)
		if err != nil {
			comRelease(compTex)
			comRelease(lastTex)
			comRelease(dupl)
			comRelease(ctx)
			comRelease(dev)
			lastErr = err
			continue
		}

		patchGPU, err := createTex(cursorPatchMax, cursorPatchMax, d3d11UsageDefault, d3d11BindShaderResource, 0)
		if err != nil {
			comRelease(bgStage)
			comRelease(compTex)
			comRelease(lastTex)
			comRelease(dupl)
			comRelease(ctx)
			comRelease(dev)
			lastErr = err
			continue
		}

		staging, err := createTex(w, h, d3d11UsageStaging, 0, d3d11CPUAccessRead)
		if err != nil {
			comRelease(patchGPU)
			comRelease(bgStage)
			comRelease(compTex)
			comRelease(lastTex)
			comRelease(dupl)
			comRelease(ctx)
			comRelease(dev)
			lastErr = err
			continue
		}

		c.device = dev
		c.context = ctx
		c.duplication = dupl
		c.lastDesktopTex = lastTex
		c.composedTex = compTex
		c.cursorBgStaging = bgStage
		c.cursorPatchGPU = patchGPU
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
	for _, p := range []*unsafe.Pointer{
		&c.stagingTex, &c.cursorPatchGPU, &c.cursorBgStaging,
		&c.composedTex, &c.lastDesktopTex, &c.duplication, &c.context, &c.device,
	} {
		if *p != nil {
			comRelease(*p)
			*p = nil
		}
	}
}

func (c *DXGICapturer) Close() {
	c.releaseCOM()
}

func (c *DXGICapturer) pollCursor() cursorState {
	var ci cursorInfo
	ci.cbSize = uint32(unsafe.Sizeof(ci))
	ok, _, _ := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci)))
	if ok != 0 && (ci.flags&cursorShowing) != 0 && ci.hCursor != 0 {
		return cursorState{
			x:       ci.ptScreenPos.X - c.outputLeft,
			y:       ci.ptScreenPos.Y - c.outputTop,
			hCursor: ci.hCursor,
			visible: true,
		}
	}

	// Mandatory fallback: GetCursorPos + high-contrast built-in arrow sprite so cursor is never invisible
	var pt struct{ X, Y int32 }
	if gp, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt))); gp != 0 {
		return cursorState{
			x:       pt.X - c.outputLeft,
			y:       pt.Y - c.outputTop,
			hCursor: 0,
			visible: true,
		}
	}
	return cursorState{
		x:       int32(c.Width / 2),
		y:       int32(c.Height / 2),
		hCursor: 0,
		visible: true,
	}
}

// buildFallbackArrowSprite constructs a crisp 1.35x high-contrast white-core black-outlined arrow cursor.
func buildFallbackArrowSprite() *CursorSprite {
	// 18x24 base silhouette scaled to 24x32 for crisp mobile visibility
	const w, h = 24, 32
	pixels := make([]cursorPixel, w*h)

	// Point-in-triangle check for crisp arrow polygon: (1,1), (1,25), (8,19), (13,29), (16,27), (11,17), (19,17)
	poly := [][2]float64{
		{1.5, 1.5}, {1.5, 25.5}, {7.8, 19.5}, {12.5, 29.5},
		{16.2, 27.8}, {11.5, 17.8}, {19.8, 17.5},
	}
	inside := func(px, py float64) bool {
		c := false
		j := len(poly) - 1
		for i := 0; i < len(poly); i++ {
			yi, yj := poly[i][1], poly[j][1]
			xi, xj := poly[i][0], poly[j][0]
			if ((yi > py) != (yj > py)) && (px < (xj-xi)*(py-yi)/(yj-yi+1e-9)+xi) {
				c = !c
			}
			j = i
		}
		return c
	}

	fillMask := make([]bool, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			fillMask[y*w+x] = inside(float64(x)+0.5, float64(y)+0.5)
		}
	}

	minX, minY, maxX, maxY := w, h, -1, -1
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := y*w + x
			if fillMask[idx] {
				pixels[idx] = cursorPixel{kind: pixelOpaque, b: 255, g: 255, r: 255, a: 255}
			} else {
				// 2-pixel black outline around white arrow core
				border := false
				for dy := -2; dy <= 2 && !border; dy++ {
					for dx := -2; dx <= 2; dx++ {
						if dx*dx+dy*dy > 5 {
							continue
						}
						nx, ny := x+dx, y+dy
						if nx >= 0 && nx < w && ny >= 0 && ny < h && fillMask[ny*w+nx] {
							border = true
							break
						}
					}
				}
				if border {
					pixels[idx] = cursorPixel{kind: pixelOpaque, b: 0, g: 0, r: 0, a: 255}
				}
			}
			if pixels[idx].kind != pixelTransparent {
				if x < minX {
					minX = x
				}
				if y < minY {
					minY = y
				}
				if x > maxX {
					maxX = x
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	return &CursorSprite{
		width: w, height: h, hotX: 2, hotY: 2,
		minX: minX, minY: minY, maxX: maxX, maxY: maxY,
		pixels: pixels,
	}
}

// decodeCursorSprite decodes a live Windows HCURSOR via dual black/white DIB DrawIconEx passes
// into a cached CursorSprite supporting opaque, alpha-blended, and XOR-inverted (I-Beam) pixels.
func (c *DXGICapturer) decodeCursorSprite(hCursor uintptr) *CursorSprite {
	if hCursor == 0 {
		return c.fallbackSprite
	}
	if cached, ok := c.cursorCache[hCursor]; ok {
		return cached
	}

	var ii iconInfo
	if ok, _, _ := procGetIconInfo.Call(hCursor, uintptr(unsafe.Pointer(&ii))); ok == 0 {
		return c.fallbackSprite
	}
	defer func() {
		if ii.hbmMask != 0 {
			procDeleteObject.Call(ii.hbmMask)
		}
		if ii.hbmColor != 0 {
			procDeleteObject.Call(ii.hbmColor)
		}
	}()

	baseW := 32
	baseH := 32
	if sw, _, _ := procGetSystemMetrics.Call(smCXCursor); sw > 0 {
		baseW = int(sw)
	}
	if sh, _, _ := procGetSystemMetrics.Call(smCYCursor); sh > 0 {
		baseH = int(sh)
	}
	var bmp gdiBitmap
	if ii.hbmColor != 0 {
		if r, _, _ := procGetObjectW.Call(ii.hbmColor, unsafe.Sizeof(bmp), uintptr(unsafe.Pointer(&bmp))); r != 0 && bmp.bmWidth > 0 && bmp.bmHeight > 0 {
			baseW = int(bmp.bmWidth)
			baseH = int(bmp.bmHeight)
		}
	} else if ii.hbmMask != 0 {
		if r, _, _ := procGetObjectW.Call(ii.hbmMask, unsafe.Sizeof(bmp), uintptr(unsafe.Pointer(&bmp))); r != 0 && bmp.bmWidth > 0 && bmp.bmHeight > 0 {
			baseW = int(bmp.bmWidth)
			baseH = int(bmp.bmHeight / 2)
			if baseH <= 0 {
				baseH = baseW
			}
		}
	}

	targetW := int(math.Round(float64(baseW) * cursorScale))
	targetH := int(math.Round(float64(baseH) * cursorScale))
	if targetW > cursorPatchMax {
		targetW = cursorPatchMax
	}
	if targetH > cursorPatchMax {
		targetH = cursorPatchMax
	}
	if targetW <= 0 || targetH <= 0 {
		return c.fallbackSprite
	}

	scaleX := float64(targetW) / float64(baseW)
	scaleY := float64(targetH) / float64(baseH)
	hotX := int32(math.Round(float64(ii.xHotspot) * scaleX))
	hotY := int32(math.Round(float64(ii.yHotspot) * scaleY))

	hdcScreen, _, _ := procGetDC.Call(0)
	if hdcScreen == 0 {
		return c.fallbackSprite
	}
	defer procReleaseDC.Call(0, hdcScreen)

	memDC, _, _ := procCreateCompatibleDC.Call(hdcScreen)
	if memDC == 0 {
		return c.fallbackSprite
	}
	defer procDeleteDC.Call(memDC)

	bmi := bitmapInfoHeader{
		biSize:     uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		biWidth:    int32(targetW),
		biHeight:   -int32(targetH),
		biPlanes:   1,
		biBitCount: 32,
	}
	var dibBits *byte
	hBmp, _, _ := procCreateDIBSection.Call(memDC, uintptr(unsafe.Pointer(&bmi)), 0, uintptr(unsafe.Pointer(&dibBits)), 0, 0)
	if hBmp == 0 || dibBits == nil {
		return c.fallbackSprite
	}
	defer procDeleteObject.Call(hBmp)

	oldObj, _, _ := procSelectObject.Call(memDC, hBmp)
	defer procSelectObject.Call(memDC, oldObj)

	totalPx := targetW * targetH
	dibSlice := unsafe.Slice((*uint32)(unsafe.Pointer(dibBits)), totalPx)

	// Pass 1: DrawIconEx on pure black (0x00000000)
	for i := 0; i < totalPx; i++ {
		dibSlice[i] = 0x00000000
	}
	procGdiFlush.Call()
	ok1, _, _ := procDrawIconEx.Call(memDC, 0, 0, hCursor, uintptr(targetW), uintptr(targetH), 0, 0, diNormal)
	procGdiFlush.Call()
	blackCopy := make([]uint32, totalPx)
	copy(blackCopy, dibSlice)

	// Pass 2: DrawIconEx on pure white (0x00FFFFFF)
	for i := 0; i < totalPx; i++ {
		dibSlice[i] = 0x00FFFFFF
	}
	procGdiFlush.Call()
	ok2, _, _ := procDrawIconEx.Call(memDC, 0, 0, hCursor, uintptr(targetW), uintptr(targetH), 0, 0, diNormal)
	procGdiFlush.Call()
	whiteCopy := make([]uint32, totalPx)
	copy(whiteCopy, dibSlice)

	if ok1 == 0 || ok2 == 0 {
		return c.fallbackSprite
	}

	pixels := make([]cursorPixel, totalPx)
	minX, minY, maxX, maxY := targetW, targetH, -1, -1
	nonTransparent := 0

	for y := 0; y < targetH; y++ {
		for x := 0; x < targetW; x++ {
			idx := y*targetW + x
			pxB := blackCopy[idx]
			pxW := whiteCopy[idx]

			bB, gB, rB := int(pxB&0xFF), int((pxB>>8)&0xFF), int((pxB>>16)&0xFF)
			bW, gW, rW := int(pxW&0xFF), int((pxW>>8)&0xFF), int((pxW>>16)&0xFF)

			if bB == 0 && gB == 0 && rB == 0 && bW == 255 && gW == 255 && rW == 255 {
				pixels[idx].kind = pixelTransparent
				continue
			}

			// XOR / inverted cursor pixel (e.g., I-Beam text cursor): black bg becomes bright while white bg becomes dark
			if bB > bW || gB > gW || rB > rW {
				pixels[idx] = cursorPixel{
					kind: pixelXOR,
					b:    uint8(bB),
					g:    uint8(gB),
					r:    uint8(rB),
					a:    255,
				}
			} else if bB == bW && gB == gW && rB == rW {
				pixels[idx] = cursorPixel{
					kind: pixelOpaque,
					b:    uint8(bB),
					g:    uint8(gB),
					r:    uint8(rB),
					a:    255,
				}
			} else {
				// Standard alpha blending: cW - cB = 255 * (1 - alpha)
				diff := ((bW - bB) + (gW - gB) + (rW - rB)) / 3
				alpha := 255 - diff
				if alpha < 1 {
					alpha = 1
				} else if alpha > 254 {
					alpha = 254
				}
				pixels[idx] = cursorPixel{
					kind: pixelAlpha,
					b:    uint8(bB),
					g:    uint8(gB),
					r:    uint8(rB),
					a:    uint8(alpha),
				}
			}

			nonTransparent++
			if x < minX {
				minX = x
			}
			if y < minY {
				minY = y
			}
			if x > maxX {
				maxX = x
			}
			if y > maxY {
				maxY = y
			}
		}
	}

	if nonTransparent == 0 {
		return c.fallbackSprite
	}

	sp := &CursorSprite{
		width: targetW, height: targetH, hotX: hotX, hotY: hotY,
		minX: minX, minY: minY, maxX: maxX, maxY: maxY,
		pixels: pixels,
	}
	if len(c.cursorCache) > 64 {
		c.cursorCache = make(map[uintptr]*CursorSprite)
	}
	c.cursorCache[hCursor] = sp
	return sp
}

// composeCursorGPU composites the cursor onto composedTex using pure D3D11 sub-region transfers
// (CopyResource + CopySubresourceRegion bg read + CPU local blend + UpdateSubresource + CopySubresourceRegion + Flush),
// avoiding IDXGISurface1::GetDC so NVENC D3D11 registration never interferes on frame 2+.
func (c *DXGICapturer) composeCursorGPU(cur cursorState) {
	t0 := time.Now()
	vtblCall(c.context, 47, uintptr(c.composedTex), uintptr(c.lastDesktopTex)) // CopyResource
	c.lastCursor = cur

	if !cur.visible {
		vtblCall(c.context, 111) // Flush
		c.lastComposeDur = time.Since(t0)
		return
	}

	sp := c.decodeCursorSprite(cur.hCursor)
	if sp == nil || sp.maxX < sp.minX || sp.maxY < sp.minY {
		sp = c.fallbackSprite
	}

	// Screen-space bounding box of non-transparent cursor pixels
	rawX0 := int(cur.x-sp.hotX) + sp.minX
	rawY0 := int(cur.y-sp.hotY) + sp.minY
	rawX1 := int(cur.x-sp.hotX) + sp.maxX + 1
	rawY1 := int(cur.y-sp.hotY) + sp.maxY + 1

	// Clip against desktop bounds
	dstX0 := rawX0
	if dstX0 < 0 {
		dstX0 = 0
	}
	dstY0 := rawY0
	if dstY0 < 0 {
		dstY0 = 0
	}
	dstX1 := rawX1
	if dstX1 > c.Width {
		dstX1 = c.Width
	}
	dstY1 := rawY1
	if dstY1 > c.Height {
		dstY1 = c.Height
	}
	boxW := dstX1 - dstX0
	boxH := dstY1 - dstY0
	if boxW <= 0 || boxH <= 0 {
		vtblCall(c.context, 111) // Flush
		c.lastComposeDur = time.Since(t0)
		return
	}
	if boxW > cursorPatchMax {
		boxW = cursorPatchMax
	}
	if boxH > cursorPatchMax {
		boxH = cursorPatchMax
	}

	// 1. Copy local desktop background region under cursor into 128x128 staging texture
	srcBox := d3d11Box{
		left:   uint32(dstX0),
		top:    uint32(dstY0),
		front:  0,
		right:  uint32(dstX0 + boxW),
		bottom: uint32(dstY0 + boxH),
		back:   1,
	}
	vtblCall(c.context, 46, uintptr(c.cursorBgStaging), 0, 0, 0, 0, uintptr(c.lastDesktopTex), 0, uintptr(unsafe.Pointer(&srcBox))) // CopySubresourceRegion

	var mapped d3d11MappedSubresource
	if rm := uint32(vtblCall(c.context, 14, uintptr(c.cursorBgStaging), 0, uintptr(d3d11MapRead), 0, uintptr(unsafe.Pointer(&mapped)))); rm != 0 {
		vtblCall(c.context, 111) // Flush
		c.lastComposeDur = time.Since(t0)
		return
	}

	pitch := int(mapped.RowPitch)
	bgSlice := unsafe.Slice(mapped.pData, pitch*boxH)
	patchStride := boxW * 4
	spOffX := sp.minX + (dstX0 - rawX0)
	spOffY := sp.minY + (dstY0 - rawY0)

	// 2. Blend CursorSprite pixels onto local background into pre-allocated patchBuf
	for py := 0; py < boxH; py++ {
		bgRow := bgSlice[py*pitch:]
		dstRow := c.patchBuf[py*patchStride:]
		spRowOff := (spOffY + py) * sp.width
		for px := 0; px < boxW; px++ {
			b0 := bgRow[px*4]
			g0 := bgRow[px*4+1]
			r0 := bgRow[px*4+2]

			cp := sp.pixels[spRowOff+spOffX+px]
			switch cp.kind {
			case pixelTransparent:
				dstRow[px*4] = b0
				dstRow[px*4+1] = g0
				dstRow[px*4+2] = r0
				dstRow[px*4+3] = 255
			case pixelOpaque:
				dstRow[px*4] = cp.b
				dstRow[px*4+1] = cp.g
				dstRow[px*4+2] = cp.r
				dstRow[px*4+3] = 255
			case pixelAlpha:
				invA := 255 - int(cp.a)
				nb := int(cp.b) + (int(b0)*invA)/255
				ng := int(cp.g) + (int(g0)*invA)/255
				nr := int(cp.r) + (int(r0)*invA)/255
				if nb > 255 {
					nb = 255
				}
				if ng > 255 {
					ng = 255
				}
				if nr > 255 {
					nr = 255
				}
				dstRow[px*4] = uint8(nb)
				dstRow[px*4+1] = uint8(ng)
				dstRow[px*4+2] = uint8(nr)
				dstRow[px*4+3] = 255
			case pixelXOR:
				// High-contrast XOR inversion for text I-Beam across light, dark, and mid-gray backgrounds
				luma := (int(r0)*77 + int(g0)*150 + int(b0)*29) >> 8
				if luma >= 84 && luma <= 172 {
					var v uint8 = 255
					if luma >= 128 {
						v = 0
					}
					dstRow[px*4] = v
					dstRow[px*4+1] = v
					dstRow[px*4+2] = v
				} else {
					dstRow[px*4] = b0 ^ cp.b
					dstRow[px*4+1] = g0 ^ cp.g
					dstRow[px*4+2] = r0 ^ cp.r
				}
				dstRow[px*4+3] = 255
			}
		}
	}
	vtblCall(c.context, 15, uintptr(c.cursorBgStaging), 0) // Unmap

	// 3. Upload blended patch to cursorPatchGPU and copy to composedTex
	patchBox := d3d11Box{left: 0, top: 0, front: 0, right: uint32(boxW), bottom: uint32(boxH), back: 1}
	vtblCall(c.context, 48, uintptr(c.cursorPatchGPU), 0, uintptr(unsafe.Pointer(&patchBox)), uintptr(unsafe.Pointer(&c.patchBuf[0])), uintptr(patchStride), 0)                                     // UpdateSubresource
	vtblCall(c.context, 46, uintptr(c.composedTex), 0, uintptr(dstX0), uintptr(dstY0), 0, uintptr(c.cursorPatchGPU), 0, uintptr(unsafe.Pointer(&patchBox))) // CopySubresourceRegion
	vtblCall(c.context, 111)                                                                                                                                // Flush GPU command queue before NVENC
	c.lastComposeDur = time.Since(t0)
}

// seedInitialDesktopGPU captures the current static desktop via GDI BitBlt into a CPU DIB
// and uploads it to lastDesktopTex via UpdateSubresource (no GDI_COMPATIBLE surface on D3D11 textures).
func (c *DXGICapturer) seedInitialDesktopGPU() bool {
	if c.lastDesktopTex == nil || c.Width <= 0 || c.Height <= 0 {
		return false
	}
	AttachToInputDesktop()
	hdcScreen, _, _ := procGetDC.Call(0)
	if hdcScreen == 0 {
		return false
	}
	defer procReleaseDC.Call(0, hdcScreen)

	memDC, _, _ := procCreateCompatibleDC.Call(hdcScreen)
	if memDC == 0 {
		return false
	}
	defer procDeleteDC.Call(memDC)

	bmi := bitmapInfoHeader{
		biSize:     uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		biWidth:    int32(c.Width),
		biHeight:   -int32(c.Height),
		biPlanes:   1,
		biBitCount: 32,
	}
	var bits *byte
	hBmp, _, _ := procCreateDIBSection.Call(memDC, uintptr(unsafe.Pointer(&bmi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hBmp == 0 || bits == nil {
		return false
	}
	defer procDeleteObject.Call(hBmp)

	oldObj, _, _ := procSelectObject.Call(memDC, hBmp)
	defer procSelectObject.Call(memDC, oldObj)

	const srccopy = 0x00CC0020
	ok, _, _ := procBitBlt.Call(memDC, 0, 0, uintptr(c.Width), uintptr(c.Height), hdcScreen, uintptr(c.outputLeft), uintptr(c.outputTop), srccopy)
	if ok == 0 {
		return false
	}
	procGdiFlush.Call()

	rowBytes := uint32(c.Width * 4)
	vtblCall(c.context, 48, uintptr(c.lastDesktopTex), 0, 0, uintptr(unsafe.Pointer(bits)), uintptr(rowBytes), 0) // UpdateSubresource
	c.hasFrame = true
	return true
}

// AcquireGPUFrame captures the next desktop frame and composites the real hardware cursor on GPU VRAM.
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
		// Treat DXGI_ERROR_ACCESS_LOST (0x887a0026), DXGI_ERROR_INVALID_CALL (0x887a0001),
		// E_ACCESSDENIED (0x80070005), DEVICE_REMOVED/RESET as recoverable reinit triggers.
		return nil, 0, 0, false, ErrAccessLost
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

// VerifyCursorDiffPixels reads back a 64x64 region around the current cursor from both lastDesktopTex
// (without cursor) and composedTex (with cursor) and counts RGB-changed pixels for ground-truth verification.
func (c *DXGICapturer) VerifyCursorDiffPixels() (curX, curY int32, diffPixels int, err error) {
	if c.lastDesktopTex == nil || c.composedTex == nil || c.cursorBgStaging == nil {
		return 0, 0, 0, errors.New("textures not initialized")
	}
	cur := c.lastCursor
	x0 := int(cur.x) - 16
	y0 := int(cur.y) - 16
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x0+64 > c.Width {
		x0 = c.Width - 64
	}
	if y0+64 > c.Height {
		y0 = c.Height - 64
	}
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	rw := 64
	if x0+rw > c.Width {
		rw = c.Width - x0
	}
	rh := 64
	if y0+rh > c.Height {
		rh = c.Height - y0
	}
	box := d3d11Box{
		left:   uint32(x0),
		top:    uint32(y0),
		front:  0,
		right:  uint32(x0 + rw),
		bottom: uint32(y0 + rh),
		back:   1,
	}

	readRegion := func(tex unsafe.Pointer) ([]byte, int, error) {
		vtblCall(c.context, 46, uintptr(c.cursorBgStaging), 0, 0, 0, 0, uintptr(tex), 0, uintptr(unsafe.Pointer(&box)))
		var mapped d3d11MappedSubresource
		if rm := uint32(vtblCall(c.context, 14, uintptr(c.cursorBgStaging), 0, uintptr(d3d11MapRead), 0, uintptr(unsafe.Pointer(&mapped)))); rm != 0 {
			return nil, 0, fmt.Errorf("Map failed: 0x%08x", rm)
		}
		pitch := int(mapped.RowPitch)
		src := unsafe.Slice(mapped.pData, pitch*rh)
		out := make([]byte, rw*rh*4)
		for y := 0; y < rh; y++ {
			copy(out[y*rw*4:(y+1)*rw*4], src[y*pitch:y*pitch+rw*4])
		}
		vtblCall(c.context, 15, uintptr(c.cursorBgStaging), 0)
		return out, rw, nil
	}

	baseBuf, _, err := readRegion(c.lastDesktopTex)
	if err != nil {
		return cur.x, cur.y, 0, err
	}
	compBuf, _, err := readRegion(c.composedTex)
	if err != nil {
		return cur.x, cur.y, 0, err
	}

	diff := 0
	for i := 0; i < rw*rh; i++ {
		o := i * 4
		if baseBuf[o] != compBuf[o] || baseBuf[o+1] != compBuf[o+1] || baseBuf[o+2] != compBuf[o+2] {
			diff++
		}
	}
	return cur.x, cur.y, diff, nil
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
