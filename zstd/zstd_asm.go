//go:build (amd64 || arm64) && !purego

package zstd

import "unsafe"

// seqCtx carries the state of zdec.decodeSeqs into the assembly decoder. Its
// layout matches the CTX_ offsets in zstd_amd64.s and zstd_arm64.s.
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
	plimAbs uintptr // scratch for the amd64 assembly
	endAbs  uintptr
}

// execCtx carries execBlock's state into the assembly executor. Its layout
// matches the EX_ offsets in zstd_amd64.s and zstd_arm64.s.
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

// shufMasks[d] repeats the first d of 16 bytes, as the indexes of PSHUFB on
// amd64 and of TBL on arm64.
var shufMasks [16][16]byte

func init() {
	for d := 1; d < 16; d++ {
		for i := range 16 {
			shufMasks[d][i] = byte(i % d)
		}
	}
}

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
	execSeqs(&ctx)
	return ctx.o, ctx.lit, ctx.err == 0
}
