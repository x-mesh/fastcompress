//go:build amd64 && !purego

package fastgzip

import (
	"bytes"
	"math/rand/v2"
	"testing"
	"unsafe"
)

func TestSeqCtxLayout(t *testing.T) {
	var c seqCtx
	for _, f := range []struct {
		name     string
		got, asm uintptr
	}{
		{"src", unsafe.Offsetof(c.src), 0},
		{"p", unsafe.Offsetof(c.p), 8},
		{"plim", unsafe.Offsetof(c.plim), 16},
		{"c", unsafe.Offsetof(c.c), 24},
		{"bits", unsafe.Offsetof(c.bits), 32},
		{"tab", unsafe.Offsetof(c.tab), 40},
		{"ll", unsafe.Offsetof(c.ll), 48},
		{"ml", unsafe.Offsetof(c.ml), 56},
		{"of", unsafe.Offsetof(c.of), 64},
		{"rep", unsafe.Offsetof(c.rep), 72},
		{"seqs", unsafe.Offsetof(c.seqs), 96},
		{"i", unsafe.Offsetof(c.i), 104},
		{"n", unsafe.Offsetof(c.n), 112},
		{"plimAbs", unsafe.Offsetof(c.plimAbs), 120},
		{"endAbs", unsafe.Offsetof(c.endAbs), 128},
	} {
		if f.got != f.asm {
			t.Errorf("seqCtx.%s at %d, assembly expects %d", f.name, f.got, f.asm)
		}
	}
	if unsafe.Sizeof(seqVal{}) != 12 {
		t.Errorf("seqVal is %d bytes, assembly expects 12", unsafe.Sizeof(seqVal{}))
	}
}

// The Go loops and the assembly must decode the same data.
func TestZstdAsmMatchesGo(t *testing.T) {
	if !useSeqAsm {
		t.Skip("no BMI2")
	}
	vs := zstdVectors(t)
	run := func() map[string]string {
		m := map[string]string{}
		for _, v := range vs {
			out, err := Unzstd(v.src)
			m[v.name] = sha256Hex(out)
			if err != nil {
				m[v.name] = err.Error()
			}
		}
		return m
	}
	withAsm := run()
	useSeqAsm, useHufAsm, useExecAsm = false, false, false
	defer func() { useSeqAsm, useHufAsm, useExecAsm = true, true, true }()
	withGo := run()
	for k, v := range withAsm {
		if withGo[k] != v {
			t.Errorf("%s: assembly %s, Go %s", k, v, withGo[k])
		}
	}
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

func TestExecCtxLayout(t *testing.T) {
	var c execCtx
	for _, f := range []struct {
		name     string
		got, asm uintptr
	}{
		{"out", unsafe.Offsetof(c.out), 0},
		{"o", unsafe.Offsetof(c.o), 8},
		{"end", unsafe.Offsetof(c.end), 16},
		{"lits", unsafe.Offsetof(c.lits), 24},
		{"lit", unsafe.Offsetof(c.lit), 32},
		{"litEnd", unsafe.Offsetof(c.litEnd), 40},
		{"seqs", unsafe.Offsetof(c.seqs), 48},
		{"n", unsafe.Offsetof(c.n), 56},
		{"fs", unsafe.Offsetof(c.fs), 64},
		{"window", unsafe.Offsetof(c.window), 72},
		{"masks", unsafe.Offsetof(c.masks), 80},
		{"strides", unsafe.Offsetof(c.strides), 88},
		{"err", unsafe.Offsetof(c.err), 96},
	} {
		if f.got != f.asm {
			t.Errorf("execCtx.%s at %d, assembly expects %d", f.name, f.got, f.asm)
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
func TestZstdEncAsmMatchesGo(t *testing.T) {
	if !useSeqAsm {
		t.Skip("no BMI2")
	}
	r := rand.New(rand.NewPCG(11, 12))
	data := sampleData(r, 3<<20)
	for _, level := range []int{1, 3} {
		withAsm := Zstd(nil, data, level)
		useSeqAsm = false
		withGo := Zstd(nil, data, level)
		useSeqAsm = true
		if !bytes.Equal(withAsm, withGo) {
			t.Errorf("level %d: assembly and Go encodings differ", level)
		}
	}
}
