//go:build amd64 && !purego

#include "textflag.h"

// func hasBMI2() bool
TEXT ·hasBMI2(SB), NOSPLIT, $0-1
	XORL AX, AX
	XORL CX, CX
	CPUID
	CMPL AX, $7
	JB   none
	MOVL $7, AX
	XORL CX, CX
	CPUID
	SHRL $8, BX
	ANDL $1, BX
	MOVB BX, ret+0(FP)
	RET

none:
	MOVB $0, ret+0(FP)
	RET

// Offsets in seqCtx; zstd_amd64_test.go checks them.
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
#define CTX_PLIMABS 120
#define CTX_ENDABS 128

// READ adds the next n bits (n in CX) of the stream to dst. R8 holds the 8
// bytes at the read position and R9 the number of their bits not yet read,
// counting from the top, so the next n bits are R8 >> (R9-n), masked.
#define READ(dst) \
	SUBQ  CX, R9     \
	SHRXQ R9, R8, BX \
	BZHIQ CX, BX, BX \
	ADDQ  BX, dst

// RELOAD steps the read position back over the whole bytes read and loads
// the 8 bytes there, leaving at most 7 bits read.
#define RELOAD \
	MOVQ  $64, BX \
	SUBQ  R9, BX  \
	MOVQ  BX, BP  \
	SHRQ  $3, BP  \
	SUBQ  BP, SI  \
	ANDQ  $-8, BX \
	ADDQ  BX, R9  \
	MOVQ  (SI), R8

// func decodeSeqsBMI2(ctx *seqCtx)
//
// decodeSeqsBMI2 is the loop of zdec.decodeSeqs, for the part of the stream
// at least 24 bytes from its start: each sequence reloads twice and each
// reload steps back at most 8 bytes, so the 8-byte loads stay in the stream.
// It returns at the start of a sequence, with ctx updated, when the stream
// start is near or every sequence is decoded.
//
// Registers: AX next seqVal, SI read position, DI ctx, R8 bits, R9 bits
// left, R11 tables, R12/R13/R14 literal length/match length/offset state,
// R15 rep0. BX, CX, DX, BP and R10 are scratch.
TEXT ·decodeSeqsBMI2(SB), NOSPLIT, $8-8
	MOVQ ctx+0(FP), DI
	MOVQ CTX_SRC(DI), SI
	MOVQ CTX_PLIM(DI), BX
	ADDQ SI, BX
	MOVQ BX, CTX_PLIMABS(DI)
	ADDQ CTX_P(DI), SI
	MOVQ CTX_C(DI), R8
	MOVQ $64, R9
	SUBQ CTX_BITS(DI), R9
	MOVQ CTX_TAB(DI), R11
	MOVQ CTX_LL(DI), R12
	MOVQ CTX_ML(DI), R13
	MOVQ CTX_OF(DI), R14
	MOVQ CTX_REP0(DI), R15
	MOVQ CTX_SEQS(DI), AX
	MOVQ CTX_N(DI), BX
	IMULQ $12, BX
	ADDQ AX, BX
	MOVQ BX, CTX_ENDABS(DI)
	MOVQ CTX_I(DI), BX
	IMULQ $12, BX
	ADDQ BX, AX

loop:
	CMPQ SI, CTX_PLIMABS(DI)
	JB   exit
	CMPQ AX, CTX_ENDABS(DI)
	JAE  exit
	ANDQ $0x7ff, R12
	ANDQ $0x7ff, R13
	ANDQ $0x7ff, R14

	// offset value into DX, match length into R10: at most 54 bits read
	MOVBQZX 4(R11)(R14*8), CX
	MOVL    (R11)(R14*8), DX
	READ(DX)
	MOVBQZX 4(R11)(R13*8), CX
	MOVL    (R11)(R13*8), R10
	READ(R10)
	RELOAD

	// literal length into BP
	MOVBQZX 4(R11)(R12*8), CX
	MOVL    (R11)(R12*8), BP
	READ(BP)

	// offset values 1-3 are repeat offsets; a zero literal length shifts
	// them by one, and 4 is rep0-1
	MOVBQZX 4(R11)(R14*8), CX
	CMPQ    CX, $1
	JBE     repeat
	MOVQ    CTX_REP1(DI), BX
	MOVQ    BX, CTX_REP2(DI)
	MOVQ    R15, CTX_REP1(DI)
	MOVQ    DX, R15
	JMP     store

