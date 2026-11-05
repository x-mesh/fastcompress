//go:build arm64 && !purego

package zstd

import "unsafe"

// The compiled Go loops of decodeSeqs and execBlock keep part of their state
// on the stack. The assembly keeps all of it in registers, and it uses only
// instructions that every arm64 CPU has.
var useSeqAsm = true

//go:noescape
func decodeSeqsARM64(ctx *seqCtx)

func decodeSeqsAsm(ctx *seqCtx) { decodeSeqsARM64(ctx) }

var useExecAsm = true

//go:noescape
func execSeqsARM64(ctx *execCtx)

func execSeqs(ctx *execCtx) { execSeqsARM64(ctx) }

// The compiled Go loop of encodeSeqBits reloads a table pointer from the
// stack in each sequence and makes each mask of state bits in three
// instructions. On one goroutine of an M1 Pro, Encode took 2-4% less time
// with the assembly.
var useEncAsm = true

//go:noescape
func encSeqsARM64(ctx *seqEncCtx)

func encodeSeqsAsm(ctx *seqEncCtx) { encSeqsARM64(ctx) }

// On an M1 Pro, decoding the sequences of a block takes about as long as
// executing them, and the literals wait behind both on one goroutine. A
// third goroutine for the literals made Decode 8-37% faster with the
// default GOMAXPROCS, for 2% more CPU time.
const splitLits = true

// Decoding a sequence waits on a chain of loads and shifts, and copying it
// waits on memory. In one loop the two overlap: on one goroutine of an M1
// Pro, a 100 MB log at level 1 decoded in 15% less time than with two loops.
var useFusedAsm = true

// fusedCtx carries runFused's state into fusedSeqsARM64: the bitstream as in
// seqCtx, and the output as in execCtx. Its layout matches the FS_ and FX_
// offsets in zstd_arm64.s.
type fusedCtx struct {
	s seqCtx
	x execCtx
}

//go:noescape
func fusedSeqsARM64(ctx *fusedCtx)

// runFused decodes the sequences of a compressed block and executes them
// with x in one pass, as parseSeqs and run do in two. The assembly stops near
// the start of the bitstream; the Go loops decode and execute the rest.
func (d *zdec) runFused(x *zexec, out []byte, it *zitem) ([]byte, error) {
	src := it.seqSrc
	nseq, m, err := d.seq.readHeader(src, 0)
	if err != nil {
		return out, err
	}
	if nseq == 0 {
		if m != len(src) {
			return out, ErrCorrupt
		}
		return x.run(out, it)
	}
	// every sequence copies a match of at least 3 bytes
	if m >= len(src) || nseq > zstdBlockMax/3 {
		return out, ErrCorrupt
	}
	out, room := x.room(out)
	start := len(out)
	var s seqState
	if err := d.seqInit(&s, src, m, len(src)); err != nil {
		return out, err
	}
	out, err = d.fusedSeqs(x, out, &s, nseq, it.lits, start+room)
	if err != nil {
		return out[:start], err
	}
	x.finish(out, start)
	return out, nil
}

// fusedSeqs decodes nseq sequences from s and executes them with lits onto
// out, up to blockEnd, in the frame and window of x.
func (d *zdec) fusedSeqs(x *zexec, out []byte, s *seqState, nseq int, lits []byte, blockEnd int) ([]byte, error) {
	ctx := fusedCtx{
		s: seqCtx{
			src: unsafe.Pointer(unsafe.SliceData(s.br.src)), p: s.br.p, plim: s.br.start + 24,
			c: s.br.c, bits: uint64(s.br.consumed), tab: unsafe.Pointer(&d.seq.tab),
			ll: uint64(s.ll), ml: uint64(s.ml), of: uint64(s.of),
			rep: [3]uint64{uint64(s.rep0), uint64(s.rep1), uint64(s.rep2)},
			n:   nseq,
		},
		x: execCtx{
			out:     unsafe.Pointer(unsafe.SliceData(out)),
			o:       len(out),
			end:     blockEnd,
			lits:    unsafe.Pointer(unsafe.SliceData(lits)),
			litEnd:  len(lits),
			fs:      x.frameStart,
			window:  x.window,
			masks:   unsafe.Pointer(&shufMasks),
			strides: unsafe.Pointer(&stride16),
		},
	}
	fusedSeqsARM64(&ctx)
	if ctx.x.err != 0 {
		return out, ErrCorrupt
	}
	s.br.p, s.br.c, s.br.consumed = ctx.s.p, ctx.s.c, uint(ctx.s.bits)
	s.ll, s.ml, s.of = uint(ctx.s.ll), uint(ctx.s.ml), uint(ctx.s.of)
	s.rep0, s.rep1, s.rep2 = uint(ctx.s.rep[0]), uint(ctx.s.rep[1]), uint(ctx.s.rep[2])
	seqs := d.seqs[:nseq-ctx.s.i]
	d.seqDecode(s, seqs)
	out, err := execBlock(out[:ctx.x.o], seqs, lits[ctx.x.lit:], blockEnd, x.frameStart, x.window)
	if err != nil {
		return out, err
	}
	return out, d.seqDone(s)
}
