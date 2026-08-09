#include "textflag.h"

TEXT ·call0Address(SB), NOSPLIT, $0-8
	MOVD $·call0Target(SB), R0
	MOVD R0, ret+0(FP)
	RET

TEXT ·callBytesAddress(SB), NOSPLIT, $0-8
	MOVD $·callBytesTarget(SB), R0
	MOVD R0, ret+0(FP)
	RET

TEXT ·call0Target(SB), NOSPLIT|NOFRAME, $0-0
	MOVW $42, R0
	RET

TEXT ·callBytesTarget(SB), NOSPLIT|NOFRAME, $0-0
	MOVBU (R0), R2
	ADD R1, R2, R0
	RET