repeat:
	TESTQ BP, BP
	JNZ   shifted
	INCQ  DX

shifted:
	CMPQ DX, $2
	JB   store
	JE   rep1
	CMPQ DX, $3
	JE   rep2
	MOVQ CTX_REP1(DI), BX
	MOVQ BX, CTX_REP2(DI)
	MOVQ R15, CTX_REP1(DI)
	DECQ R15
	JMP  store

rep1:
	MOVQ CTX_REP1(DI), BX
	MOVQ R15, CTX_REP1(DI)
	MOVQ BX, R15
	JMP  store

rep2:
	MOVQ CTX_REP2(DI), BX
	MOVQ CTX_REP1(DI), CX
	MOVQ CX, CTX_REP2(DI)
	MOVQ R15, CTX_REP1(DI)
	MOVQ BX, R15

store:
	MOVL BP, 0(AX)
	MOVL R10, 4(AX)
	MOVL R15, 8(AX)
	ADDQ $12, AX
	CMPQ AX, CTX_ENDABS(DI)
	JAE  exit

	// next states: at most 42 more bits since the reload
	MOVBQZX 5(R11)(R12*8), CX
	MOVWQZX 6(R11)(R12*8), DX
	READ(DX)
	MOVQ    DX, R12
	MOVBQZX 5(R11)(R13*8), CX
	MOVWQZX 6(R11)(R13*8), DX
	READ(DX)
	MOVQ    DX, R13
	MOVBQZX 5(R11)(R14*8), CX
	MOVWQZX 6(R11)(R14*8), DX
	READ(DX)
	MOVQ    DX, R14
	RELOAD
	JMP     loop

exit:
	SUBQ CTX_SRC(DI), SI
	MOVQ SI, CTX_P(DI)
	MOVQ R8, CTX_C(DI)
	MOVQ $64, BX
	SUBQ R9, BX
	MOVQ BX, CTX_BITS(DI)
	MOVQ R12, CTX_LL(DI)
	MOVQ R13, CTX_ML(DI)
	MOVQ R14, CTX_OF(DI)
	MOVQ R15, CTX_REP0(DI)
	SUBQ CTX_SEQS(DI), AX
	XORL DX, DX
	MOVQ $12, CX
	DIVQ CX
	MOVQ AX, CTX_I(DI)
	RET

#define HUF_TAB 0
#define HUF_SRC 8
#define HUF_P 16
#define HUF_PMIN 48
#define HUF_H 80
#define HUF_OUT 112
#define HUF_SEG 120
#define HUF_SHIFT 128
#define HUF_ROUNDS 136

// HRELOAD steps stream k back over the whole bytes it read, found from the
// marker bit below its unread bits, and reloads 8 bytes with a new marker.
// The marker replaces the last bit of the 8 bytes, which a round never
// reaches: at most 7 + 5*11 bits are read between reloads. A stream within
// 8 bytes of its start ends the loop instead.
#define HRELOAD(k, h) \
	MOVQ   HUF_P+8*k(DI), R8    \
	CMPQ   R8, HUF_PMIN+8*k(DI) \
	JB     hexit                \
	TZCNTQ h, BX                \
	MOVQ   BX, DX               \
	SHRQ   $3, DX               \
	SUBQ   DX, R8               \
	MOVQ   R8, HUF_P+8*k(DI)    \
	MOVQ   (R9)(R8*1), R8       \
	ORQ    $1, R8               \
	ANDQ   $7, BX               \
	SHLXQ  BX, R8, h

// HSYM decodes one symbol of a stream with bits h into dst.
#define HSYM(h, dst) \
	SHRXQ   R10, h, BX         \
	MOVWQZX (R11)(BX*2), DX    \
	MOVB    DX, dst            \
	SHRQ    $8, DX             \
	SHLXQ   DX, h, h

