package zstd

import (
	"encoding/binary"
	"errors"
	"io"
	"unsafe"
)

var (
	// ErrCorrupt reports zstd data that does not decode.
	ErrCorrupt = errors.New("zstd: corrupt data")
	// ErrHeader reports data that does not start with a zstd or skippable
	// frame header.
	ErrHeader = errors.New("zstd: invalid header")
	// ErrChecksum reports a frame whose content checksum does not match.
	ErrChecksum = errors.New("zstd: invalid checksum")

	errEOF    = io.ErrUnexpectedEOF
	errClosed = errors.New("zstd: read after Close")
)

// These helpers are copies of the ones in the gzip package, which keeps them
// unexported.

// stride8[d] is the smallest multiple of d that is at least 8: copying 8
// bytes from that far back repeats a period-d pattern without overlap.
var stride8 = [8]int{0, 8, 8, 9, 8, 10, 12, 14}

// stride16[d] is the smallest multiple of d that is at least 16, for 16-byte
// steps.
var stride16 = [16]int{0, 16, 16, 18, 16, 20, 18, 21, 16, 18, 20, 22, 24, 26, 28, 30}

// load64 and store64 skip bounds checks: the caller must keep off and off+8
// inside the allocation.
func load64(p unsafe.Pointer, off int) uint64 {
	return binary.LittleEndian.Uint64((*[8]byte)(unsafe.Add(p, off))[:])
}

func store64(p unsafe.Pointer, off int, v uint64) {
	binary.LittleEndian.PutUint64((*[8]byte)(unsafe.Add(p, off))[:], v)
}

// growOut returns b with room for n more bytes. It doubles the capacity:
// append grows large slices by only a quarter.
func growOut(b []byte, n int) []byte {
	if cap(b)-len(b) >= n {
		return b
	}
	nb := make([]byte, len(b), max(2*cap(b), len(b)+n))
	copy(nb, b)
	return nb
}
