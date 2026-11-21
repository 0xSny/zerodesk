package encoder

import (
	"time"
	"unsafe"
)

// EncodedFrame represents one compressed H.264 Access Unit (Annex-B NALUs).
type EncodedFrame struct {
	Data       []byte
	IsKeyFrame bool
	Duration   time.Duration
}

// VideoEncoder defines a hardware-agnostic video encoding interface.
// Stage 1 uses Windows MediaFoundation H.264 MFT; Stage 2 adds NVENC D3D11 zero-copy behind TextureEncoder.
type VideoEncoder interface {
	Name() string
	Init(width, height, fps, bitrateKbps int) error
	Encode(bgra []byte, stride int, forceIDR bool) (EncodedFrame, error)
	Close() error
}

// TextureEncoder is an optional zero-copy GPU interface for hardware encoders (e.g., NVENC D3D11).
type TextureEncoder interface {
	VideoEncoder
	InitD3D11(d3dDevice unsafe.Pointer, width, height, fps, bitrateKbps int) error
	EncodeTexture(d3dTex unsafe.Pointer, forceIDR bool) (EncodedFrame, error)
}

// SPSPPSCache parses Annex-B NALUs, caches the latest SPS (7) and PPS (8),
// and ensures every IDR (5) frame is prefixed with SPS+PPS so mid-stream iOS reconnects never black-screen.
type SPSPPSCache struct {
	lastSPS []byte
	lastPPS []byte
	outBuf  []byte
}

func (c *SPSPPSCache) Reset() {
	c.lastSPS = c.lastSPS[:0]
	c.lastPPS = c.lastPPS[:0]
}

// Process scans Annex-B bitstream, caches SPS/PPS, and prepends cached SPS+PPS before IDR if missing.
func (c *SPSPPSCache) Process(annexB []byte) ([]byte, bool) {
	if len(annexB) == 0 {
		return nil, false
	}

	var hasSPS, hasPPS, hasIDR bool
	type naluSpan struct {
		start int // start of start-code
		hdr   int // index of NALU header byte
		end   int
	}

	n := len(annexB)
	var spans []naluSpan
	i := 0
	for i+3 < n {
		scLen := 0
		if annexB[i] == 0 && annexB[i+1] == 0 {
			if annexB[i+2] == 1 {
				scLen = 3
			} else if annexB[i+2] == 0 && i+4 < n && annexB[i+3] == 1 {
				scLen = 4
			}
		}
		if scLen > 0 {
			if len(spans) > 0 {
				spans[len(spans)-1].end = i
			}
			spans = append(spans, naluSpan{start: i, hdr: i + scLen, end: n})
			i += scLen + 1
			continue
		}
		i++
	}

	for _, sp := range spans {
		if sp.hdr >= sp.end {
			continue
		}
		nalType := annexB[sp.hdr] & 0x1F
		switch nalType {
		case 7: // SPS
			hasSPS = true
			c.lastSPS = c.lastSPS[:0]
			c.lastSPS = append(c.lastSPS, 0, 0, 0, 1)
			c.lastSPS = append(c.lastSPS, annexB[sp.hdr:sp.end]...)
		case 8: // PPS
			hasPPS = true
			c.lastPPS = c.lastPPS[:0]
			c.lastPPS = append(c.lastPPS, 0, 0, 0, 1)
			c.lastPPS = append(c.lastPPS, annexB[sp.hdr:sp.end]...)
		case 5: // IDR
			hasIDR = true
		}
	}

	if hasIDR && (!hasSPS || !hasPPS) && len(c.lastSPS) > 0 && len(c.lastPPS) > 0 {
		needLen := len(c.lastSPS) + len(c.lastPPS) + len(annexB)
		if cap(c.outBuf) < needLen {
			c.outBuf = make([]byte, 0, needLen)
		} else {
			c.outBuf = c.outBuf[:0]
		}
		if !hasSPS {
			c.outBuf = append(c.outBuf, c.lastSPS...)
		}
		if !hasPPS {
			c.outBuf = append(c.outBuf, c.lastPPS...)
		}
		c.outBuf = append(c.outBuf, annexB...)
		out := make([]byte, len(c.outBuf))
		copy(out, c.outBuf)
		return out, true
	}

	out := make([]byte, len(annexB))
	copy(out, annexB)
	return out, hasIDR || hasSPS
}