// func huf4BMI2(ctx *hufCtx)
//
// Registers: AX stream 1 output, SI stream 3 output, CX segment length,
// DI ctx, R9 input, R10 64 - table log, R11 table, R12-R15 stream bits,
// BP rounds left. BX, DX and R8 are scratch.
TEXT ·huf4BMI2(SB), NOSPLIT, $8-8
	MOVQ ctx+0(FP), DI
	MOVQ HUF_TAB(DI), R11
	MOVQ HUF_SRC(DI), R9
	MOVQ HUF_SHIFT(DI), R10
	MOVQ HUF_H+0(DI), R12
	MOVQ HUF_H+8(DI), R13
	MOVQ HUF_H+16(DI), R14
	MOVQ HUF_H+24(DI), R15
	MOVQ HUF_OUT(DI), AX
	MOVQ HUF_SEG(DI), CX
	LEAQ (AX)(CX*2), SI
	MOVQ HUF_ROUNDS(DI), BP

hloop:
	HRELOAD(0, R12)
	HRELOAD(1, R13)
	HRELOAD(2, R14)
	HRELOAD(3, R15)
	HSYM(R12, 0(AX))
	HSYM(R13, 0(AX)(CX*1))
	HSYM(R14, 0(SI))
	HSYM(R15, 0(SI)(CX*1))
	HSYM(R12, 1(AX))
	HSYM(R13, 1(AX)(CX*1))
	HSYM(R14, 1(SI))
	HSYM(R15, 1(SI)(CX*1))
	HSYM(R12, 2(AX))
	HSYM(R13, 2(AX)(CX*1))
	HSYM(R14, 2(SI))
	HSYM(R15, 2(SI)(CX*1))
	HSYM(R12, 3(AX))
	HSYM(R13, 3(AX)(CX*1))
	HSYM(R14, 3(SI))
	HSYM(R15, 3(SI)(CX*1))
	HSYM(R12, 4(AX))
	HSYM(R13, 4(AX)(CX*1))
	HSYM(R14, 4(SI))
	HSYM(R15, 4(SI)(CX*1))
	ADDQ $5, AX
	ADDQ $5, SI
	DECQ BP
	JNZ  hloop

hexit:
	MOVQ R12, HUF_H+0(DI)
	MOVQ R13, HUF_H+8(DI)
	MOVQ R14, HUF_H+16(DI)
	MOVQ R15, HUF_H+24(DI)
	SUBQ BP, HUF_ROUNDS(DI)
	RET

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

// func execSeqsSSE(ctx *execCtx)
//
// execSeqsSSE is the loop of execBlock, with 16-byte copies. A match closer
// than 16 bytes starts with one PSHUFB of the bytes before it into the
// repeating pattern, then copies 16 bytes at a time from a multiple of the
// distance at least 16 back.
//
// It prefetches the match source of the sequence PREFETCH_AHEAD after the
// current one: matches far back, as zstd -19 makes in its 8 MB window, miss
// the cache (5% faster on a level 19 log, no change on others). A prefetch
// of a bad address does not fault.
//
// Registers: DI ctx, R8 output position, R9 block end, R10 frame start,
// R11 window, SI literal position, R12 literal end, R13 next seqVal, R14
// seqVal end, BP output position of the sequence prefetched next. AX, BX,
// CX, DX and R15 are scratch.
#define PREFETCH_AHEAD 8

TEXT ·execSeqsSSE(SB), NOSPLIT, $8-8
	MOVQ ctx+0(FP), DI
	MOVQ EX_OUT(DI), AX
	MOVQ EX_O(DI), R8
	ADDQ AX, R8
	MOVQ EX_END(DI), R9
	ADDQ AX, R9
	MOVQ EX_FS(DI), R10
	ADDQ AX, R10
	MOVQ EX_WINDOW(DI), R11
	MOVQ EX_LITS(DI), AX
	MOVQ EX_LIT(DI), SI
	ADDQ AX, SI
	MOVQ EX_LITEND(DI), R12
	ADDQ AX, R12
	MOVQ EX_SEQS(DI), R13
	MOVQ EX_N(DI), R14
	IMULQ $12, R14
	ADDQ R13, R14
	CMPQ R13, R14
	JAE  exdone

	// BP: where the sequence PREFETCH_AHEAD on starts
	MOVQ R8, BP
	MOVQ R13, DX
	MOVQ $PREFETCH_AHEAD, CX

