package encoder

import (
	"errors"
	"fmt"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ole32DLL                 = windows.NewLazySystemDLL("ole32.dll")
	mfplatDLL                = windows.NewLazySystemDLL("mfplat.dll")
	procCoInitializeEx       = ole32DLL.NewProc("CoInitializeEx")
	procCoUninitialize       = ole32DLL.NewProc("CoUninitialize")
	procCoCreateInstance     = ole32DLL.NewProc("CoCreateInstance")
	procMFStartup            = mfplatDLL.NewProc("MFStartup")
	procMFShutdown           = mfplatDLL.NewProc("MFShutdown")
	procMFCreateMediaType    = mfplatDLL.NewProc("MFCreateMediaType")
	procMFCreateSample       = mfplatDLL.NewProc("MFCreateSample")
	procMFCreateMemoryBuffer = mfplatDLL.NewProc("MFCreateMemoryBuffer")
)

const (
	mfVersion        uintptr = 0x00020070
	mfStartupNoSock  uintptr = 1
	coinitMulti      uintptr = 0
	clsctxInproc     uintptr = 1
	mfVideoProg      uint32  = 2 // MFVideoInterlace_Progressive
	h264ProfileBase  uint32  = 66

	mftMessageNotifyBeginStreaming uintptr = 0x10000000
	mftMessageNotifyStartOfStream  uintptr = 0x10000003
	mftMessageCommandDrain         uintptr = 0x00000001

	mfETransformNeedMoreInput uint32 = 0xC00D6D72
	mfETransformStreamChange  uint32 = 0xC00D6D61
	mftOutputProvidesSamples  uint32 = 0x00000100
	mftOutputCanProvide       uint32 = 0x00000200
)

