#include "textflag.h"

TEXT ·call0Address(SB), NOSPLIT, $0-8
	LEAQ ·call0Target(SB), AX
	MOVQ AX, ret+0(FP)
	RET

TEXT ·callBytesAddress(SB), NOSPLIT, $0-8
	LEAQ ·callBytesTarget(SB), AX
	MOVQ AX, ret+0(FP)
	RET

TEXT ·call0Target(SB), NOSPLIT|NOFRAME, $0-0
	MOVL $42, AX
	RET

TEXT ·callBytesTarget(SB), NOSPLIT|NOFRAME, $0-0
	MOVBLZX (DI), CX
	ADDQ SI, CX
	MOVQ CX, AX
	RET
