package encoder

import (
	"bytes"
	"testing"
)

func TestSPSPPSCachePrependOnIDR(t *testing.T) {
	var cache SPSPPSCache

	// First AU contains SPS(7) + PPS(8) + IDR(5)
	sps := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x64, 0x00, 0x32}
	pps := []byte{0x00, 0x00, 0x00, 0x01, 0x68, 0xEE, 0x3C, 0x80}
	idr := []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88, 0x84, 0x21}
	first := append(append(append([]byte{}, sps...), pps...), idr...)

	out1, key1 := cache.Process(first)
	if !key1 {
		t.Fatalf("expected key1=true")
	}
	if !bytes.Equal(out1, first) {
		t.Fatalf("expected first AU unchanged when SPS+PPS already present")
	}

	// Second AU is an IDR without SPS/PPS: cache must prepend SPS+PPS automatically
	out2, key2 := cache.Process(idr)
	if !key2 {
		t.Fatalf("expected key2=true")
	}
	if !bytes.Equal(out2, first) {
		t.Fatalf("expected cached SPS+PPS prepended to bare IDR: got %x want %x", out2, first)
	}

	// Third AU is a non-IDR P-frame: should not be prefixed
	pframe := []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x9A, 0x01}
	out3, key3 := cache.Process(pframe)
	if key3 {
		t.Fatalf("expected key3=false for P-frame")
	}
	if !bytes.Equal(out3, pframe) {
		t.Fatalf("expected P-frame unchanged")
	}
}
