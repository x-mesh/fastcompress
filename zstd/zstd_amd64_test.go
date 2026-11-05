//go:build amd64 && !purego

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
	useSeqAsm, useHufAsm, useExecAsm = false, false, false
	return func() { useSeqAsm, useHufAsm, useExecAsm = true, true, true }
}

func TestHufCtxLayout(t *testing.T) {
	var c hufCtx
	for _, f := range []struct {
		name     string
		got, asm uintptr
	}{
		{"tab", unsafe.Offsetof(c.tab), 0},
		{"src", unsafe.Offsetof(c.src), 8},
		{"p", unsafe.Offsetof(c.p), 16},
		{"pmin", unsafe.Offsetof(c.pmin), 48},
		{"h", unsafe.Offsetof(c.h), 80},
		{"out", unsafe.Offsetof(c.out), 112},
		{"seg", unsafe.Offsetof(c.seg), 120},
		{"shift", unsafe.Offsetof(c.shift), 128},
		{"rounds", unsafe.Offsetof(c.rounds), 136},
	} {
		if f.got != f.asm {
			t.Errorf("hufCtx.%s at %d, assembly expects %d", f.name, f.got, f.asm)
		}
	}
}

// The assembly Huffman decoder must decode and refuse the four streams of a
// literals section as the Go loop does: sections of the encoder with few or
// many symbols and skewed or flat counts, for table logs up to 11, and
// damaged copies of them.
func TestHufAsmMatchesGo(t *testing.T) {
	if !useHufAsm {
		t.Skip("no assembly on this CPU")
	}
	r := rand.New(rand.NewPCG(23, 24))
	var e zenc
	e.reset(1)
	h := frameHeader{window: 1 << 20}
	lit := make([]byte, zstdBlockMax+litSlack)
	decode := func(sec []byte) ([]byte, error) {
		var d zdec
		d.startLits(&h)
		out, _, err := d.literals(sec, lit)
		return bytes.Clone(out), err
	}
	four := 0
	for k := range 400 {
		data := make([]byte, 256+r.IntN(32<<10))
		nsym, skew := 2+r.IntN(255), r.IntN(4)
		for i := range data {
			s := r.IntN(nsym)
			for range skew {
				s = min(s, r.IntN(nsym))
			}
			data[i] = byte(s)
		}
		sec := e.literals(nil, data)
		if sec[0]&3 != 2 || sec[0]>>2&3 == 0 {
			continue
		}
		four++
		got, err := decode(sec)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("case %d: %d bytes of %d symbols: %v", k, len(data), nsym, err)
		}
		for range 4 {
			bad := bytes.Clone(sec)
			for n := 1 + r.IntN(3); n > 0; n-- {
				bad[r.IntN(len(bad))] ^= byte(1 + r.IntN(255))
			}
			asm, aerr := decode(bad)
			useHufAsm = false
			gol, gerr := decode(bad)
			useHufAsm = true
			if !errors.Is(aerr, gerr) || aerr == nil && !bytes.Equal(asm, gol) {
				t.Fatalf("case %d damaged: assembly %d bytes, %v; Go %d bytes, %v",
					k, len(asm), aerr, len(gol), gerr)
			}
		}
	}
	if four < 250 {
		t.Errorf("only %d of the sections have four streams", four)
	}
}
