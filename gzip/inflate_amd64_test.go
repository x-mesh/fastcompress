//go:build amd64 && !purego

package gzip

import (
	"bytes"
	"compress/flate"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
	"unsafe"
)

func TestFastCtxLayout(t *testing.T) {
	var c fastCtx
	for _, f := range []struct {
		name     string
		got, asm uintptr
	}{
		{"in", unsafe.Offsetof(c.in), 0},
		{"ip", unsafe.Offsetof(c.ip), 8},
		{"last", unsafe.Offsetof(c.last), 16},
		{"out", unsafe.Offsetof(c.out), 24},
		{"o", unsafe.Offsetof(c.o), 32},
		{"end", unsafe.Offsetof(c.end), 40},
		{"base", unsafe.Offsetof(c.base), 48},
		{"lt", unsafe.Offsetof(c.lt), 56},
		{"dt", unsafe.Offsetof(c.dt), 64},
		{"lbits", unsafe.Offsetof(c.lbits), 72},
		{"dbits", unsafe.Offsetof(c.dbits), 80},
		{"hold", unsafe.Offsetof(c.hold), 88},
		{"bits", unsafe.Offsetof(c.bits), 96},
		{"masks", unsafe.Offsetof(c.masks), 104},
		{"strides", unsafe.Offsetof(c.strides), 112},
		{"status", unsafe.Offsetof(c.status), 120},
	} {
		if f.got != f.asm {
			t.Errorf("fastCtx.%s at %d, assembly expects %d", f.name, f.got, f.asm)
		}
	}
	if unsafe.Sizeof(stride16[0]) != 8 {
		t.Errorf("stride16 entries are %d bytes, assembly expects 8", unsafe.Sizeof(stride16[0]))
	}
}

// inflateResult is what a decoder reports for one input.
type inflateResult struct {
	out  []byte
	used int
	err  error
}

func inflateBoth(t *testing.T, src []byte) (asm, gol inflateResult) {
	t.Helper()
	asm.out, asm.used, asm.err = Inflate(nil, src)
	useFastAsm = false
	defer func() { useFastAsm = true }()
	gol.out, gol.used, gol.err = Inflate(nil, src)
	return asm, gol
}

// sameAsGo fails if the assembly and the Go loop decode src to different
// output, consume different input, or return different errors.
func sameAsGo(t *testing.T, name string, src []byte) inflateResult {
	t.Helper()
	asm, gol := inflateBoth(t, src)
	same := bytes.Equal(asm.out, gol.out) && asm.used == gol.used
	if asm.err != nil {
		// the assembly hands back its output every fastChunk, so before an
		// error it can return more of it
		same = bytes.HasPrefix(asm.out, gol.out)
	}
	if !errors.Is(asm.err, gol.err) || !same {
		t.Fatalf("%s: assembly %d bytes, used %d, %v; Go %d bytes, used %d, %v",
			name, len(asm.out), asm.used, asm.err, len(gol.out), gol.used, gol.err)
	}
	return asm
}

func TestFastAsmMatchesGo(t *testing.T) {
	if !useFastAsm {
		t.Skip("no BMI2")
	}
	r := rand.New(rand.NewPCG(21, 22))
	var inputs [][]byte
	for _, n := range []int{0, 1, 30, 400, 5000, 70000, 1 << 20} {
		data := sampleData(r, n)
		for _, lv := range []int{flate.HuffmanOnly, 1, 4, 6, 9} {
			var buf bytes.Buffer
			w, _ := flate.NewWriter(&buf, lv)
			w.Write(data)
			w.Close()
			inputs = append(inputs, buf.Bytes())
		}
		for _, lv := range []int{1, 6, 9} {
			comp, err := Deflate(nil, data, lv)
			if err != nil {
				t.Fatal(err)
			}
			inputs = append(inputs, comp)
		}
	}
	for i, src := range inputs {
		if asm := sameAsGo(t, fmt.Sprintf("input %d", i), src); asm.err != nil {
			t.Fatalf("input %d: %v", i, asm.err)
		}
		if len(src) < 2 {
			continue
		}
		// damaged copies take the error paths at random places
		for range 20 {
			bad := bytes.Clone(src)
			for k := 1 + r.IntN(3); k > 0; k-- {
				bad[r.IntN(len(bad))] ^= byte(1 + r.IntN(255))
			}
			bad = bad[:len(bad)-r.IntN(min(len(bad), 64))]
			sameAsGo(t, fmt.Sprintf("input %d damaged", i), bad)
		}
	}
}

func FuzzFastAsm(f *testing.F) {
	if !useFastAsm {
		f.Skip("no BMI2")
	}
	r := rand.New(rand.NewPCG(27, 28))
	for _, lv := range []int{flate.HuffmanOnly, 1, 6, 9} {
		var buf bytes.Buffer
		w, _ := flate.NewWriter(&buf, lv)
		w.Write(sampleData(r, 5000))
		w.Close()
		f.Add(buf.Bytes())
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		sameAsGo(t, "fuzz input", src)
	})
}

// A Reader decodes into a bounded buffer, so the fast loop stops at the
// output margin often.
func TestFastAsmReader(t *testing.T) {
	if !useFastAsm {
		t.Skip("no BMI2")
	}
	r := rand.New(rand.NewPCG(23, 24))
	data := sampleData(r, 3<<20)
	for _, lv := range []int{1, 6, 9} {
		src := gzipBytes(t, data, lv)
		for _, n := range []int{1, 1000, 64 << 10} {
			z, err := NewReader(bytes.NewReader(src))
			if err != nil {
				t.Fatal(err)
			}
			got, err := readWith(z, n)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("level %d, reads of %d: %d bytes, %v", lv, n, len(got), err)
			}
		}
	}
}
