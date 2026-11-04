//go:build !(amd64 || arm64) || purego

package zstd

import "unsafe"

type seqCtx struct {
	src             unsafe.Pointer
	p, plim         int
	c, bits         uint64
	tab             unsafe.Pointer
	ll, ml, of      uint64
	rep             [3]uint64
	seqs            unsafe.Pointer
	i, n            int
	plimAbs, endAbs uintptr
}

const useSeqAsm = false

func decodeSeqsAsm(*seqCtx) {}

const useExecAsm = false

func execBlockAsm([]byte, []seqVal, []byte, int, int, int) (int, int, bool) { return 0, 0, false }

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