expre:
	CMPQ DX, R14
	JAE  exloop
	MOVL 0(DX), AX
	ADDQ AX, BP
	MOVL 4(DX), AX
	ADDQ AX, BP
	ADDQ $12, DX
	DECQ CX
	JNZ  expre

exloop:
	LEAQ 12*PREFETCH_AHEAD(R13), DX
	CMPQ DX, R14
	JAE  exnopre
	MOVL 0(DX), AX
	MOVL 4(DX), BX
	MOVL 8(DX), CX
	ADDQ AX, BP
	MOVQ BP, R15
	SUBQ CX, R15
	PREFETCHT0 (R15)
	ADDQ BX, BP

exnopre:
	MOVL 0(R13), AX
	MOVL 4(R13), BX
	MOVL 8(R13), CX

	// the literals must be there and the sequence must fit the block
	LEAQ (SI)(AX*1), DX
	CMPQ DX, R12
	JA   excorrupt
	LEAQ (R8)(AX*1), DX
	ADDQ BX, DX
	CMPQ DX, R9
	JA   excorrupt

	// literals, 16 bytes at a time, up to 16 past the end
	MOVOU (SI), X0
	MOVOU X0, (R8)
	CMPQ  AX, $16
	JBE   exlitdone
	MOVQ  $16, DX

exlit:
	MOVOU (SI)(DX*1), X0
	MOVOU X0, (R8)(DX*1)
	ADDQ  $16, DX
	CMPQ  DX, AX
	JB    exlit

exlitdone:
	ADDQ AX, SI
	ADDQ AX, R8

	// offset-1 < min(output of the frame, window); offset 0 wraps
	MOVQ    R8, DX
	SUBQ    R10, DX
	CMPQ    DX, R11
	CMOVQHI R11, DX
	LEAQ    -1(CX), R15
	CMPQ    R15, DX
	JAE     excorrupt

	MOVQ R8, DX
	SUBQ CX, DX
	CMPQ CX, $16
	JB   exshort

	MOVOU (DX), X0
	MOVOU X0, (R8)
	CMPQ  BX, $16
	JBE   exmdone
	MOVQ  $16, R15

exmatch:
	MOVOU (DX)(R15*1), X0
	MOVOU X0, (R8)(R15*1)
	ADDQ  $16, R15
	CMPQ  R15, BX
	JB    exmatch
	JMP   exmdone

exshort:
	MOVOU  (DX), X0
	MOVQ   CX, R15
	SHLQ   $4, R15
	MOVQ   EX_MASKS(DI), AX
	PSHUFB (AX)(R15*1), X0
	MOVOU  X0, (R8)
	CMPQ   BX, $16
	JBE    exmdone
	MOVQ   EX_STRIDES(DI), AX
	MOVQ   (AX)(CX*8), AX
	MOVQ   R8, DX
	SUBQ   AX, DX
	MOVQ   $16, R15

exstride:
	MOVOU (DX)(R15*1), X0
	MOVOU X0, (R8)(R15*1)
	ADDQ  $16, R15
	CMPQ  R15, BX
	JB    exstride

exmdone:
	ADDQ BX, R8
	ADDQ $12, R13
	CMPQ R13, R14
	JB   exloop

exdone:
	MOVQ $0, EX_ERR(DI)
	JMP  exout

excorrupt:
	MOVQ $1, EX_ERR(DI)

exout:
	SUBQ EX_OUT(DI), R8
	MOVQ R8, EX_O(DI)
	SUBQ EX_LITS(DI), SI
	MOVQ SI, EX_LIT(DI)
	RET

#define ES_OUT 0
#define ES_O 8
#define ES_SEQS 16
#define ES_CODES 24
#define ES_I 32
#define ES_LLT 40
#define ES_MLT 48
#define ES_OFT 56
#define ES_LLS 64
#define ES_MLS 72
#define ES_OFS 80
#define ES_ACC 88
#define ES_NB 96
#define ES_LLINFO 104
#define ES_MLINFO 112

// Offsets in fseEnc; zstd_amd64_test.go checks them.
#define FE_STATE 8
#define FE_TT 1032

