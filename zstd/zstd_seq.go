package zstd

import (
	"unsafe"
)

const (
	// Literal copies read up to 16 bytes past the last literal.
	litSlack = 32
	// One sequence writes at most 16 bytes past the end of its match, so
	// this much spare capacity past the block limit keeps stores in bounds.
	zOutMargin = 32
)

// seqTables holds the FSE tables of the current frame in tab, at
// kindOffset; repeat mode reuses a table in place. The fourth quarter of tab
// is padding, so a state masked with seqTabMask stays in the array.
type seqTables struct {
	tab  [4 * seqTableSize]uint64
	log  [3]uint
	ok   [3]bool
	norm [256]int16
}

// readSeqHeader reads the sequences section header (RFC 8878 3.1.1.3.2.1)
// from blk[i:] and sets the tables. It returns the number of sequences and
// the index of the bitstream.
func (s *seqTables) readHeader(blk []byte, i int) (nseq, next int, err error) {
	if i >= len(blk) {
		return 0, 0, errEOF
	}
	b0 := int(blk[i])
	switch {
	case b0 == 0:
		return 0, i + 1, nil
	case b0 < 128:
		nseq = b0
		i++
	case b0 < 255:
		if i+1 >= len(blk) {
			return 0, 0, errEOF
		}
		nseq = (b0-128)<<8 + int(blk[i+1])
		i += 2
	default:
		if i+2 >= len(blk) {
			return 0, 0, errEOF
		}
		nseq = int(blk[i+1]) + int(blk[i+2])<<8 + 0x7f00
		i += 3
	}
	if i >= len(blk) {
		return 0, 0, errEOF
	}
	modes := blk[i]
	i++
	if modes&3 != 0 {
		return 0, 0, ErrCorrupt
	}
	for _, k := range [3]seqKind{seqLL, seqOF, seqML} {
		mode := modes >> (6 - 2*k) & 3
		n, err := s.set(k, mode, blk[i:])
		if err != nil {
			return 0, 0, err
		}
		i += n
	}
	return nseq, i, nil
}

func (s *seqTables) set(k seqKind, mode byte, src []byte) (int, error) {
	t := s.tab[kindOffset(k):][:seqTableSize]
	n := 0
	switch mode {
	case 0:
		switch k {
		case seqLL:
			s.log[k] = predefLLLog
			copy(t, predefLL)
		case seqML:
			s.log[k] = predefMLLog
			copy(t, predefML)
		default:
			s.log[k] = predefOFLog
			copy(t, predefOF)
		}
	case 1:
		if len(src) < 1 {
			return 0, errEOF
		}
		c := src[0]
		if int(c) > maxSymOf(k) {
			return 0, ErrCorrupt
		}
		vb, extra := codeValue(k, c)
		t[0] = seqEntry(vb, extra, 0, kindOffset(k))
		s.log[k] = 0
		n = 1
	case 2:
		log, nc, err := readNCount(src, &s.norm, maxSymOf(k), maxLogOf(k))
		if err != nil {
			return 0, err
		}
		if err := buildSeqTable(t, k, s.norm[:maxSymOf(k)+1], log); err != nil {
			return 0, err
		}
		s.log[k] = log
		n = nc
	default:
		if !s.ok[k] {
			return 0, ErrCorrupt
		}
	}
	s.ok[k] = true
	return n, nil
}

func tableAt(t unsafe.Pointer, i uint) uint64 {
	return *(*uint64)(unsafe.Add(t, i*8))
}

// seqVal is one decoded sequence: literal length, match length and offset.
type seqVal struct {
	ll, ml, off uint32
}