var (
	clsidCMSH264EncoderMFT = windows.GUID{Data1: 0x6ca50344, Data2: 0x051a, Data3: 0x4ded, Data4: [8]byte{0x97, 0x79, 0xa4, 0x33, 0x05, 0x16, 0x5e, 0x35}}
	iidIMFTransform        = windows.GUID{Data1: 0xbf94c121, Data2: 0x5b05, Data3: 0x4e6f, Data4: [8]byte{0x80, 0x00, 0xba, 0x59, 0x89, 0x61, 0x41, 0x4d}}
	iidICodecAPI           = windows.GUID{Data1: 0x901db4c7, Data2: 0x31ce, Data3: 0x41a2, Data4: [8]byte{0x85, 0xdc, 0x8f, 0xa0, 0xbf, 0x41, 0xb8, 0xda}}

	mfMediaTypeVideo = windows.GUID{Data1: 0x73646976, Data2: 0x0000, Data3: 0x0010, Data4: [8]byte{0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}}
	// FCC('H264') = 0x34363248, FCC('NV12') = 0x3231564E
	mfVideoFormatH264 = windows.GUID{Data1: 0x34363248, Data2: 0x0000, Data3: 0x0010, Data4: [8]byte{0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}}
	mfVideoFormatNV12 = windows.GUID{Data1: 0x3231564E, Data2: 0x0000, Data3: 0x0010, Data4: [8]byte{0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}}

	mfMTMajorType         = windows.GUID{Data1: 0x48eba18e, Data2: 0xf8c9, Data3: 0x4687, Data4: [8]byte{0xbf, 0x11, 0x0a, 0x74, 0xc9, 0xf9, 0x6a, 0x8f}}
	mfMTSubtype           = windows.GUID{Data1: 0xf7e34c9a, Data2: 0x42e8, Data3: 0x4714, Data4: [8]byte{0xb7, 0x4b, 0xcb, 0x29, 0xd7, 0x2c, 0x35, 0xe5}}
	mfMTAvgBitrate        = windows.GUID{Data1: 0x20332624, Data2: 0xfb0d, Data3: 0x4d9e, Data4: [8]byte{0xbd, 0x0d, 0xcb, 0xf6, 0x78, 0x6c, 0x10, 0x2e}}
	mfMTInterlaceMode     = windows.GUID{Data1: 0xe2724bb8, Data2: 0xe676, Data3: 0x4806, Data4: [8]byte{0xb4, 0xb2, 0xa8, 0xd6, 0xef, 0xb4, 0x4c, 0xcd}}
	mfMTFrameSize         = windows.GUID{Data1: 0x1652c33d, Data2: 0xd6b2, Data3: 0x4012, Data4: [8]byte{0xb8, 0x34, 0x72, 0x03, 0x08, 0x49, 0xa3, 0x7d}}
	mfMTFrameRate         = windows.GUID{Data1: 0xc459a2e8, Data2: 0x3d2c, Data3: 0x4e44, Data4: [8]byte{0xb1, 0x32, 0xfe, 0xe5, 0x15, 0x6c, 0x7b, 0xb0}}
	mfMTPixelAspectRatio  = windows.GUID{Data1: 0xc6376a1e, Data2: 0x8d0a, Data3: 0x4027, Data4: [8]byte{0xbe, 0x45, 0x6d, 0x9a, 0x0a, 0xd3, 0x9b, 0xb6}}
	mfMTMPEG2Profile      = windows.GUID{Data1: 0xad76a80b, Data2: 0x2d5c, Data3: 0x4e0b, Data4: [8]byte{0xb3, 0x75, 0x64, 0xe5, 0x20, 0x13, 0x70, 0x36}}

	codecAPIRateControlMode  = windows.GUID{Data1: 0x1c0608e9, Data2: 0x370c, Data3: 0x4710, Data4: [8]byte{0x8a, 0x58, 0xcb, 0x61, 0x81, 0xc4, 0x24, 0x23}}
	codecAPILowLatency       = windows.GUID{Data1: 0x9d3ecd55, Data2: 0x89e8, Data3: 0x490a, Data4: [8]byte{0x97, 0x0a, 0x0c, 0x95, 0x48, 0xd5, 0xa5, 0x6e}}
	codecAPIQualityVsSpeed   = windows.GUID{Data1: 0x98332df8, Data2: 0x03cd, Data3: 0x476b, Data4: [8]byte{0x89, 0xfa, 0x3f, 0x9e, 0x44, 0x2d, 0xec, 0x9f}}
	codecAPIMeanBitRate      = windows.GUID{Data1: 0xf7222374, Data2: 0x2144, Data3: 0x4815, Data4: [8]byte{0xb5, 0x50, 0xa3, 0x7f, 0x8e, 0x12, 0xee, 0x52}}
	codecAPIGOPSize          = windows.GUID{Data1: 0x95f31b26, Data2: 0x95a4, Data3: 0x41aa, Data4: [8]byte{0x93, 0x03, 0x24, 0x6a, 0x7f, 0xc6, 0xee, 0xf1}}
	codecAPIForceKeyFrame    = windows.GUID{Data1: 0x398c1b98, Data2: 0x8353, Data3: 0x475a, Data4: [8]byte{0x9e, 0xf2, 0x8f, 0x26, 0x5d, 0x26, 0x03, 0x45}}
	codecAPIMaxNumRefFrame   = windows.GUID{Data1: 0x964829ed, Data2: 0x94f9, Data3: 0x43b4, Data4: [8]byte{0xb7, 0x4d, 0xef, 0x40, 0x94, 0x4b, 0x69, 0xa0}}
	codecAPIBPictureCount    = windows.GUID{Data1: 0x8d390aac, Data2: 0xdc5c, Data3: 0x4200, Data4: [8]byte{0xb5, 0x7f, 0x81, 0x4d, 0x04, 0xba, 0xba, 0xb2}}
)

type variant struct {
	vt         uint16
	wReserved1 uint16
	wReserved2 uint16
	wReserved3 uint16
	data       [16]byte
}

