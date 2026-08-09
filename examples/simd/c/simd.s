	.build_version macos, 26, 0	sdk_version 26, 5
	.section	__TEXT,__text,regular,pure_instructions
	.globl	_sum_u32                        ; -- Begin function sum_u32
	.p2align	2
_sum_u32:                               ; @sum_u32
; %bb.0:
	cbz	x1, LBB0_3
; %bb.1:
	cmp	x1, #3
	b.hi	LBB0_4
; %bb.2:
	mov	x9, #0                          ; =0x0
	mov	x8, #0                          ; =0x0
	b	LBB0_13
LBB0_3:
	mov	x8, #0                          ; =0x0
	mov	x0, x8
	ret
LBB0_4:
	cmp	x1, #16
	b.hs	LBB0_6
; %bb.5:
	mov	x9, #0                          ; =0x0
	mov	x8, #0                          ; =0x0
	b	LBB0_10
LBB0_6:
	movi.2d	v0, #0000000000000000
	and	x9, x1, #0xfffffffffffffff0
	movi.2d	v1, #0000000000000000
	add	x8, x0, #32
	mov	x10, x9
	movi.2d	v3, #0000000000000000
	movi.2d	v4, #0000000000000000
	movi.2d	v5, #0000000000000000
	movi.2d	v2, #0000000000000000
	movi.2d	v7, #0000000000000000
	movi.2d	v6, #0000000000000000
LBB0_7:                                 ; =>This Inner Loop Header: Depth=1
	ldp	q16, q17, [x8, #-32]
	uaddw2.2d	v1, v1, v16
	uaddw.2d	v0, v0, v16
	ldp	q16, q18, [x8], #64
	uaddw2.2d	v4, v4, v17
	uaddw.2d	v3, v3, v17
	uaddw2.2d	v2, v2, v16
	uaddw.2d	v5, v5, v16
	uaddw2.2d	v6, v6, v18
	uaddw.2d	v7, v7, v18
	subs	x10, x10, #16
	b.ne	LBB0_7
; %bb.8:
	add.2d	v0, v3, v0
	add.2d	v1, v4, v1
	add.2d	v3, v7, v5
	add.2d	v0, v3, v0
	add.2d	v2, v6, v2
	add.2d	v1, v2, v1
	add.2d	v0, v0, v1
	addp.2d	d0, v0
	fmov	x8, d0
	cmp	x1, x9
	b.eq	LBB0_15
; %bb.9:
	tst	x1, #0xc
	b.eq	LBB0_13
LBB0_10:
	mov	x10, x9
	and	x9, x1, #0xfffffffffffffffc
	movi.2d	v0, #0000000000000000
	movi.2d	v1, #0000000000000000
	mov.d	v1[0], x8
	sub	x8, x10, x9
	add	x10, x0, x10, lsl #2
LBB0_11:                                ; =>This Inner Loop Header: Depth=1
	ldr	q2, [x10], #16
	uaddw2.2d	v0, v0, v2
	uaddw.2d	v1, v1, v2
	adds	x8, x8, #4
	b.ne	LBB0_11
; %bb.12:
	add.2d	v0, v1, v0
	addp.2d	d0, v0
	fmov	x8, d0
	cmp	x1, x9
	b.eq	LBB0_15
LBB0_13:
	sub	x10, x1, x9
	add	x9, x0, x9, lsl #2
LBB0_14:                                ; =>This Inner Loop Header: Depth=1
	ldr	w11, [x9], #4
	add	x8, x8, x11
	subs	x10, x10, #1
	b.ne	LBB0_14
LBB0_15:
	mov	x0, x8
	ret
                                        ; -- End function
	.globl	_add_u32                        ; -- Begin function add_u32
	.p2align	2
_add_u32:                               ; @add_u32
; %bb.0:
	cbz	x3, LBB1_5
; %bb.1:
	cmp	x3, #3
	b.hi	LBB1_6
; %bb.2:
	mov	x8, #0                          ; =0x0
LBB1_3:
	sub	x9, x3, x8
	lsl	x11, x8, #2
	add	x8, x2, x11
	add	x10, x1, x11
	add	x11, x0, x11
LBB1_4:                                 ; =>This Inner Loop Header: Depth=1
	ldr	w12, [x11], #4
	ldr	w13, [x10], #4
	add	w12, w13, w12
	str	w12, [x8], #4
	subs	x9, x9, #1
	b.ne	LBB1_4
LBB1_5:
	ret
LBB1_6:
	mov	x8, #0                          ; =0x0
	sub	x9, x2, x0
	cmp	x9, #64
	b.lo	LBB1_3
; %bb.7:
	sub	x9, x2, x1
	cmp	x9, #64
	b.lo	LBB1_3
; %bb.8:
	cmp	x3, #16
	b.hs	LBB1_10
; %bb.9:
	mov	x8, #0                          ; =0x0
	b	LBB1_14
LBB1_10:
	and	x8, x3, #0xfffffffffffffff0
	add	x9, x0, #32
	add	x10, x1, #32
	add	x11, x2, #32
	mov	x12, x8
LBB1_11:                                ; =>This Inner Loop Header: Depth=1
	ldp	q0, q1, [x9, #-32]
	ldp	q2, q3, [x9], #64
	ldp	q4, q5, [x10, #-32]
	ldp	q6, q7, [x10], #64
	add.4s	v0, v4, v0
	add.4s	v1, v5, v1
	add.4s	v2, v6, v2
	add.4s	v3, v7, v3
	stp	q0, q1, [x11, #-32]
	stp	q2, q3, [x11], #64
	subs	x12, x12, #16
	b.ne	LBB1_11
; %bb.12:
	cmp	x3, x8
	b.eq	LBB1_5
; %bb.13:
	tst	x3, #0xc
	b.eq	LBB1_3
LBB1_14:
	mov	x10, x8
	and	x8, x3, #0xfffffffffffffffc
	sub	x9, x10, x8
	lsl	x12, x10, #2
	add	x10, x2, x12
	add	x11, x1, x12
	add	x12, x0, x12
LBB1_15:                                ; =>This Inner Loop Header: Depth=1
	ldr	q0, [x12], #16
	ldr	q1, [x11], #16
	add.4s	v0, v1, v0
	str	q0, [x10], #16
	adds	x9, x9, #4
	b.ne	LBB1_15
; %bb.16:
	cmp	x3, x8
	b.ne	LBB1_3
	b	LBB1_5
                                        ; -- End function
.subsections_via_symbols
