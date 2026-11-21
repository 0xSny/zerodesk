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
	nvencDLL                      = windows.NewLazySystemDLL("nvEncodeAPI64.dll")
	procNvEncodeAPICreateInstance = nvencDLL.NewProc("NvEncodeAPICreateInstance")
)

const (
	// NVENC SDK 13.1 struct version macros (verified via MSVC x64 nvEncodeAPI.h)
	nvEncAPIVersion               uint32 = 0x0100000D
	nvEncFuncListVer              uint32 = 0x7102000D
	nvEncOpenSessionExVer         uint32 = 0x7101000D
	nvEncPresetConfigVer          uint32 = 0xF105000D
	nvEncConfigVer                uint32 = 0xF109000D
	nvEncInitParamsVer            uint32 = 0xF107000D
	nvEncCreateBitstreamBufVer    uint32 = 0x7101000D
	nvEncRegisterResourceVer      uint32 = 0x7105000D
	nvEncMapInputResourceVer      uint32 = 0x7104000D
	nvEncPicParamsVer             uint32 = 0xF107000D
	nvEncLockBitstreamVer         uint32 = 0xF102000D

	nvEncDeviceTypeDirectX          uint32 = 0
	nvEncTuningUltraLowLatency      uint32 = 3
	nvEncInputResTypeDirectX        uint32 = 0
	nvEncBufferFmtARGB              uint32 = 0x01000000 // matches DXGI_FORMAT_B8G8R8A8_UNORM
	nvEncBufferUsageInputImage      uint32 = 0
	nvEncPicStructFrame             uint32 = 1
	nvEncParamsFrameFieldModeFrame  uint32 = 1
	nvEncParamsRCCBR                uint32 = 2
	nvEncPicFlagForceIDR            uint32 = 0x2
	nvEncPicFlagOutputSPSPPS        uint32 = 0x4
)

// NV_ENCODE_API_FUNCTION_LIST byte offsets (verified against NVENC 13.1 x64)
const (
	fnOffInitializeEncoder       = 96
	fnOffCreateBitstreamBuffer   = 120
	fnOffDestroyBitstreamBuffer  = 128
	fnOffEncodePicture           = 136
	fnOffLockBitstream           = 144
	fnOffUnlockBitstream         = 152
	fnOffMapInputResource        = 208
	fnOffUnmapInputResource      = 216
	fnOffDestroyEncoder          = 224
	fnOffOpenEncodeSessionEx     = 240
	fnOffRegisterResource        = 248
	fnOffUnregisterResource      = 256
	fnOffGetEncodePresetConfigEx = 320
)

var (
	nvEncCodecH264GUID       = windows.GUID{Data1: 0x6bc82762, Data2: 0x4e63, Data3: 0x4ca4, Data4: [8]byte{0xaa, 0x85, 0x1e, 0x50, 0xf3, 0x21, 0xf6, 0xbf}}
	nvEncPresetP1GUID        = windows.GUID{Data1: 0xfc0a8d3e, Data2: 0x45f8, Data3: 0x4cf8, Data4: [8]byte{0x80, 0xc7, 0x29, 0x88, 0x71, 0x59, 0x0e, 0xbf}}
	nvEncH264ProfileHighGUID = windows.GUID{Data1: 0xe7cbc309, Data2: 0x4f7a, Data3: 0x4b89, Data4: [8]byte{0xaf, 0x2a, 0xd5, 0x37, 0xc9, 0x2b, 0xe3, 0x10}}
)

func writeU32(buf []byte, off int, v uint32) {
	*(*uint32)(unsafe.Pointer(&buf[off])) = v
}

func readU32(buf []byte, off int) uint32 {
	return *(*uint32)(unsafe.Pointer(&buf[off]))
}

func writeU64(buf []byte, off int, v uint64) {
	*(*uint64)(unsafe.Pointer(&buf[off])) = v
}

func writePtr(buf []byte, off int, p unsafe.Pointer) {
	*(*uintptr)(unsafe.Pointer(&buf[off])) = uintptr(p)
}

func readPtr(buf []byte, off int) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&buf[off]))
}

func writeGUID(buf []byte, off int, g windows.GUID) {
	*(*windows.GUID)(unsafe.Pointer(&buf[off])) = g
}

// NVENCEncoder implements zero-copy D3D11 hardware H.264 encoding via system nvEncodeAPI64.dll.
type NVENCEncoder struct {
	mu           sync.Mutex
	fnList       [2552]byte
	session      unsafe.Pointer
	bitstreamBuf unsafe.Pointer
	d3dDevice    unsafe.Pointer
	width        int
	height       int
	fps          int
	bitrateKbps  int
	frameIdx     uint32

	regTex        unsafe.Pointer
	regResHandle  unsafe.Pointer
	spspps        SPSPPSCache
}