func variantUI4(v uint32) variant {
	var vr variant
	vr.vt = 19 // VT_UI4
	*(*uint32)(unsafe.Pointer(&vr.data[0])) = v
	return vr
}

func variantBool(b bool) variant {
	var vr variant
	vr.vt = 11 // VT_BOOL
	if b {
		*(*int16)(unsafe.Pointer(&vr.data[0])) = -1
	}
	return vr
}

type mftOutputStreamInfo struct {
	dwFlags     uint32
	cbSize      uint32
	cbAlignment uint32
}

type mftOutputDataBuffer struct {
	dwStreamID uint32
	_pad0      uint32
	pSample    unsafe.Pointer
	dwStatus   uint32
	_pad1      uint32
	pEvents    unsafe.Pointer
}

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
		vtblCall(obj, 2)
	}
}

func packU64(hi, lo uint32) uint64 {
	return (uint64(hi) << 32) | uint64(lo)
}

// MFH264Encoder implements VideoEncoder using Windows built-in MediaFoundation H.264 MFT.
type MFH264Encoder struct {
	mu           sync.Mutex
	mft          unsafe.Pointer
	codecAPI     unsafe.Pointer
	inSample     unsafe.Pointer
	inBuf        unsafe.Pointer
	width        int
	height       int
	fps          int
	bitrateKbps  int
	frameIndex   int64
	sampleDur100 int64 // 100-ns units per frame
	nv12Buf      []byte
	outBuf       []byte
	outStreamInf mftOutputStreamInfo
	mfStarted    bool
	comInited    bool
	spspps       SPSPPSCache
}

func NewMFH264Encoder() *MFH264Encoder {
	return &MFH264Encoder{}
}

func (e *MFH264Encoder) Name() string {
	return "Windows MediaFoundation H.264 MFT"
}

