package asm

import (
	"fmt"
	"strings"

	"golang.org/x/arch/arm64/arm64asm"
	"golang.org/x/arch/x86/x86asm"
)

var (
	amd64Registers = buildAMD64RegisterSet()
	arm64Registers = buildARM64RegisterSet()
)

func buildAMD64RegisterSet() map[string]struct{} {
	set := make(map[string]struct{})
	for i := 0; i < 256; i++ {
		r := x86asm.Reg(i)
		name := strings.ToUpper(r.String())
		if name == "" || strings.HasPrefix(name, "REG(") {
			continue
		}
		set[name] = struct{}{}
		if strings.HasPrefix(name, "X") {
			set["XMM"+name[1:]] = struct{}{}
			set["YMM"+name[1:]] = struct{}{}
			set["ZMM"+name[1:]] = struct{}{}
		}
	}
	for i := 0; i <= 31; i++ {
		num := fmt.Sprintf("%d", i)
		set["XMM"+num] = struct{}{}
		set["YMM"+num] = struct{}{}
		set["ZMM"+num] = struct{}{}
		set["K"+num] = struct{}{}
	}
	// Classic names without the R prefix (Go asm spelling).
	for _, name := range []string{"AX", "BX", "CX", "DX", "SI", "DI", "BP", "SP"} {
		set[name] = struct{}{}
	}
	for i := 8; i <= 15; i++ {
		base := fmt.Sprintf("R%d", i)
		for _, suffix := range []string{"B", "W", "D"} {
			set[base+suffix] = struct{}{}
		}
	}
	return set
}

func buildARM64RegisterSet() map[string]struct{} {
	set := make(map[string]struct{})
	for r := arm64asm.Reg(0); r < 2048; r++ {
		name := strings.ToUpper(r.String())
		if name == "" || strings.HasPrefix(name, "REG(") {
			continue
		}
		set[name] = struct{}{}
		if strings.HasPrefix(name, "V") {
			num := name[1:]
			for _, arr := range []string{"8B", "16B", "4H", "8H", "2S", "4S", "2D", "1D", "1Q", "B", "H", "S", "D", "Q"} {
				set["V"+num+"."+arr] = struct{}{}
			}
		}
	}
	set["SP"] = struct{}{}
	set["WSP"] = struct{}{}
	set["XZR"] = struct{}{}
	set["WZR"] = struct{}{}
	return set
}

func isAMD64Register(name string) bool {
	_, ok := amd64Registers[strings.ToUpper(name)]
	return ok
}

func isARM64Register(name string) bool {
	_, ok := arm64Registers[strings.ToUpper(name)]
	return ok
}
