//go:build !amd64 || purego

package zstd

import "unsafe"

const useHufAsm = false

func (h *hufTable) decode4Asm(*[4]revReader, [5]int, []byte, []byte, int) int { return 0 }

type seqEncCtx struct {
	out            unsafe.Pointer
	o              int
	seqs, codes    unsafe.Pointer
	i              int
	tabs           [3]unsafe.Pointer
	states         [3]uint64
	acc, nb        uint64
	llInfo, mlInfo unsafe.Pointer
}

const useEncAsm = false

func encodeSeqsAsm(*seqEncCtx) {}
