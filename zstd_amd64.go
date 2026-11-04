//go:build amd64 && !purego

package fastgzip

import (
	"encoding/binary"
	"math/bits"
	"unsafe"
)

// seqCtx carries the state of zdec.decodeSeqs into decodeSeqsBMI2. Its
// layout matches the CTX_ offsets in zstd_amd64.s.
type seqCtx struct {
	src     unsafe.Pointer
	p       int // index in src of the 8 bytes in c
	plim    int // decode while p >= plim
	c       uint64
	bits    uint64 // bits of c already read
	tab     unsafe.Pointer
	ll      uint64 // states: indexes into seqTables.tab
	ml      uint64
	of      uint64
	rep     [3]uint64
	seqs    unsafe.Pointer
	i       int // next sequence to decode
	n       int
	plimAbs uintptr // scratch for the assembly
	endAbs  uintptr
}

// BMI2 shifts take any register as the count; without them the Go loop
// spends instructions moving counts into CX: GOAMD64=v3 alone made it 15%
// faster.
var useSeqAsm = hasBMI2()

func hasBMI2() bool

//go:noescape
func decodeSeqsBMI2(ctx *seqCtx)

func decodeSeqsAsm(ctx *seqCtx) { decodeSeqsBMI2(ctx) }

// hufCtx carries four Huffman streams into huf4BMI2. Its layout matches the
// HUF_ offsets in zstd_amd64.s.
type hufCtx struct {
	tab    unsafe.Pointer
	src    unsafe.Pointer
	p      [4]int    // index in src of the 8 bytes each stream reloads from
	pmin   [4]int    // a stream reloads only while p >= pmin
	h      [4]uint64 // unread bits, left-aligned, above a 1 marker bit
	out    unsafe.Pointer
	seg    int
	shift  uint64 // 64 - table log
	rounds int    // in: most rounds the outputs hold; out: rounds done
}

var useHufAsm = useSeqAsm

//go:noescape
func huf4BMI2(ctx *hufCtx)

// decode4Asm decodes 5-symbol rounds of the four streams until a stream
// nears its start or the fourth output fills, and returns the number of
// symbols decoded per stream. A round reloads each stream once, stepping
// back at most 8 bytes.
func (h *hufTable) decode4Asm(br *[4]revReader, bounds [5]int, src, out []byte, seg int) int {
	rounds := (len(out) - 3*seg) / 5
	if rounds <= 0 {
		return 0
	}
	ctx := hufCtx{
		tab:    unsafe.Pointer(&h.t),
		src:    unsafe.Pointer(unsafe.SliceData(src)),
		out:    unsafe.Pointer(unsafe.SliceData(out)),
		seg:    seg,
		shift:  64 - uint64(h.log),
		rounds: rounds,
	}
	for k := range br {
		ctx.p[k] = br[k].p
		ctx.pmin[k] = bounds[k] + 8
		ctx.h[k] = (br[k].c | 1) << br[k].consumed
	}
	huf4BMI2(&ctx)
	for k := range br {
		br[k].p = ctx.p[k]
		br[k].c = binary.LittleEndian.Uint64(src[br[k].p:])
		br[k].consumed = uint(bits.TrailingZeros64(ctx.h[k]))
	}
	return ctx.rounds * 5
}

// execCtx carries execBlock's state into execSeqsSSE. Its layout matches the
// EX_ offsets in zstd_amd64.s.
type execCtx struct {
	out     unsafe.Pointer
	o       int // output position
	end     int // block end
	lits    unsafe.Pointer
	lit     int // next literal
	litEnd  int
	seqs    unsafe.Pointer
	n       int
	fs      int // frame start
	window  int
	masks   unsafe.Pointer // &shufMasks
	strides unsafe.Pointer // &stride16
	err     int
}

// shufMasks[d] makes PSHUFB repeat the first d of 16 bytes.
var shufMasks [16][16]byte

func init() {
	for d := 1; d < 16; d++ {
		for i := range 16 {
			shufMasks[d][i] = byte(i % d)
		}
	}
}

// PSHUFB is SSSE3, which every CPU with BMI2 has.
var useExecAsm = useSeqAsm

//go:noescape
func execSeqsSSE(ctx *execCtx)

// execBlockAsm runs the sequences of execBlock and returns the output and
// literal positions after them.
func execBlockAsm(out []byte, seqs []seqVal, lits []byte, blockEnd, frameStart, window int) (o, lit int, ok bool) {
	ctx := execCtx{
		out:     unsafe.Pointer(unsafe.SliceData(out)),
		o:       len(out),
		end:     blockEnd,
		lits:    unsafe.Pointer(unsafe.SliceData(lits)),
		litEnd:  len(lits),
		seqs:    unsafe.Pointer(unsafe.SliceData(seqs)),
		n:       len(seqs),
		fs:      frameStart,
		window:  window,
		masks:   unsafe.Pointer(&shufMasks),
		strides: unsafe.Pointer(&stride16),
	}
	execSeqsSSE(&ctx)
	return ctx.o, ctx.lit, ctx.err == 0
}

// seqEncCtx carries encodeSeqBits' state into encSeqsBMI2. Its layout
// matches the ES_ offsets in zstd_amd64.s.
type seqEncCtx struct {
	out    unsafe.Pointer
	o      int
	seqs   unsafe.Pointer
	codes  unsafe.Pointer
	i      int               // the sequence to encode next, counting down to 0
	tabs   [3]unsafe.Pointer // literal length, match length, offset *fseEnc
	states [3]uint64
	acc    uint64
	nb     uint64
	llInfo unsafe.Pointer
	mlInfo unsafe.Pointer
}

//go:noescape
func encSeqsBMI2(ctx *seqEncCtx)

func encodeSeqsAsm(ctx *seqEncCtx) { encSeqsBMI2(ctx) }
