//go:build arm64 && !purego

#include "textflag.h"

// Offsets in seqCtx; zstd_asm_test.go checks them.
#define CTX_SRC 0
#define CTX_P 8
#define CTX_PLIM 16
#define CTX_C 24
#define CTX_BITS 32
#define CTX_TAB 40
#define CTX_LL 48
#define CTX_ML 56
#define CTX_OF 64
#define CTX_REP0 72
#define CTX_REP1 80
#define CTX_REP2 88
#define CTX_SEQS 96
#define CTX_I 104
#define CTX_N 112

// READ adds the next n bits of the stream to dst. R4 holds the 8 bytes at the
// read position and R5 the number of their bits not yet read, counting from
// the top, so the next n bits are R4 >> (R5-n) without the bits from n up.
// R15 holds -1. R0 and R26 are scratch.
#define READ(n, dst) \
	SUB n, R5, R5     \
	LSR R5, R4, R26   \
	LSL n, R15, R0    \
	BIC R0, R26, R26  \
	ADD R26, dst, dst

// RELOAD steps the read position back over the whole bytes read and loads
// the 8 bytes there, leaving at most 7 bits read.
#define RELOAD \
	NEG  R5, R26        \
	ADD  $64, R26, R26  \
	LSR  $3, R26, R26   \
	SUB  R26, R2, R2    \
	ADD  R26<<3, R5, R5 \
	MOVD (R2), R4

// SEQ_HEAD loads the table entries of the three states into R22/R23/R24,
// reads the offset value into R20 and the match length into R21 (at most 54
// bits after a reload), and compares the bits left with what the literal
// length and the next states may take: its extra bits, left in R1, plus 26.
// The caller reloads if the flags say LO. Unlike on amd64, a reload that is
// not needed costs more than the test, because the next read waits for its
// load.
#define SEQ_HEAD \
	AND   $0x7ff, R7, R7   \
	AND   $0x7ff, R8, R8   \
	AND   $0x7ff, R9, R9   \
	MOVD  (R6)(R9<<3), R22 \
	MOVD  (R6)(R8<<3), R23 \
	MOVD  (R6)(R7<<3), R24 \
	UBFX  $32, R22, $8, R1 \
	MOVWU R22, R20         \
	READ(R1, R20)          \
	UBFX  $32, R23, $8, R1 \
	MOVWU R23, R21         \
	READ(R1, R21)          \
	UBFX  $32, R24, $8, R1 \
	ADD   $26, R1, R26     \
	CMP   R26, R5

// SEQ_TAIL reads the literal length into R25 and sets the repeat offsets
// R10-R12; R10 is then the offset of the sequence. Offset values 1-3 are
// repeat offsets; a zero literal length shifts them by one, and 4 is rep0-1.
// Repeat and new offsets mix without a pattern, so the new rep0-2 come from
// CSEL, not from branches: R17 is the shifted value, and R0, R26 and R16 the
// new rep0, rep1 and rep2 of a repeat offset.
#define SEQ_TAIL \
	MOVWU R24, R25          \
	READ(R1, R25)           \
	CMP   $0, R25           \
	CSINC NE, R20, R20, R17 \
	SUB   $1, R10, R16      \
	CMP   $2, R17           \
	CSEL  EQ, R11, R10, R0  \
	CMP   $3, R17           \
	CSEL  EQ, R12, R0, R0   \
	CMP   $4, R17           \
	CSEL  EQ, R16, R0, R0   \
	CMP   $1, R17           \
	CSEL  EQ, R11, R10, R26 \
	CMP   $3, R17           \
	CSEL  HS, R11, R12, R16 \
	UBFX  $32, R22, $8, R1  \
	CMP   $1, R1            \
	CSEL  HI, R11, R16, R12 \
	CSEL  HI, R10, R26, R11 \
	CSEL  HI, R20, R0, R10