func (e *MFH264Encoder) Init(width, height, fps, bitrateKbps int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closeLocked()

	// Ensure even dimensions for NV12
	w := width &^ 1
	h := height &^ 1
	if w <= 0 || h <= 0 {
		return fmt.Errorf("invalid frame dimensions: %dx%d", width, height)
	}
	if fps <= 0 {
		fps = 60
	}
	if bitrateKbps <= 0 {
		bitrateKbps = 20000
	}

	hrCo, _, _ := procCoInitializeEx.Call(0, coinitMulti)
	if uint32(hrCo) == 0 || uint32(hrCo) == 1 {
		e.comInited = true
	}

	hrMF, _, _ := procMFStartup.Call(mfVersion, mfStartupNoSock)
	if uint32(hrMF) != 0 {
		e.closeLocked()
		return fmt.Errorf("MFStartup failed: 0x%08x", uint32(hrMF))
	}
	e.mfStarted = true

	var mft unsafe.Pointer
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidCMSH264EncoderMFT)),
		0,
		clsctxInproc,
		uintptr(unsafe.Pointer(&iidIMFTransform)),
		uintptr(unsafe.Pointer(&mft)),
	)
	if uint32(hr) != 0 {
		e.closeLocked()
		return fmt.Errorf("CoCreateInstance CMSH264EncoderMFT failed: 0x%08x", uint32(hr))
	}
	e.mft = mft

	// Configure low-latency encoding via ICodecAPI
	var capi unsafe.Pointer
	if uint32(vtblCall(mft, 0, uintptr(unsafe.Pointer(&iidICodecAPI)), uintptr(unsafe.Pointer(&capi)))) == 0 {
		e.codecAPI = capi
		vLow := variantBool(true)
		vtblCall(capi, 9, uintptr(unsafe.Pointer(&codecAPILowLatency)), uintptr(unsafe.Pointer(&vLow)))
		vRC := variantUI4(4) // LowDelayVBR
		if uint32(vtblCall(capi, 9, uintptr(unsafe.Pointer(&codecAPIRateControlMode)), uintptr(unsafe.Pointer(&vRC)))) != 0 {
			vRC = variantUI4(0) // CBR fallback
			vtblCall(capi, 9, uintptr(unsafe.Pointer(&codecAPIRateControlMode)), uintptr(unsafe.Pointer(&vRC)))
		}
		vBR := variantUI4(uint32(bitrateKbps * 1000))
		vtblCall(capi, 9, uintptr(unsafe.Pointer(&codecAPIMeanBitRate)), uintptr(unsafe.Pointer(&vBR)))
		vGOP := variantUI4(uint32(fps * 2))
		vtblCall(capi, 9, uintptr(unsafe.Pointer(&codecAPIGOPSize)), uintptr(unsafe.Pointer(&vGOP)))
		vSpeed := variantUI4(33)
		vtblCall(capi, 9, uintptr(unsafe.Pointer(&codecAPIQualityVsSpeed)), uintptr(unsafe.Pointer(&vSpeed)))
		vRef := variantUI4(1)
		vtblCall(capi, 9, uintptr(unsafe.Pointer(&codecAPIMaxNumRefFrame)), uintptr(unsafe.Pointer(&vRef)))
		vB := variantUI4(0)
		vtblCall(capi, 9, uintptr(unsafe.Pointer(&codecAPIBPictureCount)), uintptr(unsafe.Pointer(&vB)))
	}

	// Output MediaType MUST be set before Input MediaType on CMSH264EncoderMFT
	var outType unsafe.Pointer
	if r, _, _ := procMFCreateMediaType.Call(uintptr(unsafe.Pointer(&outType))); uint32(r) != 0 {
		e.closeLocked()
		return fmt.Errorf("MFCreateMediaType out failed: 0x%08x", uint32(r))
	}
	defer comRelease(outType)
	vtblCall(outType, 24, uintptr(unsafe.Pointer(&mfMTMajorType)), uintptr(unsafe.Pointer(&mfMediaTypeVideo)))
	vtblCall(outType, 24, uintptr(unsafe.Pointer(&mfMTSubtype)), uintptr(unsafe.Pointer(&mfVideoFormatH264)))
	vtblCall(outType, 21, uintptr(unsafe.Pointer(&mfMTAvgBitrate)), uintptr(uint32(bitrateKbps*1000)))
	vtblCall(outType, 21, uintptr(unsafe.Pointer(&mfMTInterlaceMode)), uintptr(mfVideoProg))
	vtblCall(outType, 22, uintptr(unsafe.Pointer(&mfMTFrameSize)), uintptr(packU64(uint32(w), uint32(h))))
	vtblCall(outType, 22, uintptr(unsafe.Pointer(&mfMTFrameRate)), uintptr(packU64(uint32(fps), 1)))
	vtblCall(outType, 22, uintptr(unsafe.Pointer(&mfMTPixelAspectRatio)), uintptr(packU64(1, 1)))
	vtblCall(outType, 21, uintptr(unsafe.Pointer(&mfMTMPEG2Profile)), uintptr(h264ProfileBase))

	if r := uint32(vtblCall(mft, 16, 0, uintptr(outType), 0)); r != 0 { // SetOutputType
		e.closeLocked()
		return fmt.Errorf("IMFTransform::SetOutputType failed: 0x%08x", r)
	}

	// Input MediaType (NV12)
	var inType unsafe.Pointer
	if r, _, _ := procMFCreateMediaType.Call(uintptr(unsafe.Pointer(&inType))); uint32(r) != 0 {
		e.closeLocked()
		return fmt.Errorf("MFCreateMediaType in failed: 0x%08x", uint32(r))
	}
	defer comRelease(inType)
	vtblCall(inType, 24, uintptr(unsafe.Pointer(&mfMTMajorType)), uintptr(unsafe.Pointer(&mfMediaTypeVideo)))
	vtblCall(inType, 24, uintptr(unsafe.Pointer(&mfMTSubtype)), uintptr(unsafe.Pointer(&mfVideoFormatNV12)))
	vtblCall(inType, 21, uintptr(unsafe.Pointer(&mfMTInterlaceMode)), uintptr(mfVideoProg))
	vtblCall(inType, 22, uintptr(unsafe.Pointer(&mfMTFrameSize)), uintptr(packU64(uint32(w), uint32(h))))
	vtblCall(inType, 22, uintptr(unsafe.Pointer(&mfMTFrameRate)), uintptr(packU64(uint32(fps), 1)))
	vtblCall(inType, 22, uintptr(unsafe.Pointer(&mfMTPixelAspectRatio)), uintptr(packU64(1, 1)))

	if r := uint32(vtblCall(mft, 15, 0, uintptr(inType), 0)); r != 0 { // SetInputType
		e.closeLocked()
		return fmt.Errorf("IMFTransform::SetInputType failed: 0x%08x", r)
	}

	vtblCall(mft, 7, 0, uintptr(unsafe.Pointer(&e.outStreamInf))) // GetOutputStreamInfo
	vtblCall(mft, 23, mftMessageNotifyBeginStreaming, 0)          // ProcessMessage
	vtblCall(mft, 23, mftMessageNotifyStartOfStream, 0)           // ProcessMessage

	e.width = w
	e.height = h
	e.fps = fps
	e.bitrateKbps = bitrateKbps
	e.frameIndex = 0
	e.sampleDur100 = int64(10_000_000 / fps)

	nv12Len := w * h * 3 / 2
	if cap(e.nv12Buf) < nv12Len {
		e.nv12Buf = make([]byte, nv12Len)
	} else {
		e.nv12Buf = e.nv12Buf[:nv12Len]
	}
	if cap(e.outBuf) < 512*1024 {
		e.outBuf = make([]byte, 0, 512*1024)
	}

	// Pre-allocate reusable input IMFSample + IMFMediaBuffer to avoid per-frame COM jitter
	var inSample, inBuf unsafe.Pointer
	if r, _, _ := procMFCreateSample.Call(uintptr(unsafe.Pointer(&inSample))); uint32(r) != 0 {
		e.closeLocked()
		return fmt.Errorf("MFCreateSample failed: 0x%08x", uint32(r))
	}
	if r, _, _ := procMFCreateMemoryBuffer.Call(uintptr(nv12Len), uintptr(unsafe.Pointer(&inBuf))); uint32(r) != 0 {
		comRelease(inSample)
		e.closeLocked()
		return fmt.Errorf("MFCreateMemoryBuffer failed: 0x%08x", uint32(r))
	}
	vtblCall(inSample, 42, uintptr(inBuf)) // AddBuffer
	e.inSample = inSample
	e.inBuf = inBuf
	e.spspps.Reset()
	return nil
}