func NewNVENCEncoder() *NVENCEncoder {
	return &NVENCEncoder{}
}

func (e *NVENCEncoder) Name() string {
	return "NVIDIA NVENC D3D11 Zero-Copy H.264"
}

func (e *NVENCEncoder) callFn(fnOffset int, args ...uintptr) uint32 {
	fn := *(*uintptr)(unsafe.Pointer(&e.fnList[fnOffset]))
	r1, _, _ := syscall.SyscallN(fn, args...)
	return uint32(r1)
}

func (e *NVENCEncoder) Init(width, height, fps, bitrateKbps int) error {
	return errors.New("NVENCEncoder requires D3D11 device via InitD3D11")
}

func (e *NVENCEncoder) InitD3D11(d3dDevice unsafe.Pointer, width, height, fps, bitrateKbps int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closeLocked()

	if d3dDevice == nil {
		return errors.New("nil D3D11 device for NVENC")
	}
	if err := nvencDLL.Load(); err != nil {
		return fmt.Errorf("load nvEncodeAPI64.dll: %w", err)
	}
	if err := procNvEncodeAPICreateInstance.Find(); err != nil {
		return fmt.Errorf("NvEncodeAPICreateInstance missing: %w", err)
	}

	w := width &^ 1
	h := height &^ 1
	if w <= 0 || h <= 0 {
		return fmt.Errorf("invalid NVENC dimensions: %dx%d", width, height)
	}
	if fps <= 0 {
		fps = 60
	}
	if bitrateKbps <= 0 {
		bitrateKbps = 20000
	}

	e.fnList = [2552]byte{}
	writeU32(e.fnList[:], 0, nvEncFuncListVer)
	if r, _, _ := procNvEncodeAPICreateInstance.Call(uintptr(unsafe.Pointer(&e.fnList[0]))); uint32(r) != 0 {
		return fmt.Errorf("NvEncodeAPICreateInstance failed: nvStatus=%d", uint32(r))
	}

	// 1. Open DirectX encode session
	var openParams [1552]byte
	writeU32(openParams[:], 0, nvEncOpenSessionExVer)
	writeU32(openParams[:], 4, nvEncDeviceTypeDirectX)
	writePtr(openParams[:], 8, d3dDevice)
	writeU32(openParams[:], 24, nvEncAPIVersion)

	var sess unsafe.Pointer
	if st := e.callFn(fnOffOpenEncodeSessionEx, uintptr(unsafe.Pointer(&openParams[0])), uintptr(unsafe.Pointer(&sess))); st != 0 {
		return fmt.Errorf("nvEncOpenEncodeSessionEx failed: nvStatus=%d", st)
	}
	e.session = sess

	// 2. Fetch P1 Ultra-Low-Latency preset config
	var presetCfg [5128]byte
	writeU32(presetCfg[:], 0, nvEncPresetConfigVer)
	writeU32(presetCfg[:], 8, nvEncConfigVer)

	codecCopy := nvEncCodecH264GUID
	presetCopy := nvEncPresetP1GUID
	if st := e.callFn(
		fnOffGetEncodePresetConfigEx,
		uintptr(sess),
		uintptr(unsafe.Pointer(&codecCopy)),
		uintptr(unsafe.Pointer(&presetCopy)),
		uintptr(nvEncTuningUltraLowLatency),
		uintptr(unsafe.Pointer(&presetCfg[0])),
	); st != 0 {
		e.closeLocked()
		return fmt.Errorf("nvEncGetEncodePresetConfigEx failed: nvStatus=%d", st)
	}

	// Copy presetCfg.presetCfg (3584 bytes starting at offset 8) into encCfg
	var encCfg [3584]byte
	copy(encCfg[:], presetCfg[8:8+3584])
	writeU32(encCfg[:], 0, nvEncConfigVer)
	writeGUID(encCfg[:], 4, nvEncH264ProfileHighGUID) // High Profile for crisp text
	writeU32(encCfg[:], 20, uint32(fps*2))            // gopLength = 2s
	writeU32(encCfg[:], 24, 1)                        // frameIntervalP = 1 (0 B-Frames!)
	writeU32(encCfg[:], 32, nvEncParamsFrameFieldModeFrame)

	// rcParams starts at offset 40 inside NV_ENC_CONFIG
	const rcOff = 40
	writeU32(encCfg[:], rcOff+4, nvEncParamsRCCBR)
	bitrateBps := uint32(bitrateKbps * 1000)
	writeU32(encCfg[:], rcOff+20, bitrateBps)
	writeU32(encCfg[:], rcOff+24, bitrateBps)
	vbv := bitrateBps / uint32(fps) * 2
	writeU32(encCfg[:], rcOff+28, vbv)
	writeU32(encCfg[:], rcOff+32, vbv)
	// zeroReorderDelay bit (0x00000200) at offset 36 of NV_ENC_RC_PARAMS
	writeU32(encCfg[:], rcOff+36, readU32(encCfg[:], rcOff+36)|0x00000200)

	// h264Config starts at offset 168 inside NV_ENC_CONFIG
	const h264Off = 168
	writeU32(encCfg[:], h264Off+8, uint32(fps*2)) // idrPeriod
	// repeatSPSPPS bit (0x00001000) at offset 0 of NV_ENC_CONFIG_H264; clear disableSPSPPS (0x80)
	h264Flags := readU32(encCfg[:], h264Off)
	h264Flags = (h264Flags &^ 0x00000080) | 0x00001000
	writeU32(encCfg[:], h264Off, h264Flags)

	// 3. Initialize Encoder
	var initParams [1800]byte
	writeU32(initParams[:], 0, nvEncInitParamsVer)
	writeGUID(initParams[:], 4, nvEncCodecH264GUID)
	writeGUID(initParams[:], 20, nvEncPresetP1GUID)
	writeU32(initParams[:], 36, uint32(w))
	writeU32(initParams[:], 40, uint32(h))
	writeU32(initParams[:], 44, uint32(w))
	writeU32(initParams[:], 48, uint32(h))
	writeU32(initParams[:], 52, uint32(fps))
	writeU32(initParams[:], 56, 1)
	writeU32(initParams[:], 60, 0) // enableEncodeAsync = 0 (sync ultra-low-latency)
	writeU32(initParams[:], 64, 1) // enablePTD = 1
	writePtr(initParams[:], 88, unsafe.Pointer(&encCfg[0]))
	writeU32(initParams[:], 96, uint32(w))
	writeU32(initParams[:], 100, uint32(h))
	writeU32(initParams[:], 136, nvEncTuningUltraLowLatency)

	if st := e.callFn(fnOffInitializeEncoder, uintptr(sess), uintptr(unsafe.Pointer(&initParams[0]))); st != 0 {
		e.closeLocked()
		return fmt.Errorf("nvEncInitializeEncoder failed: nvStatus=%d", st)
	}

	// 4. Create reusable bitstream output buffer
	var bsBufParams [776]byte
	writeU32(bsBufParams[:], 0, nvEncCreateBitstreamBufVer)
	if st := e.callFn(fnOffCreateBitstreamBuffer, uintptr(sess), uintptr(unsafe.Pointer(&bsBufParams[0]))); st != 0 {
		e.closeLocked()
		return fmt.Errorf("nvEncCreateBitstreamBuffer failed: nvStatus=%d", st)
	}
	e.bitstreamBuf = readPtr(bsBufParams[:], 16)
	e.d3dDevice = d3dDevice
	e.width = w
	e.height = h
	e.fps = fps
	e.bitrateKbps = bitrateKbps
	e.frameIdx = 0
	e.spspps.Reset()
	return nil
}

