//go:build (amd64 || arm64) && !purego

package zstd

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
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
	if seqTabMask != 0x7ff {
		t.Errorf("seqTabMask is %#x, assembly expects 0x7ff", seqTabMask)
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

// sameDecode decodes src with the assembly and with the Go loops, on two
// goroutines and on one. On an error the output does not count.
func sameDecode(t *testing.T, name string, src []byte) {
	t.Helper()
	old := pipeMinSize
	defer func() { pipeMinSize = old }()
	for _, pipe := range []int{0, math.MaxInt} {
		pipeMinSize = pipe
		asm, aerr := Decode(src)
		restore := decodeAsmOff()
		gol, gerr := Decode(src)
		restore()
		if !errors.Is(aerr, gerr) || aerr == nil && !bytes.Equal(asm, gol) {
			t.Fatalf("%s, pipeMinSize %d: assembly %d bytes, %v; Go %d bytes, %v",
				name, pipe, len(asm), aerr, len(gol), gerr)
		}
	}
}

// The Go loops and the assembly must decode the same data and refuse the same
// data: the zstd command's files, the encoder's output, and damaged copies.
func TestAsmMatchesGo(t *testing.T) {
	if !useSeqAsm {
		t.Skip("no assembly on this CPU")
	}
	var inputs [][]byte
	for _, v := range zstdVectors(t) {
		inputs = append(inputs, v.src)
	}
	r := rand.New(rand.NewPCG(13, 14))
	for _, n := range []int{100, 5000, 200 << 10, 3 << 20} {
		data := sampleData(r, n)
		for _, level := range []int{1, 3} {
			inputs = append(inputs, Encode(nil, data, level))
		}
	}
	// A 100 KB match 2 MB back takes 21 extra bits for its offset and 16 for
	// its length, so the decoder must reload before the literal length. The
	// zeros keep the hash table entries of the first copy.
	far := make([]byte, 100<<10)
	for i := range far {
		far[i] = byte(r.Uint32())
	}
	long := append(append(bytes.Clone(far), make([]byte, 2<<20)...), far...)
	inputs = append(inputs, Encode(nil, long, 3))
	// Many matches of 2-4 KB, 2 MB back, in one block: 21 extra bits for the
	// offset and 11 for the length, where the assembly decodes.
	many := append(bytes.Clone(far), make([]byte, 2<<20)...)
	for len(many) < len(far)+(2<<20)+(256<<10) {
		at, n := r.IntN(len(far)-4096), 2048+r.IntN(2048)
		many = append(append(many, byte(r.Uint32()), byte(r.Uint32())), far[at:at+n]...)
	}
	inputs = append(inputs, Encode(nil, many, 3))
	for i, src := range inputs {
		sameDecode(t, fmt.Sprintf("input %d", i), src)
		if len(src) < 2 {
			continue
		}
		for range 20 {
			bad := bytes.Clone(src)
			for k := 1 + r.IntN(3); k > 0; k-- {
				bad[r.IntN(len(bad))] ^= byte(1 + r.IntN(255))
			}
			bad = bad[:len(bad)-r.IntN(min(len(bad), 64))]
			sameDecode(t, fmt.Sprintf("input %d damaged", i), bad)
		}
	}
}

func FuzzAsmMatchesGo(f *testing.F) {
	if !useSeqAsm {
		f.Skip("no assembly on this CPU")
	}
	// Without -fuzz, the seeds run after every test, and the read-ahead
	// goroutines that TestReaderResetMidStream leaves still read the flags
	// that decodeAsmOff writes. TestAsmMatchesGo decodes the same inputs.
	if flag.Lookup("test.fuzz").Value.String() == "" {
		f.Skip("runs only with -fuzz")
	}
	for _, v := range zstdVectors(f) {
		f.Add(v.src)
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		sameDecode(t, "fuzz input", src)
	})
}

// The assembly executor must copy and refuse sequences as the Go loop of
// execBlock does, also sequences that run past the literals, the block or the
// window.
func TestExecMatchesGo(t *testing.T) {
	if !useExecAsm {
		t.Skip("no assembly on this CPU")
	}
	r := rand.New(rand.NewPCG(15, 16))
	random := func(n, c int) []byte {
		b := make([]byte, n, max(n, c))
		for i := range b {
			b[i] = byte(r.Uint32())
		}
		return b
	}
	for k := range 5000 {
		hist := r.IntN(300)
		window := 1 + r.IntN(400)
		frameStart := r.IntN(hist + 1)
		lits := random(r.IntN(300), 0)
		lits = append(lits, make([]byte, litSlack)...)[:len(lits)]
		seqs := make([]seqVal, r.IntN(20))
		size := 0
		for i := range seqs {
			sq := seqVal{ll: uint32(r.IntN(40)), ml: uint32(3 + r.IntN(80)), off: uint32(1 + r.IntN(hist+size+40))}
			switch r.IntN(20) {
			case 0:
				sq.off = 0
			case 1:
				sq.ll = uint32(r.IntN(400))
			case 2:
				sq.off = uint32(r.IntN(1 << 20))
			}
			seqs[i] = sq
			size += int(sq.ll + sq.ml)
		}
		room := size + len(lits)
		if r.IntN(4) == 0 {
			room = r.IntN(room + 1)
		}
		// the bytes after the capacity of out must stay as they are
		base := random(hist, hist+room+zOutMargin)
		guard := random(64, 0)
		run := func() ([]byte, error) {
			buf := append(append(make([]byte, 0, cap(base)+len(guard)), base[:cap(base)]...), guard...)
			out, err := execBlock(buf[:hist:cap(base)], seqs, lits, hist+room, frameStart, window)
			if !bytes.Equal(buf[cap(base):], guard) {
				t.Fatalf("case %d: write past the capacity", k)
			}
			return out, err
		}
		got, gerr := run()
		useExecAsm = false
		want, werr := run()
		useExecAsm = true
		if !errors.Is(gerr, werr) || !bytes.Equal(got, want) {
			t.Fatalf("case %d: assembly %d bytes, %v; Go %d bytes, %v", k, len(got), gerr, len(want), werr)
		}
	}
}