// SEQ_STATES reads the next three states, at most 42 bits after the reload
// in or before SEQ_HEAD, and reloads.
#define SEQ_STATES \
	UBFX $40, R24, $8, R1 \
	LSR  $48, R24, R7     \
	READ(R1, R7)          \
	UBFX $40, R23, $8, R1 \
	LSR  $48, R23, R8     \
	READ(R1, R8)          \
	UBFX $40, R22, $8, R1 \
	LSR  $48, R22, R9     \
	READ(R1, R9)          \
	RELOAD

// func decodeSeqsARM64(ctx *seqCtx)
//
// decodeSeqsARM64 is the loop of zdec.decodeSeqs, for the part of the stream
// at least 24 bytes from its start: each sequence reloads twice and each
// reload steps back at most 8 bytes, so the 8-byte loads stay in the stream.
// It returns at the start of a sequence, with ctx updated, when the stream
// start is near or every sequence is decoded.
//
// Registers: R2 read position, R3 its limit, R4 bits, R5 bits left, R6
// tables, R7/R8/R9 literal length/match length/offset state, R10-R12 rep0-2,
// R13 next seqVal, R14 seqVal end, R15 -1, R20/R21/R25 offset/match
// length/literal length, R22/R23/R24 their table entries. R0, R1, R16, R17
// and R26 are scratch; ctx is loaded again at the end.
TEXT ·decodeSeqsARM64(SB), NOSPLIT, $0-8
	MOVD ctx+0(FP), R0
	MOVD CTX_SRC(R0), R2
	MOVD CTX_PLIM(R0), R3
	ADD  R2, R3, R3
	MOVD CTX_P(R0), R1
	ADD  R1, R2, R2
	MOVD CTX_C(R0), R4
	MOVD CTX_BITS(R0), R1
	MOVD $64, R5
	SUB  R1, R5, R5
	MOVD CTX_TAB(R0), R6
	MOVD CTX_LL(R0), R7
	MOVD CTX_ML(R0), R8
	MOVD CTX_OF(R0), R9
	MOVD CTX_REP0(R0), R10
	MOVD CTX_REP1(R0), R11
	MOVD CTX_REP2(R0), R12
	MOVD CTX_SEQS(R0), R13
	MOVD $12, R26
	MOVD CTX_N(R0), R1
	MUL  R26, R1, R14
	ADD  R13, R14, R14
	MOVD CTX_I(R0), R1
	MUL  R26, R1, R1
	ADD  R1, R13, R13
	MOVD $-1, R15

loop:
	CMP R3, R2
	BLO exit
	CMP R14, R13
	BHS exit
	SEQ_HEAD
	BHS llread
	RELOAD

llread:
	SEQ_TAIL
	MOVW R25, (R13)
	MOVW R21, 4(R13)
	MOVW R10, 8(R13)
	ADD  $12, R13, R13
	CMP  R14, R13
	BHS  exit
	SEQ_STATES
	B    loop

exit:
	MOVD ctx+0(FP), R0
	MOVD CTX_SRC(R0), R1
	SUB  R1, R2, R1
	MOVD R1, CTX_P(R0)
	MOVD R4, CTX_C(R0)
	MOVD $64, R1
	SUB  R5, R1, R1
	MOVD R1, CTX_BITS(R0)
	MOVD R7, CTX_LL(R0)
	MOVD R8, CTX_ML(R0)
	MOVD R9, CTX_OF(R0)
	MOVD R10, CTX_REP0(R0)
	MOVD R11, CTX_REP1(R0)
	MOVD R12, CTX_REP2(R0)
	MOVD CTX_SEQS(R0), R1
	SUB  R1, R13, R1
	MOVD $12, R26
	UDIV R26, R1, R1
	MOVD R1, CTX_I(R0)
	RET

// Offsets in execCtx; zstd_asm_test.go checks them.
#define EX_OUT 0
#define EX_O 8
#define EX_END 16
#define EX_LITS 24
#define EX_LIT 32
#define EX_LITEND 40
#define EX_SEQS 48
#define EX_N 56
#define EX_FS 64
#define EX_WINDOW 72
#define EX_MASKS 80
#define EX_STRIDES 88
#define EX_ERR 96