// decodeSeqs decodes len(seqs) sequences from the backward bitstream
// src[start:end], resolving repeat offsets.
//
// Table loads go through an unsafe pointer: every state is masked to
// seqTabMask, inside seqTables.tab.
func (d *zdec) decodeSeqs(seqs []seqVal, src []byte, start, end int) error {
	var br revReader
	if err := br.init(src, start, end); err != nil {
		return err
	}
	s := &d.seq
	tp := unsafe.Pointer(&s.tab)
	llS := uint(br.read(s.log[seqLL])) + uint(kindOffset(seqLL))
	ofS := uint(br.read(s.log[seqOF])) + uint(kindOffset(seqOF))
	mlS := uint(br.read(s.log[seqML])) + uint(kindOffset(seqML))
	br.reload()

	c, consumed, p := br.c, br.consumed, br.p
	pfast := br.start + 8
	inp := unsafe.Pointer(unsafe.SliceData(src))
	rep0, rep1, rep2 := d.rep[0], d.rep[1], d.rep[2]
	sp := unsafe.Pointer(unsafe.SliceData(seqs))
	n := len(seqs)

	if useSeqAsm {
		ctx := seqCtx{
			src: inp, p: p, plim: pfast + 16, c: c, bits: uint64(consumed),
			tab: tp, ll: uint64(llS), ml: uint64(mlS), of: uint64(ofS),
			rep:  [3]uint64{uint64(rep0), uint64(rep1), uint64(rep2)},
			seqs: sp, n: n,
		}
		decodeSeqsAsm(&ctx)
		p, c, consumed = ctx.p, ctx.c, uint(ctx.bits)
		llS, mlS, ofS = uint(ctx.ll), uint(ctx.ml), uint(ctx.of)
		rep0, rep1, rep2 = uint(ctx.rep[0]), uint(ctx.rep[1]), uint(ctx.rep[2])
		sp = unsafe.Add(sp, uintptr(ctx.i)*unsafe.Sizeof(seqVal{}))
		n -= ctx.i
	}

	for n > 0 {
		ofE := tableAt(tp, ofS&seqTabMask)
		mlE := tableAt(tp, mlS&seqTabMask)
		llE := tableAt(tp, llS&seqTabMask)
		ofAdd := uint(ofE>>32) & 0xff
		mlAdd := uint(mlE>>32) & 0xff
		llAdd := uint(llE>>32) & 0xff

		// at least 57 bits are unread here: the extra bits of the
		// offset and match length (at most 47) fit, and the literal
		// length and the state updates (at most 42) fit after a reload
		ofv := uint(uint32(ofE)) + uint((c<<(consumed&63))>>1>>((63-ofAdd)&63))
		consumed += ofAdd
		mlv := uint(uint32(mlE)) + uint((c<<(consumed&63))>>1>>((63-mlAdd)&63))
		consumed += mlAdd
		if ofAdd+mlAdd+llAdd > 31 {
			if p >= pfast {
				p -= int(consumed >> 3)
				consumed &= 7
				c = load64(inp, p)
			} else {
				br.c, br.consumed, br.p = c, consumed, p
				br.reload()
				c, consumed, p = br.c, br.consumed, br.p
			}
		}
		llv := uint(uint32(llE)) + uint((c<<(consumed&63))>>1>>((63-llAdd)&63))
		consumed += llAdd

		if ofAdd > 1 {
			rep2, rep1, rep0 = rep1, rep0, ofv
		} else {
			// repeat offsets; a zero literal length shifts the code
			if llv == 0 {
				ofv++
			}
			switch ofv {
			case 1:
			case 2:
				rep1, rep0 = rep0, rep1
			case 3:
				rep2, rep1, rep0 = rep1, rep0, rep2
			default:
				rep2, rep1, rep0 = rep1, rep0, rep0-1
			}
		}
		*(*seqVal)(sp) = seqVal{uint32(llv), uint32(mlv), uint32(rep0)}
		sp = unsafe.Add(sp, unsafe.Sizeof(seqVal{}))
		n--
		if n == 0 {
			break
		}

		nb := uint(llE>>40) & 0xff
		llS = uint(llE>>48) + uint((c<<(consumed&63))>>1>>((63-nb)&63))
		consumed += nb
		nb = uint(mlE>>40) & 0xff
		mlS = uint(mlE>>48) + uint((c<<(consumed&63))>>1>>((63-nb)&63))
		consumed += nb
		nb = uint(ofE>>40) & 0xff
		ofS = uint(ofE>>48) + uint((c<<(consumed&63))>>1>>((63-nb)&63))
		consumed += nb
		if p >= pfast {
			p -= int(consumed >> 3)
			consumed &= 7
			c = load64(inp, p)
		} else {
			br.c, br.consumed, br.p = c, consumed, p
			br.reload()
			c, consumed, p = br.c, br.consumed, br.p
		}
	}
	br.c, br.consumed, br.p = c, consumed, p
	br.reload()
	if !br.done() {
		return ErrCorrupt
	}
	d.rep = [3]uint{rep0, rep1, rep2}
	return nil
}

