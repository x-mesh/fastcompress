//go:build arm64 && !purego

package zstd

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"testing"
	"unsafe"
)

// decodeAsmOff turns off the decoding assembly until the returned function
// runs.
func decodeAsmOff() func() {
	useSeqAsm, useExecAsm, useFusedAsm = false, false, false
	return func() { useSeqAsm, useExecAsm, useFusedAsm = true, true, true }
}

func TestFusedCtxLayout(t *testing.T) {
	var c fusedCtx
	if unsafe.Sizeof(c.s) != 136 || unsafe.Offsetof(c.x) != 136 {
		t.Errorf("fusedCtx.s is %d bytes and x at %d, assembly expects 136 and 136",
			unsafe.Sizeof(c.s), unsafe.Offsetof(c.x))
	}
}

// The fused loop must decode and copy as the Go loops do. Random tables and
// bits give sequences with up to 21 extra bits for the offset and 16 for the
// match length. The 4 MB of history hold every such offset and the output
// may run 2 MB past it, so that many sequences copy before one fails, and
// the bytes of the sequences before it must be the same. Random bits rarely
// end where the stream ends, so the output counts also when only that check
// fails.
func TestFusedMatchesGo(t *testing.T) {
	r := rand.New(rand.NewPCG(19, 20))
	const hist, room = 4 << 20, 2 << 20
	buf := make([]byte, hist, hist+room+zOutMargin)
	for i := range buf {
		buf[i] = byte(r.Uint32())
	}
	copied := 0
	for k := range 400 {
		var d zdec
		d.seqs = make([]seqVal, zstdBlockMax/3)
		d.x.window = 1 << 22
		randomSeqTable(t, r, &d.seq, seqLL, 1+r.IntN(26))
		randomSeqTable(t, r, &d.seq, seqML, 1+r.IntN(maxMLSym+1))
		randomSeqTable(t, r, &d.seq, seqOF, 1+r.IntN(22))
		src := make([]byte, 8+r.IntN(2000))
		for i := range src {
			src[i] = byte(r.Uint32())
		}
		src[len(src)-1] |= 1
		lits := make([]byte, r.IntN(64<<10), (64<<10)+litSlack)
		for i := range lits {
			lits[i] = byte(r.Uint32())
		}
		n := 1 + r.IntN(min(len(src)/2, zstdBlockMax/3))

		restore := decodeAsmOff()
		d.rep = [3]uint{1, 4, 8}
		seqs := make([]seqVal, n)
		derr := d.decodeSeqs(seqs, src, 0, len(src))
		wrep := d.rep
		restore()
		end := hist + 1 + r.IntN(room)
		// the Go loops execute the last sequences and the literals after
		// them, so a third of the blocks end at the last byte of the
		// output or one byte before it
		size := blockOut(seqs, len(lits))
		if k%3 == 0 && size > 1 && size <= room {
			end = hist + size - r.IntN(2)
		}
		stop := failAt(seqs, len(lits), hist, end, d.x.window)

		var s seqState
		d.rep = [3]uint{1, 4, 8}
		if err := d.seqInit(&s, src, 0, len(src)); err != nil {
			t.Fatal(err)
		}
		out, gerr := d.fusedSeqs(buf[:hist], &s, n, lits, end)
		got := bytes.Clone(out[hist:])
		before := bytes.Clone(buf[hist:stop])
		grep := d.rep

		restore = decodeAsmOff()
		out, xerr := execBlock(buf[:hist], seqs, lits, end, 0, d.x.window)
		restore()
		if !bytes.Equal(before, buf[hist:stop]) {
			t.Fatalf("case %d: the output before position %d differs", k, stop-hist)
		}
		if xerr != nil {
			// random bits also fail the check at the end of the stream,
			// so the length of the output shows where the fused loop
			// stopped
			if gerr == nil || len(got) == size {
				t.Fatalf("case %d: fused accepts what execBlock refuses at %d", k, stop-hist)
			}
			continue
		}
		copied++
		if !errors.Is(gerr, derr) || !bytes.Equal(got, out[hist:]) || grep != wrep {
			t.Fatalf("case %d: fused %d bytes, %v, rep %v; Go %d bytes, %v, rep %v",
				k, len(got), gerr, grep, len(out)-hist, derr, wrep)
		}
	}
	if copied < 100 {
		t.Errorf("only %d of the cases copied every sequence", copied)
	}
}

// blockOut returns the number of bytes that seqs and nlits literals write,
// or -1 if the sequences take more literals than there are.
func blockOut(seqs []seqVal, nlits int) int {
	n := 0
	for _, sq := range seqs {
		n += int(sq.ll) + int(sq.ml)
		nlits -= int(sq.ll)
	}
	if nlits < 0 {
		return -1
	}
	return n + nlits
}

// failAt returns the output position of the first sequence that execBlock
// refuses, from o on, or the position after the last one.
func failAt(seqs []seqVal, nlits, o, end, window int) int {
	for _, sq := range seqs {
		ll, ml, off := int(sq.ll), int(sq.ml), uint(sq.off)
		if ll > nlits || ml > end-o-ll || off-1 >= min(uint(o+ll), uint(window)) {
			return o
		}
		nlits -= ll
		o += ll + ml
	}
	return o
}
