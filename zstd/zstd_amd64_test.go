//go:build amd64 && !purego

package zstd

import (
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
