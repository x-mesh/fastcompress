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

// Offsets in fastCtx; inflate_amd64_test.go checks them.
#define FC_IN 0
#define FC_IP 8
#define FC_LAST 16
#define FC_OUT 24
#define FC_O 32
#define FC_END 40
#define FC_BASE 48
#define FC_LT 56
#define FC_DT 64
#define FC_LBITS 72
#define FC_DBITS 80
#define FC_HOLD 88
#define FC_BITS 96
#define FC_MASKS 104
#define FC_STRIDES 112
#define FC_STATUS 120

// Bits of the op byte of an entry, shifted to their place in the entry.
#define OP_ANY 0xff00
#define OP_BASE 0x1000
#define OP_END 0x2000
#define OP_INVALID 0x4000

// The loop keeps its state in registers, as (*decoder).fast keeps it in
// variables:
//
//	SI   next input byte (ip)
//	DI   next output byte (o)
//	R8   bit buffer (hold)
//	R9   bit count (bits); only the low 6 bits count
//	R10  literal/length table, R12 its root bits
//	R11  distance table, R13 its root bits
//	BX   entry (e), DX the bit buffer before it (pre)
//	R14  length entry (le), R15 its pre (lpr), then the length
//	BP   distance
//	AX, CX  scratch

// REFILL loads 8 bytes at ip into the bits above the count and steps ip over
// the whole bytes that fit, leaving 56-63 bits.
#define REFILL \
	MOVQ  (SI), AX   \
	SHLXQ R9, AX, AX \
	ORQ   AX, R8     \
	MOVL  R9, AX     \
	NOTL  AX         \
	SHRL  $3, AX     \
	ANDL  $7, AX     \
	ADDQ  AX, SI     \
	ORQ   $56, R9

// CONSUME removes the bits of entry BX, code and extra bits, from the buffer.
#define CONSUME \
	MOVQ  R8, DX     \
	SHRXQ BX, R8, R8 \
	SUBQ  BX, R9

#define LOOKUP_LIT \
	BZHIQ R12, R8, AX \
	MOVL  (R10)(AX*4), BX

#define LITERAL \
	MOVL BX, AX   \
	SHRL $16, AX  \
	MOVB AX, (DI) \
	INCQ DI

// SUBTABLE replaces the link BX with the entry of the sub-table that the
// next op bits select in table T.
#define SUBTABLE(T) \
	MOVL  BX, CX        \
	SHRL  $8, CX        \
	BZHIQ CX, R8, AX    \
	SHRL  $16, BX       \
	ADDQ  BX, AX        \
	MOVL  (T)(AX*4), BX

// VALUE sets dst to the base value of entry e plus its extra bits, which
// follow the code in pre: dst = e>>16 + pre>>(bits-n) & (1<<n - 1).
#define VALUE(e, pre, dst) \
	MOVL  e, CX         \
	SHRL  $8, CX        \
	ANDL  $15, CX       \
	MOVL  e, AX         \
	SUBL  CX, AX        \
	SHRXQ AX, pre, AX   \
	BZHIQ CX, AX, AX    \
	MOVL  e, dst        \
	SHRL  $16, dst      \
	ADDQ  AX, dst

// func inflateFastBMI2(ctx *fastCtx)
TEXT ·inflateFastBMI2(SB), NOSPLIT, $40-8
	MOVQ ctx+0(FP), AX
	MOVQ FC_IN(AX), SI
	MOVQ FC_LAST(AX), CX
	ADDQ SI, CX
	MOVQ CX, last-8(SP)
	ADDQ FC_IP(AX), SI
	MOVQ FC_OUT(AX), DI
	MOVQ FC_END(AX), CX
	ADDQ DI, CX
	MOVQ CX, end-16(SP)
	MOVQ FC_BASE(AX), CX
	ADDQ DI, CX
	MOVQ CX, base-24(SP)
	ADDQ FC_O(AX), DI
	MOVQ FC_MASKS(AX), CX
	MOVQ CX, masks-32(SP)
	MOVQ FC_STRIDES(AX), CX
	MOVQ CX, strides-40(SP)
	MOVQ FC_LT(AX), R10
	MOVQ FC_DT(AX), R11
	MOVQ FC_LBITS(AX), R12
	MOVQ FC_DBITS(AX), R13
	MOVQ FC_HOLD(AX), R8
	MOVQ FC_BITS(AX), R9

	// the bits above the count must be zero for the refill to OR in
	BZHIQ R9, R8, R8
	MOVQ  (SI), AX
	SHLXQ R9, AX, AX
	ORQ   AX, R8
	MOVQ  R9, AX
	SHRQ  $3, AX
	SUBQ  AX, SI
	ADDQ  $7, SI
	ORQ   $56, R9
	LOOKUP_LIT

	// Without the alignment, the loop started where it made the decoder
	// 13% slower on a Xeon Silver 4112. With it, the layout of the loop does
	// not depend on the code above.
	PCALIGN $32

loop:
	// the table load above overlaps this refill
	REFILL
	CONSUME