// func execSeqsARM64(ctx *execCtx)
//
// execSeqsARM64 is the loop of execBlock, with 16-byte copies. A match closer
// than 16 bytes starts with one TBL of the bytes before it into the repeating
// pattern, then copies 16 bytes at a time from a multiple of the distance at
// least 16 back.
//
// As on amd64, it prefetches the match source of the sequence PREFETCH_AHEAD
// after the current one, for matches far back. A prefetch of a bad address
// does not fault.
//
// Registers: R0 ctx, R2 output position, R3 block end, R4 frame start, R5
// window, R6 literal position, R7 literal end, R8 next seqVal, R9 seqVal
// end, R10 output position of the sequence prefetched next, R11 &shufMasks,
// R12 &stride16, R13/R14/R15 literal length/match length/offset. R1 and
// R19-R24 are scratch.
#define PREFETCH_AHEAD 8

TEXT ·execSeqsARM64(SB), NOSPLIT, $0-8
	MOVD ctx+0(FP), R0
	MOVD EX_OUT(R0), R1
	MOVD EX_O(R0), R2
	ADD  R1, R2, R2
	MOVD EX_END(R0), R3
	ADD  R1, R3, R3
	MOVD EX_FS(R0), R4
	ADD  R1, R4, R4
	MOVD EX_WINDOW(R0), R5
	MOVD EX_LITS(R0), R1
	MOVD EX_LIT(R0), R6
	ADD  R1, R6, R6
	MOVD EX_LITEND(R0), R7
	ADD  R1, R7, R7
	MOVD EX_SEQS(R0), R8
	MOVD EX_N(R0), R9
	MOVD $12, R19
	MUL  R19, R9, R9
	ADD  R8, R9, R9
	MOVD EX_MASKS(R0), R11
	MOVD EX_STRIDES(R0), R12
	CMP  R9, R8
	BHS  exdone

	// R10: where the sequence PREFETCH_AHEAD on starts
	MOVD R2, R10
	MOVD R8, R19
	MOVD $PREFETCH_AHEAD, R20

expre:
	CMP   R9, R19
	BHS   exloop
	MOVWU (R19), R21
	ADD   R21, R10, R10
	MOVWU 4(R19), R21
	ADD   R21, R10, R10
	ADD   $12, R19, R19
	SUBS  $1, R20, R20
	BNE   expre

exloop:
	ADD   $(12*PREFETCH_AHEAD), R8, R19
	CMP   R9, R19
	BHS   exnopre
	MOVWU (R19), R21
	MOVWU 4(R19), R22
	MOVWU 8(R19), R23
	ADD   R21, R10, R10
	SUB   R23, R10, R24
	PRFM  (R24), PLDL1KEEP
	ADD   R22, R10, R10

exnopre:
	MOVWU (R8), R13
	MOVWU 4(R8), R14
	MOVWU 8(R8), R15

	// the literals must be there and the sequence must fit the block
	ADD R13, R6, R19
	CMP R7, R19
	BHI excorrupt
	ADD R13, R2, R19
	ADD R14, R19, R19
	CMP R3, R19
	BHI excorrupt

	// literals, 16 bytes at a time, up to 16 past the end
	MOVD R6, R22
	MOVD R2, R23
	MOVD R13, R19

exlit:
	LDP.P 16(R22), (R20, R21)
	STP.P (R20, R21), 16(R23)
	SUBS  $16, R19, R19
	BGT   exlit
	ADD   R13, R6, R6
	ADD   R13, R2, R2

	// offset-1 < min(output of the frame, window); offset 0 wraps
	SUB  R4, R2, R19
	CMP  R5, R19
	CSEL HI, R5, R19, R19
	SUB  $1, R15, R20
	CMP  R19, R20
	BHS  excorrupt

	SUB R15, R2, R22
	CMP $16, R15
	BLO exshort

	// each 16-byte load reads only finished bytes
	MOVD R2, R23
	MOVD R14, R19

exmatch:
	LDP.P 16(R22), (R20, R21)
	STP.P (R20, R21), 16(R23)
	SUBS  $16, R19, R19
	BGT   exmatch
	B     exmdone

