//go:build amd64 && !purego

package zstd

import (
	"bytes"
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

func TestSeqEncCtxLayout(t *testing.T) {
	var c seqEncCtx
	var f fseEnc
	for _, x := range []struct {
		name     string
		got, asm uintptr
	}{
		{"out", unsafe.Offsetof(c.out), 0},
		{"o", unsafe.Offsetof(c.o), 8},
		{"seqs", unsafe.Offsetof(c.seqs), 16},
		{"codes", unsafe.Offsetof(c.codes), 24},
		{"i", unsafe.Offsetof(c.i), 32},
		{"tabs", unsafe.Offsetof(c.tabs), 40},
		{"states", unsafe.Offsetof(c.states), 64},
		{"acc", unsafe.Offsetof(c.acc), 88},
		{"nb", unsafe.Offsetof(c.nb), 96},
		{"llInfo", unsafe.Offsetof(c.llInfo), 104},
		{"mlInfo", unsafe.Offsetof(c.mlInfo), 112},
		{"fseEnc.state", unsafe.Offsetof(f.state), 8},
		{"fseEnc.tt", unsafe.Offsetof(f.tt), 1032},
		{"encSeq", unsafe.Sizeof(encSeq{}), 12},
	} {
		if x.got != x.asm {
			t.Errorf("%s at %d, assembly expects %d", x.name, x.got, x.asm)
		}
	}
}

// The assembly sequence encoder writes the same bits as the Go loop.
func TestEncAsmMatchesGo(t *testing.T) {
	if !useSeqAsm {
		t.Skip("no BMI2")
	}
	r := rand.New(rand.NewPCG(11, 12))
	data := sampleData(r, 3<<20)
	for _, level := range []int{1, 3} {
		withAsm := Encode(nil, data, level)
		useEncAsm = false
		withGo := Encode(nil, data, level)
		useEncAsm = true
		if !bytes.Equal(withAsm, withGo) {
			t.Errorf("level %d: assembly and Go encodings differ", level)
		}
	}
}