dispatch:
	// 3 literals (45 bits), or 2 literals and a length (50 bits), fit in
	// the 56 bits after a refill
	TESTL $OP_ANY, BX
	JNZ   notlit
	LITERAL
	LOOKUP_LIT
	CONSUME
	TESTL $OP_ANY, BX
	JNZ   notlit
	LITERAL
	LOOKUP_LIT
	CONSUME
	TESTL $OP_ANY, BX
	JNZ   notlit
	LITERAL
	JMP   check

notlit:
	TESTL $OP_BASE, BX
	JNZ   length
	TESTL $OP_INVALID, BX
	JNZ   special
	SUBTABLE(R10)
	CONSUME
	TESTL $OP_ANY, BX
	JNZ   sublen
	LITERAL
	JMP   check

sublen:
	TESTL $OP_BASE, BX
	JZ    special

length:
	MOVL BX, R14
	MOVQ DX, R15

	// at most 50 bits consumed since the last refill, so the buffer still
	// holds the root bits of the distance: look up first, then refill
	BZHIQ R13, R8, AX
	MOVL  (R11)(AX*4), BX
	MOVL  R9, AX
	ANDL  $63, AX
	CMPL  AX, $28
	JAE   distroot
	REFILL

distroot:
	CONSUME
	TESTL $OP_BASE, BX
	JNZ   distval
	TESTL $OP_INVALID, BX
	JNZ   corrupt
	SUBTABLE(R11)
	CONSUME
	TESTL $OP_BASE, BX
	JZ    corrupt

distval:
	VALUE(BX, DX, BP)
	VALUE(R14, R15, R15)

	// decode ahead: start the next symbol before the copy
	MOVL R9, AX
	ANDL $63, AX
	CMPL AX, $10
	JAE  ahead
	REFILL

ahead:
	LOOKUP_LIT
	REFILL
	CONSUME

	MOVQ DI, AX
	SUBQ base-24(SP), AX
	CMPQ BP, AX
	JA   corrupt
	MOVQ DI, AX
	SUBQ BP, AX

	// copies may run up to 15 bytes past o+length; later output overwrites
	// them and fastOutMargin keeps them in bounds
	CMPQ BP, $16
	JB   near

	// each 16-byte load reads only finished bytes; most matches in log
	// data are 32 bytes or shorter
	MOVOU (AX), X0
	MOVOU X0, (DI)
	MOVOU 16(AX), X0
	MOVOU X0, 16(DI)
	CMPQ  R15, $32
	JBE   copied
	MOVL  $32, CX

far:
	MOVOU (AX)(CX*1), X0
	MOVOU X0, (DI)(CX*1)
	ADDQ  $16, CX
	CMPQ  CX, R15
	JB    far
	JMP   copied

near:
	// repeat the last dist bytes into 16; bytes at o and after are not
	// written yet, and the mask does not pick them
	MOVOU  (AX), X0
	MOVQ   BP, CX
	SHLQ   $4, CX
	MOVQ   masks-32(SP), AX
	PSHUFB (AX)(CX*1), X0
	MOVOU  X0, (DI)
	CMPQ   R15, $16
	JBE    copied

	// then 16 at a time from a multiple of dist at least 16 back
	MOVQ strides-40(SP), AX
	MOVQ (AX)(BP*8), AX
	NEGQ AX
	ADDQ DI, AX
	MOVL $16, CX

nearloop:
	MOVOU (AX)(CX*1), X0
	MOVOU X0, (DI)(CX*1)
	ADDQ  $16, CX
	CMPQ  CX, R15
	JB    nearloop

copied:
	ADDQ R15, DI
	CMPQ SI, last-8(SP)
	JA   undo
	CMPQ DI, end-16(SP)
	JA   undo
	JMP  dispatch

undo:
	// give the decoded-ahead entry back
	MOVQ DX, R8
	ADDQ BX, R9
	XORL CX, CX
	JMP  exit

check:
	XORL CX, CX
	CMPQ SI, last-8(SP)
	JA   exit
	CMPQ DI, end-16(SP)
	JA   exit
	LOOKUP_LIT
	JMP  loop

special:
	TESTL $OP_END, BX
	JZ    corrupt
	MOVL  $1, CX

exit:
	// give back the whole bytes still in the buffer
	ANDQ  $63, R9
	MOVQ  R9, AX
	SHRQ  $3, AX
	SUBQ  AX, SI
	ANDQ  $7, R9
	BZHIQ R9, R8, R8
	MOVQ  ctx+0(FP), AX
	MOVQ  CX, FC_STATUS(AX)
	SUBQ  FC_IN(AX), SI
	MOVQ  SI, FC_IP(AX)
	SUBQ  FC_OUT(AX), DI
	MOVQ  DI, FC_O(AX)
	MOVQ  R8, FC_HOLD(AX)
	MOVQ  R9, FC_BITS(AX)
	RET

corrupt:
	MOVQ ctx+0(FP), AX
	MOVQ $2, FC_STATUS(AX)
	RET