func (e *NVENCEncoder) Encode(bgra []byte, stride int, forceIDR bool) (EncodedFrame, error) {
	return EncodedFrame{}, errors.New("NVENCEncoder uses zero-copy EncodeTexture")
}

// EncodeTexture encodes an ID3D11Texture2D directly in GPU VRAM with zero CPU readback.
func (e *NVENCEncoder) EncodeTexture(d3dTex unsafe.Pointer, forceIDR bool) (EncodedFrame, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.session == nil || e.bitstreamBuf == nil || d3dTex == nil {
		return EncodedFrame{}, errors.New("nvenc encoder not initialized")
	}

	// Register ID3D11Texture2D once and cache handle across frames
	if e.regTex != d3dTex {
		if e.regResHandle != nil {
			e.callFn(fnOffUnregisterResource, uintptr(e.session), uintptr(e.regResHandle))
			e.regResHandle = nil
			e.regTex = nil
		}
		var regParams [1536]byte
		writeU32(regParams[:], 0, nvEncRegisterResourceVer)
		writeU32(regParams[:], 4, nvEncInputResTypeDirectX)
		writeU32(regParams[:], 8, uint32(e.width))
		writeU32(regParams[:], 12, uint32(e.height))
		writeU32(regParams[:], 16, 0)
		writeU32(regParams[:], 20, 0)
		writePtr(regParams[:], 24, d3dTex)
		writeU32(regParams[:], 40, nvEncBufferFmtARGB)
		writeU32(regParams[:], 44, nvEncBufferUsageInputImage)

		if st := e.callFn(fnOffRegisterResource, uintptr(e.session), uintptr(unsafe.Pointer(&regParams[0]))); st != 0 {
			return EncodedFrame{}, fmt.Errorf("nvEncRegisterResource failed: nvStatus=%d", st)
		}
		e.regResHandle = readPtr(regParams[:], 32)
		e.regTex = d3dTex
	}

	// Map resource for this frame
	var mapParams [1544]byte
	writeU32(mapParams[:], 0, nvEncMapInputResourceVer)
	writePtr(mapParams[:], 16, e.regResHandle)
	if st := e.callFn(fnOffMapInputResource, uintptr(e.session), uintptr(unsafe.Pointer(&mapParams[0]))); st != 0 {
		return EncodedFrame{}, fmt.Errorf("nvEncMapInputResource failed: nvStatus=%d", st)
	}
	mappedRes := readPtr(mapParams[:], 24)
	mappedFmt := readU32(mapParams[:], 32)
	defer e.callFn(fnOffUnmapInputResource, uintptr(e.session), uintptr(mappedRes))

	// Encode picture
	var picParams [3360]byte
	writeU32(picParams[:], 0, nvEncPicParamsVer)
	writeU32(picParams[:], 4, uint32(e.width))
	writeU32(picParams[:], 8, uint32(e.height))
	writeU32(picParams[:], 12, uint32(e.width*4))
	if forceIDR {
		writeU32(picParams[:], 16, nvEncPicFlagForceIDR|nvEncPicFlagOutputSPSPPS)
	}
	writeU32(picParams[:], 20, e.frameIdx)
	writeU64(picParams[:], 24, uint64(e.frameIdx)*uint64(10_000_000/e.fps))
	writeU64(picParams[:], 32, uint64(10_000_000/e.fps))
	writePtr(picParams[:], 40, mappedRes)
	writePtr(picParams[:], 48, e.bitstreamBuf)
	writeU32(picParams[:], 64, mappedFmt)
	writeU32(picParams[:], 68, nvEncPicStructFrame)
	e.frameIdx++

	if st := e.callFn(fnOffEncodePicture, uintptr(e.session), uintptr(unsafe.Pointer(&picParams[0]))); st != 0 {
		return EncodedFrame{}, fmt.Errorf("nvEncEncodePicture failed: nvStatus=%d", st)
	}

	// Lock bitstream & read Annex-B output
	var lockParams [1544]byte
	writeU32(lockParams[:], 0, nvEncLockBitstreamVer)
	writePtr(lockParams[:], 8, e.bitstreamBuf)
	if st := e.callFn(fnOffLockBitstream, uintptr(e.session), uintptr(unsafe.Pointer(&lockParams[0]))); st != 0 {
		return EncodedFrame{}, fmt.Errorf("nvEncLockBitstream failed: nvStatus=%d", st)
	}
	bsPtr := readPtr(lockParams[:], 56)
	bsLen := readU32(lockParams[:], 36)
	var processed []byte
	var isKey bool
	if bsPtr != nil && bsLen > 0 {
		raw := unsafe.Slice((*byte)(bsPtr), bsLen)
		processed, isKey = e.spspps.Process(raw)
	}
	e.callFn(fnOffUnlockBitstream, uintptr(e.session), uintptr(e.bitstreamBuf))

	return EncodedFrame{
		Data:       processed,
		IsKeyFrame: isKey,
		Duration:   time.Second / time.Duration(e.fps),
	}, nil
}

func (e *NVENCEncoder) closeLocked() {
	if e.session != nil {
		if e.regResHandle != nil {
			e.callFn(fnOffUnregisterResource, uintptr(e.session), uintptr(e.regResHandle))
			e.regResHandle = nil
			e.regTex = nil
		}
		if e.bitstreamBuf != nil {
			e.callFn(fnOffDestroyBitstreamBuffer, uintptr(e.session), uintptr(e.bitstreamBuf))
			e.bitstreamBuf = nil
		}
		e.callFn(fnOffDestroyEncoder, uintptr(e.session))
		e.session = nil
	}
}

func (e *NVENCEncoder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closeLocked()
	return nil
}