exshort:
	// the indexes of the mask pick only the dist bytes before the output
	VLD1 (R22), [V0.B16]
	ADD  R15<<4, R11, R20
	VLD1 (R20), [V1.B16]
	VTBL V1.B16, [V0.B16], V0.B16
	VST1 [V0.B16], (R2)
	CMP  $16, R14
	BLS  exmdone
	MOVD (R12)(R15<<3), R20
	SUB  R20, R2, R22
	ADD  $16, R22, R22
	ADD  $16, R2, R23
	SUB  $16, R14, R19

exstride:
	LDP.P 16(R22), (R20, R21)
	STP.P (R20, R21), 16(R23)
	SUBS  $16, R19, R19
	BGT   exstride

exmdone:
	ADD R14, R2, R2
	ADD $12, R8, R8
	CMP R9, R8
	BLO exloop

exdone:
	MOVD ZR, EX_ERR(R0)
	B    exout

excorrupt:
	MOVD $1, R1
	MOVD R1, EX_ERR(R0)

exout:
	MOVD EX_OUT(R0), R1
	SUB  R1, R2, R1
	MOVD R1, EX_O(R0)
	MOVD EX_LITS(R0), R1
	SUB  R1, R6, R1
	MOVD R1, EX_LIT(R0)
	RET

// Offsets in fusedCtx: a seqCtx, then an execCtx at 136. zstd_arm64_test.go
// checks them.
#define FX_OUT (136+EX_OUT)
#define FX_O (136+EX_O)
#define FX_END (136+EX_END)
#define FX_LITS (136+EX_LITS)
#define FX_LIT (136+EX_LIT)
#define FX_LITEND (136+EX_LITEND)
#define FX_FS (136+EX_FS)
#define FX_WINDOW (136+EX_WINDOW)
#define FX_MASKS (136+EX_MASKS)
#define FX_STRIDES (136+EX_STRIDES)
#define FX_ERR (136+EX_ERR)

// func fusedSeqsARM64(ctx *fusedCtx)
//
// fusedSeqsARM64 decodes each sequence with the macros of decodeSeqsARM64
// and copies it at once, as execSeqsARM64 does, without the prefetch. It stops at the
// start of a sequence when the stream start is near, after the last
// sequence, or at a sequence that does not fit, with ctx.x.err set.
//
// Registers: R2-R12 and R15 as in decodeSeqsARM64, R13 sequences left, R14
// output position, R19 literal position, R20/R21/R25 offset/match
// length/literal length, R22/R23/R24 their table entries. R0, R1, R16, R17
// and R26 are scratch. The frame holds the block end, the literal end, the
// frame start, the window, &shufMasks and &stride16.
TEXT ·fusedSeqsARM64(SB), NOSPLIT, $48-8
	MOVD ctx+0(FP), R0
	MOVD CTX_SRC(R0), R2
	MOVD CTX_PLIM(R0), R3
	ADD  R2, R3, R3
	MOVD CTX_P(R0), R1
	ADD  R1, R2, R2
	MOVD CTX_C(R0), R4
	MOVD CTX_BITS(R0), R1
	MOVD $64, R5
	SUB  R1, R5, R5
	MOVD CTX_TAB(R0), R6
	MOVD CTX_LL(R0), R7
	MOVD CTX_ML(R0), R8
	MOVD CTX_OF(R0), R9
	MOVD CTX_REP0(R0), R10
	MOVD CTX_REP1(R0), R11
	MOVD CTX_REP2(R0), R12
	MOVD CTX_N(R0), R13
	MOVD CTX_I(R0), R1
	SUB  R1, R13, R13
	MOVD $-1, R15
	MOVD FX_OUT(R0), R1
	MOVD FX_O(R0), R14
	ADD  R1, R14, R14
	MOVD FX_END(R0), R16
	ADD  R1, R16, R16
	MOVD R16, end-8(SP)
	MOVD FX_FS(R0), R16
	ADD  R1, R16, R16
	MOVD R16, fs-24(SP)
	MOVD FX_WINDOW(R0), R16
	MOVD R16, window-32(SP)
	MOVD FX_LITS(R0), R1
	MOVD FX_LIT(R0), R19
	ADD  R1, R19, R19
	MOVD FX_LITEND(R0), R16
	ADD  R1, R16, R16
	MOVD R16, litend-16(SP)
	MOVD FX_MASKS(R0), R16
	MOVD R16, masks-40(SP)
	MOVD FX_STRIDES(R0), R16
	MOVD R16, strides-48(SP)
	CBZ  R13, fexit

