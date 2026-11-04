//go:build amd64 && !purego

package zstd

import (
	"encoding/binary"
	"math/bits"
	"unsafe"
)

// BMI2 shifts take any register as the count; without them the Go loop
// spends instructions moving counts into CX: GOAMD64=v3 alone made it 15%
// faster.
var useSeqAsm = hasBMI2()

func hasBMI2() bool

//go:noescape
func decodeSeqsBMI2(ctx *seqCtx)

func decodeSeqsAsm(ctx *seqCtx) { decodeSeqsBMI2(ctx) }

// hufCtx carries four Huffman streams into huf4BMI2. Its layout matches the
// HUF_ offsets in zstd_amd64.s.
type hufCtx struct {
	tab    unsafe.Pointer
	src    unsafe.Pointer
	p      [4]int    // index in src of the 8 bytes each stream reloads from
	pmin   [4]int    // a stream reloads only while p >= pmin
	h      [4]uint64 // unread bits, left-aligned, above a 1 marker bit
	out    unsafe.Pointer
	seg    int
	shift  uint64 // 64 - table log
	rounds int    // in: most rounds the outputs hold; out: rounds done
}

var useHufAsm = useSeqAsm

//go:noescape
func huf4BMI2(ctx *hufCtx)

// decode4Asm decodes 5-symbol rounds of the four streams until a stream
// nears its start or the fourth output fills, and returns the number of
// symbols decoded per stream. A round reloads each stream once, stepping
// back at most 8 bytes.
func (h *hufTable) decode4Asm(br *[4]revReader, bounds [5]int, src, out []byte, seg int) int {
	rounds := (len(out) - 3*seg) / 5
	if rounds <= 0 {
		return 0
	}
	ctx := hufCtx{
		tab:    unsafe.Pointer(&h.t),
		src:    unsafe.Pointer(unsafe.SliceData(src)),
		out:    unsafe.Pointer(unsafe.SliceData(out)),
		seg:    seg,
		shift:  64 - uint64(h.log),
		rounds: rounds,
	}
	for k := range br {
		ctx.p[k] = br[k].p
		ctx.pmin[k] = bounds[k] + 8
		ctx.h[k] = (br[k].c | 1) << br[k].consumed
	}
	huf4BMI2(&ctx)
	for k := range br {
		br[k].p = ctx.p[k]
		br[k].c = binary.LittleEndian.Uint64(src[br[k].p:])
		br[k].consumed = uint(bits.TrailingZeros64(ctx.h[k]))
	}
	return ctx.rounds * 5
}

// PSHUFB is SSSE3, which every CPU with BMI2 has.
var useExecAsm = useSeqAsm

//go:noescape
func execSeqsSSE(ctx *execCtx)

func execSeqs(ctx *execCtx) { execSeqsSSE(ctx) }

//go:noescape
func encSeqsBMI2(ctx *seqEncCtx)

var useEncAsm = useSeqAsm

func encodeSeqsAsm(ctx *seqEncCtx) { encSeqsBMI2(ctx) }