// Encode converts BGRA input to NV12 and encodes one H.264 Annex-B Access Unit.
func (e *MFH264Encoder) Encode(bgra []byte, stride int, forceIDR bool) (EncodedFrame, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.mft == nil {
		return EncodedFrame{}, errors.New("encoder not initialized")
	}

	if forceIDR && e.codecAPI != nil {
		vForce := variantUI4(1)
		vtblCall(e.codecAPI, 9, uintptr(unsafe.Pointer(&codecAPIForceKeyFrame)), uintptr(unsafe.Pointer(&vForce)))
	}

	bgraToNV12(bgra, stride, e.width, e.height, e.nv12Buf)

	nv12Len := uint32(len(e.nv12Buf))
	var ptr *byte
	if r := uint32(vtblCall(e.inBuf, 3, uintptr(unsafe.Pointer(&ptr)), 0, 0)); r != 0 { // Lock
		return EncodedFrame{}, fmt.Errorf("IMFMediaBuffer::Lock failed: 0x%08x", r)
	}
	dstSlice := unsafe.Slice(ptr, nv12Len)
	copy(dstSlice, e.nv12Buf)
	vtblCall(e.inBuf, 4)                  // Unlock
	vtblCall(e.inBuf, 6, uintptr(nv12Len)) // SetCurrentLength

	ts := e.frameIndex * e.sampleDur100
	vtblCall(e.inSample, 36, uintptr(ts))            // SetSampleTime
	vtblCall(e.inSample, 38, uintptr(e.sampleDur100)) // SetSampleDuration
	e.frameIndex++

	if r := uint32(vtblCall(e.mft, 24, 0, uintptr(e.inSample), 0)); r != 0 { // ProcessInput
		return EncodedFrame{}, fmt.Errorf("IMFTransform::ProcessInput failed: 0x%08x", r)
	}

	e.outBuf = e.outBuf[:0]
	for {
		var outSample unsafe.Pointer
		provides := (e.outStreamInf.dwFlags & (mftOutputProvidesSamples | mftOutputCanProvide)) != 0
		if !provides {
			var outMediaBuf unsafe.Pointer
			sz := e.outStreamInf.cbSize
			if sz < 1024*1024 {
				sz = 1024 * 1024
			}
			if r, _, _ := procMFCreateSample.Call(uintptr(unsafe.Pointer(&outSample))); uint32(r) != 0 {
				return EncodedFrame{}, fmt.Errorf("MFCreateSample out failed: 0x%08x", uint32(r))
			}
			if r, _, _ := procMFCreateMemoryBuffer.Call(uintptr(sz), uintptr(unsafe.Pointer(&outMediaBuf))); uint32(r) != 0 {
				comRelease(outSample)
				return EncodedFrame{}, fmt.Errorf("MFCreateMemoryBuffer out failed: 0x%08x", uint32(r))
			}
			vtblCall(outSample, 42, uintptr(outMediaBuf))
			comRelease(outMediaBuf)
		}

		odb := mftOutputDataBuffer{dwStreamID: 0, pSample: outSample}
		var status uint32
		ro := uint32(vtblCall(e.mft, 25, 0, 1, uintptr(unsafe.Pointer(&odb)), uintptr(unsafe.Pointer(&status)))) // ProcessOutput
		if ro == mfETransformNeedMoreInput {
			if outSample != nil {
				comRelease(outSample)
			}
			break
		}
		if ro == mfETransformStreamChange {
			if outSample != nil {
				comRelease(outSample)
			}
			var availType unsafe.Pointer
			if uint32(vtblCall(e.mft, 14, 0, 0, uintptr(unsafe.Pointer(&availType)))) == 0 {
				vtblCall(e.mft, 16, 0, uintptr(availType), 0)
				comRelease(availType)
			}
			vtblCall(e.mft, 7, 0, uintptr(unsafe.Pointer(&e.outStreamInf)))
			continue
		}
		if ro != 0 {
			if odb.pSample != nil {
				comRelease(odb.pSample)
			}
			return EncodedFrame{}, fmt.Errorf("IMFTransform::ProcessOutput failed: 0x%08x", ro)
		}

		if odb.pSample != nil {
			var buf unsafe.Pointer
			if uint32(vtblCall(odb.pSample, 41, uintptr(unsafe.Pointer(&buf)))) == 0 && buf != nil { // ConvertToContiguousBuffer
				var pData *byte
				var curLen uint32
				if uint32(vtblCall(buf, 3, uintptr(unsafe.Pointer(&pData)), 0, uintptr(unsafe.Pointer(&curLen)))) == 0 && curLen > 0 {
					chunk := unsafe.Slice(pData, curLen)
					e.outBuf = append(e.outBuf, chunk...)
					vtblCall(buf, 4) // Unlock
				}
				comRelease(buf)
			}
			comRelease(odb.pSample)
		}
		if odb.pEvents != nil {
			comRelease(odb.pEvents)
		}
		break
	}

	processed, isKey := e.spspps.Process(e.outBuf)

	return EncodedFrame{
		Data:       processed,
		IsKeyFrame: isKey,
		Duration:   time.Second / time.Duration(e.fps),
	}, nil
}