// ETRANS encodes the code at byte off of the packed codes with the FSE
// table T and state S: it writes the state's low k bits and moves S on.
#define ETRANS(off, T, S) \
	MOVBQZX off(R14), DX          \
	MOVQ    FE_TT(T)(DX*8), BP    \
	MOVL    S, DX                 \
	ADDL    BP, DX                \
	SHRL    $16, DX               \
	BZHIQ   DX, S, AX             \
	SHLXQ   R9, AX, AX            \
	ORQ     AX, R8                \
	ADDQ    DX, R9                \
	SHRXQ   DX, S, AX             \
	SARQ    $32, BP               \
	ADDQ    BP, AX                \
	ANDQ    $511, AX              \
	MOVWQZX FE_STATE(T)(AX*2), S

// EFLUSH stores the 8 bytes of R8 and keeps the bits of a partial byte;
// R9 is below 64.
#define EFLUSH \
	MOVQ  R8, (SI)   \
	MOVQ  R9, AX     \
	SHRQ  $3, AX     \
	ADDQ  AX, SI     \
	MOVQ  R9, AX     \
	ANDQ  $56, AX    \
	SHRXQ AX, R8, R8 \
	ANDQ  $7, R9

// EXTRA adds the extra bits of the length at byte off of the packed codes:
// the length at loff in the sequence minus its code's base, from the info
// table at ctx offset info.
#define EXTRA(off, info, loff) \
	MOVBQZX off(R14), DX     \
	MOVQ    info(DI), AX     \
	MOVQ    (AX)(DX*8), BP   \
	MOVL    loff(R13), AX    \
	SUBL    BP, AX           \
	SHLXQ   R9, AX, AX       \
	ORQ     AX, R8           \
	SHRQ    $32, BP          \
	ADDQ    BP, R9

// func encSeqsBMI2(ctx *seqEncCtx)
//
// encSeqsBMI2 is the loop of encodeSeqBits: it encodes sequences i down to
// 0 into space the caller reserved, 12 bytes each.
//
// Registers: SI output, DI ctx, R8 bits, R9 their count, R10/R11/R12
// offset/match length/literal length state, R13 sequence, R14 its packed
// codes, R15/BX/CX their tables. AX, DX and BP are scratch.
TEXT ·encSeqsBMI2(SB), NOSPLIT, $8-8
	MOVQ ctx+0(FP), DI
	MOVQ ES_OUT(DI), SI
	ADDQ ES_O(DI), SI
	MOVQ ES_ACC(DI), R8
	MOVQ ES_NB(DI), R9
	MOVQ ES_LLS(DI), R12
	MOVQ ES_MLS(DI), R11
	MOVQ ES_OFS(DI), R10
	MOVQ ES_OFT(DI), R15
	MOVQ ES_MLT(DI), BX
	MOVQ ES_LLT(DI), CX
	MOVQ ES_I(DI), AX
	MOVQ AX, R13
	IMULQ $12, R13
	ADDQ ES_SEQS(DI), R13
	MOVQ ES_CODES(DI), R14
	LEAQ (R14)(AX*4), R14

eloop:
	ETRANS(2, R15, R10)
	ETRANS(1, BX, R11)
	ETRANS(0, CX, R12)
	EFLUSH
	EXTRA(0, ES_LLINFO, 0)
	EXTRA(1, ES_MLINFO, 4)

	// the offset's extra bits are the offset value without its top bit;
	// flush first if they would not fit
	MOVBQZX 2(R14), DX
	LEAQ    (R9)(DX*1), AX
	CMPQ    AX, $64
	JBE     efits
	EFLUSH

efits:
	MOVL  8(R13), AX
	BTRL  DX, AX
	SHLXQ R9, AX, AX
	ORQ   AX, R8
	ADDQ  DX, R9
	EFLUSH
	SUBQ  $12, R13
	SUBQ  $4, R14
	CMPQ  R14, ES_CODES(DI)
	JAE   eloop

	SUBQ ES_OUT(DI), SI
	MOVQ SI, ES_O(DI)
	MOVQ R8, ES_ACC(DI)
	MOVQ R9, ES_NB(DI)
	MOVQ R12, ES_LLS(DI)
	MOVQ R11, ES_MLS(DI)
	MOVQ R10, ES_OFS(DI)
	RET
