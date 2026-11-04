//go:build amd64 && !purego

package gzip

import (
	"math/bits"
	"unsafe"
)

// fastCtx carries the state of (*decoder).fast into inflateFastBMI2. Its
// layout matches the FC_ offsets in inflate_amd64.s.
type fastCtx struct {
	in      unsafe.Pointer
	ip      int
	last    int // decode while ip <= last
	out     unsafe.Pointer
	o       int
	end     int // decode while o <= end
	base    int
	lt, dt  unsafe.Pointer
	lbits   uint64 // root bits of lt
	dbits   uint64
	hold    uint64
	bits    uint64
	masks   unsafe.Pointer // &shufMasks
	strides unsafe.Pointer // &stride16
	status  int
}

const (
	fastMargin = iota // the input or output margin ran out
	fastDone          // end of block
	fastCorrupt
)

// fastChunk bounds the output of one inflateFastBMI2 call. The runtime
// cannot preempt assembly, so a call delays a stop-the-world GC until it
// returns. At the slowest rate we measured, 2.9 ns per output byte, a 64 KB
// call takes about 0.2 ms.
const fastChunk = 64 << 10

// BMI2 shifts take any register as the count; without them the Go loop
// spends instructions moving counts into CX, and GOAMD64=v3 alone made it
// about 10% faster.
var useFastAsm = hasBMI2()

func hasBMI2() bool

//go:noescape
func inflateFastBMI2(ctx *fastCtx)

// shufMasks[d] makes PSHUFB repeat the first d of 16 bytes. PSHUFB is SSSE3,
// which every CPU with BMI2 has.
var shufMasks [16][16]byte

func init() {
	for d := 1; d < 16; d++ {
		for i := range 16 {
			shufMasks[d][i] = byte(i % d)
		}
	}
}

// fastAsm is (*decoder).fast in assembly. It writes the same output and
// leaves the same state.
func (d *decoder) fastAsm(lt, dt []uint32, lmask, dmask uint64) (done bool, err error) {
	o := len(d.out)
	ctx := fastCtx{
		in:      unsafe.Pointer(unsafe.SliceData(d.in)),
		ip:      d.ip,
		last:    len(d.in) - fastInMargin,
		out:     unsafe.Pointer(unsafe.SliceData(d.out)),
		o:       o,
		end:     min(cap(d.out)-fastOutMargin, o+fastChunk),
		base:    d.base,
		lt:      unsafe.Pointer(unsafe.SliceData(lt)),
		dt:      unsafe.Pointer(unsafe.SliceData(dt)),
		lbits:   uint64(bits.Len64(lmask)),
		dbits:   uint64(bits.Len64(dmask)),
		hold:    d.hold,
		bits:    uint64(d.bits),
		masks:   unsafe.Pointer(&shufMasks),
		strides: unsafe.Pointer(&stride16),
	}
	inflateFastBMI2(&ctx)
	if ctx.status == fastCorrupt {
		return false, ErrCorrupt
	}
	d.ip, d.hold, d.bits = ctx.ip, ctx.hold, uint(ctx.bits)
	d.out = d.out[:ctx.o]
	return ctx.status == fastDone, nil
}