func (e *MFH264Encoder) closeLocked() {
	if e.inBuf != nil {
		comRelease(e.inBuf)
		e.inBuf = nil
	}
	if e.inSample != nil {
		comRelease(e.inSample)
		e.inSample = nil
	}
	if e.codecAPI != nil {
		comRelease(e.codecAPI)
		e.codecAPI = nil
	}
	if e.mft != nil {
		vtblCall(e.mft, 23, mftMessageCommandDrain, 0)
		comRelease(e.mft)
		e.mft = nil
	}
	if e.mfStarted {
		procMFShutdown.Call()
		e.mfStarted = false
	}
	if e.comInited {
		procCoUninitialize.Call()
		e.comInited = false
	}
}

func (e *MFH264Encoder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closeLocked()
	return nil
}

// bgraToNV12 converts BGRA32 pixels into NV12 (Y plane + interleaved UV plane) using BT.601 integer math
// parallelized across horizontal 2-row stripes.
func bgraToNV12(bgra []byte, stride, w, h int, nv12 []byte) {
	yPlane := nv12[:w*h]
	uvPlane := nv12[w*h:]

	workers := 4
	if h < 120 {
		workers = 1
	}
	rowsPerWorker := ((h / workers) + 1) &^ 1
	var wg sync.WaitGroup
	for t := 0; t < workers; t++ {
		yStart := t * rowsPerWorker
		if yStart >= h {
			break
		}
		yEnd := yStart + rowsPerWorker
		if t == workers-1 || yEnd > h {
			yEnd = h
		}
		wg.Add(1)
		go func(ys, ye int) {
			defer wg.Done()
			for y := ys; y < ye; y += 2 {
				row0 := bgra[y*stride : y*stride+w*4]
				row1 := bgra[(y+1)*stride : (y+1)*stride+w*4]
				yOff0 := y * w
				yOff1 := (y + 1) * w
				uvOff := (y >> 1) * w

				for x := 0; x < w; x += 2 {
					i00 := x * 4
					i01 := i00 + 4

					b00, g00, r00 := int(row0[i00]), int(row0[i00+1]), int(row0[i00+2])
					b01, g01, r01 := int(row0[i01]), int(row0[i01+1]), int(row0[i01+2])
					b10, g10, r10 := int(row1[i00]), int(row1[i00+1]), int(row1[i00+2])
					b11, g11, r11 := int(row1[i01]), int(row1[i01+1]), int(row1[i01+2])

					yPlane[yOff0+x] = byte(((66*r00 + 129*g00 + 25*b00 + 128) >> 8) + 16)
					yPlane[yOff0+x+1] = byte(((66*r01 + 129*g01 + 25*b01 + 128) >> 8) + 16)
					yPlane[yOff1+x] = byte(((66*r10 + 129*g10 + 25*b10 + 128) >> 8) + 16)
					yPlane[yOff1+x+1] = byte(((66*r11 + 129*g11 + 25*b11 + 128) >> 8) + 16)

					rAvg := (r00 + r01 + r10 + r11 + 2) >> 2
					gAvg := (g00 + g01 + g10 + g11 + 2) >> 2
					bAvg := (b00 + b01 + b10 + b11 + 2) >> 2

					u := ((-38*rAvg - 74*gAvg + 112*bAvg + 128) >> 8) + 128
					v := ((112*rAvg - 94*gAvg - 18*bAvg + 128) >> 8) + 128
					if u < 0 {
						u = 0
					} else if u > 255 {
						u = 255
					}
					if v < 0 {
						v = 0
					} else if v > 255 {
						v = 255
					}
					uvPlane[uvOff+x] = byte(u)
					uvPlane[uvOff+x+1] = byte(v)
				}
			}
		}(yStart, yEnd)
	}
	wg.Wait()
}