// execBlock executes seqs with the literals lits onto out. out must have
// zOutMargin bytes of capacity past blockEnd, and lits litSlack bytes past
// its length; frameStart is the index in out where the frame begins.
//
// The copies go through unsafe pointers: each sequence is checked against
// the literals left and blockEnd before it is copied, and each offset
// against the frame's output and window.
func execBlock(out []byte, seqs []seqVal, lits []byte, blockEnd, frameStart, window int) ([]byte, error) {
	if useExecAsm {
		o, lit, ok := execBlockAsm(out, seqs, lits, blockEnd, frameStart, window)
		if !ok {
			return out, ErrCorrupt
		}
		rest := len(lits) - lit
		if rest > blockEnd-o {
			return out, ErrCorrupt
		}
		out = out[:o+rest]
		copy(out[o:], lits[lit:])
		return out, nil
	}
	outp := unsafe.Pointer(unsafe.SliceData(out))
	o := len(out)
	litp := unsafe.Pointer(unsafe.SliceData(lits))
	litLeft := len(lits)
	win := uint(window)
	for _, sq := range seqs {
		ll, ml, offset := int(sq.ll), int(sq.ml), uint(sq.off)
		if ll > litLeft || ml > blockEnd-o-ll {
			return out, ErrCorrupt
		}
		// literals: 16 bytes at a time, up to 16 past the end
		store64(outp, o, load64(litp, 0))
		store64(outp, o+8, load64(litp, 8))
		for j := 16; j < ll; j += 16 {
			store64(outp, o+j, load64(litp, j))
			store64(outp, o+j+8, load64(litp, j+8))
		}
		o += ll
		litp = unsafe.Add(litp, ll)
		litLeft -= ll

		// offset 0 wraps to a huge value and fails too
		if offset-1 >= min(uint(o-frameStart), win) {
			return out, ErrCorrupt
		}
		m := o - int(offset)
		if offset >= 16 {
			store64(outp, o, load64(outp, m))
			store64(outp, o+8, load64(outp, m+8))
			for j := 16; j < ml; j += 16 {
				store64(outp, o+j, load64(outp, m+j))
				store64(outp, o+j+8, load64(outp, m+j+8))
			}
		} else {
			dist := int(offset)
			if dist >= 8 {
				store64(outp, o, load64(outp, m))
				store64(outp, o+8, load64(outp, m+8))
			} else {
				// repeat the last dist bytes into one 8-byte pattern
				v := load64(outp, m) & (1<<(8*uint(dist)) - 1)
				for sh := 8 * uint(dist); sh < 64; sh *= 2 {
					v |= v << sh
				}
				store64(outp, o, v)
				store64(outp, o+8, load64(outp, o+8-stride8[dist]))
			}
			st := stride16[dist]
			for j := 16; j < ml; j += 16 {
				store64(outp, o+j, load64(outp, o+j-st))
				store64(outp, o+j+8, load64(outp, o+j-st+8))
			}
		}
		o += ml
	}
	if litLeft > blockEnd-o {
		return out, ErrCorrupt
	}
	out = out[:o+litLeft]
	copy(out[o:], unsafe.Slice((*byte)(litp), litLeft))
	return out, nil
}