floop:
	CMP R3, R2
	BLO fexit
	SEQ_HEAD
	BHS fllread
	RELOAD

fllread:
	SEQ_TAIL

	// the next states first, so that the next sequence decodes while this
	// one copies; the last sequence has none
	SUB $1, R13, R13
	CBZ R13, fcopy
	SEQ_STATES

fcopy:
	// the literals must be there and the sequence must fit the block
	ADD  R25, R19, R0
	MOVD litend-16(SP), R1
	CMP  R1, R0
	BHI  fcorrupt
	ADD  R25, R14, R0
	ADD  R21, R0, R0
	MOVD end-8(SP), R1
	CMP  R1, R0
	BHI  fcorrupt

	MOVD R19, R0
	MOVD R14, R1
	MOVD R25, R26

flit:
	LDP.P 16(R0), (R16, R17)
	STP.P (R16, R17), 16(R1)
	SUBS  $16, R26, R26
	BGT   flit
	ADD   R25, R19, R19
	ADD   R25, R14, R14

	// offset-1 < min(output of the frame, window); offset 0 wraps
	MOVD fs-24(SP), R0
	SUB  R0, R14, R0
	MOVD window-32(SP), R1
	CMP  R1, R0
	CSEL HI, R1, R0, R0
	SUB  $1, R10, R1
	CMP  R0, R1
	BHS  fcorrupt

	SUB  R10, R14, R0
	CMP  $16, R10
	BLO  fshort
	MOVD R14, R1
	MOVD R21, R26

fmatch:
	LDP.P 16(R0), (R16, R17)
	STP.P (R16, R17), 16(R1)
	SUBS  $16, R26, R26
	BGT   fmatch
	B     fnext

fshort:
	VLD1 (R0), [V0.B16]
	MOVD masks-40(SP), R1
	ADD  R10<<4, R1, R1
	VLD1 (R1), [V1.B16]
	VTBL V1.B16, [V0.B16], V0.B16
	VST1 [V0.B16], (R14)
	CMP  $16, R21
	BLS  fnext
	MOVD strides-48(SP), R1
	MOVD (R1)(R10<<3), R1
	SUB  R1, R14, R0
	ADD  $16, R0, R0
	ADD  $16, R14, R1
	SUB  $16, R21, R26

fstride:
	LDP.P 16(R0), (R16, R17)
	STP.P (R16, R17), 16(R1)
	SUBS  $16, R26, R26
	BGT   fstride

fnext:
	ADD  R21, R14, R14
	CBNZ R13, floop

fexit:
	MOVD ctx+0(FP), R0
	MOVD CTX_SRC(R0), R1
	SUB  R1, R2, R1
	MOVD R1, CTX_P(R0)
	MOVD R4, CTX_C(R0)
	MOVD $64, R1
	SUB  R5, R1, R1
	MOVD R1, CTX_BITS(R0)
	MOVD R7, CTX_LL(R0)
	MOVD R8, CTX_ML(R0)
	MOVD R9, CTX_OF(R0)
	MOVD R10, CTX_REP0(R0)
	MOVD R11, CTX_REP1(R0)
	MOVD R12, CTX_REP2(R0)
	MOVD CTX_N(R0), R1
	SUB  R13, R1, R1
	MOVD R1, CTX_I(R0)
	MOVD FX_OUT(R0), R1
	SUB  R1, R14, R1
	MOVD R1, FX_O(R0)
	MOVD FX_LITS(R0), R1
	SUB  R1, R19, R1
	MOVD R1, FX_LIT(R0)
	MOVD ZR, FX_ERR(R0)
	RET

fcorrupt:
	MOVD ctx+0(FP), R0
	MOVD $1, R1
	MOVD R1, FX_ERR(R0)
	RET