// randomSeqTable fills the table of kind k from a random distribution, which
// gives the codes with the most extra bits as often as the others.
// Only the first nsym codes occur.
func randomSeqTable(t *testing.T, r *rand.Rand, s *seqTables, k seqKind, nsym int) {
	log := uint(5 + r.IntN(int(maxLogOf(k))-4))
	syms := r.Perm(nsym)[:min(1+r.IntN(nsym), 1<<log)]
	norm := make([]int16, nsym)
	for _, sym := range syms {
		norm[sym] = 1
	}
	for left := 1<<log - len(syms); left > 0; left-- {
		norm[syms[r.IntN(len(syms))]]++
	}
	for _, sym := range syms {
		if norm[sym] == 1 && r.IntN(4) == 0 {
			norm[sym] = -1
		}
	}
	if err := buildSeqTable(s.tab[kindOffset(k):][:seqTableSize], k, norm, log); err != nil {
		t.Fatal(err)
	}
	s.log[k] = log
}

// Random tables and random bits make sequences with up to 31 extra bits for
// the offset and 16 for the match length, which real files rarely have where
// the assembly decodes. The assembly must decode the same sequences.
func TestDecodeSeqsMatchesGo(t *testing.T) {
	if !useSeqAsm {
		t.Skip("no assembly on this CPU")
	}
	r := rand.New(rand.NewPCG(17, 18))
	for k := range 3000 {
		var d zdec
		for _, kind := range []seqKind{seqLL, seqOF, seqML} {
			randomSeqTable(t, r, &d.seq, kind, maxSymOf(kind)+1)
		}
		src := make([]byte, 8+r.IntN(1000))
		for i := range src {
			src[i] = byte(r.Uint32())
		}
		src[len(src)-1] |= 1
		n := 1 + r.IntN(len(src)/2)
		got, want := make([]seqVal, n), make([]seqVal, n)
		d.rep = [3]uint{1, 4, 8}
		gerr := d.decodeSeqs(got, src, 0, len(src))
		grep := d.rep
		d.rep = [3]uint{1, 4, 8}
		useSeqAsm = false
		werr := d.decodeSeqs(want, src, 0, len(src))
		useSeqAsm = true
		if gerr != werr || grep != d.rep || !slices.Equal(got, want) {
			t.Fatalf("case %d: assembly %v, rep %v; Go %v, rep %v; sequences equal %v",
				k, gerr, grep, werr, d.rep, slices.Equal(got, want))
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
	if !useEncAsm {
		t.Skip("no assembly on this CPU")
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

// The assembly encoder must write the bits of the Go loop also for lengths
// up to 128 KB and offset codes up to 30, where the offset bits need a flush
// of their own, and for single-code, predefined and new tables. Only blocks
// of 2049 sequences and more get tables of 512 states.
func TestEncodeSeqsMatchesGo(t *testing.T) {
	if !useEncAsm {
		t.Skip("no assembly on this CPU")
	}
	r := rand.New(rand.NewPCG(21, 22))
	var ea, eg zenc
	for k := range 2000 {
		n := 2 + r.IntN(1000)
		if k%4 == 0 {
			n = 2049 + r.IntN(4000)
		}
		seqs := make([]encSeq, n)
		lb, mb, ob := r.IntN(18), r.IntN(18), r.IntN(31)
		for i := range seqs {
			seqs[i] = encSeq{
				ll:  uint32(r.IntN(1 << lb)),
				ml:  3 + uint32(r.IntN(1<<mb)),
				off: 1 + uint32(r.IntN(1<<ob)),
			}
		}
		ea.reset(1)
		eg.reset(1)
		withAsm := ea.sequences(nil, slices.Clone(seqs))
		useEncAsm = false
		withGo := eg.sequences(nil, slices.Clone(seqs))
		useEncAsm = true
		if !bytes.Equal(withAsm, withGo) {
			t.Fatalf("case %d: %d sequences, %d, %d and %d bits: assembly and Go encodings differ",
				k, len(seqs), lb, mb, ob)
		}
	}
}
