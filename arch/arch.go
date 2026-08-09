// Package arch describes the platform C ABI choices used by the converter.
package arch

import (
	"fmt"
	"strings"
)

// Descriptor is the immutable architecture configuration for one conversion.
type Descriptor struct {
	name                  string
	stackPointer          string
	jumpMnemonics         map[string]string
	integerArgRegisters   []string
	integerReturnRegister string
	wordMove              string
	wordBytes             int
	reservedRegisters     map[string]bool
}

func (d Descriptor) Name() string         { return d.name }
func (d Descriptor) StackPointer() string { return d.stackPointer }

// IntegerArgRegister returns the platform C ABI register for a word-sized
// integer argument. False means the argument would have to use the C stack.
func (d Descriptor) IntegerArgRegister(index int) (string, bool) {
	if index < 0 || index >= len(d.integerArgRegisters) {
		return "", false
	}
	return d.integerArgRegisters[index], true
}

func (d Descriptor) IntegerReturnRegister() string { return d.integerReturnRegister }
func (d Descriptor) WordMove() string              { return d.wordMove }
func (d Descriptor) WordBytes() int                { return d.wordBytes }

// ReservedRegister reports registers that converted code must not clobber
// while it runs directly on a goroutine stack.
func (d Descriptor) ReservedRegister(name string) bool {
	return d.reservedRegisters[strings.ToUpper(name)]
}

// JumpMnemonic returns the Plan 9 spelling for an architecture branch.
func (d Descriptor) JumpMnemonic(opcode string) (string, bool) {
	mnemonic, ok := d.jumpMnemonics[opcode]
	return mnemonic, ok
}

// AMD64 returns the System V AMD64 descriptor.
func AMD64() Descriptor {
	return Descriptor{
		name:                  "amd64",
		stackPointer:          "SP",
		integerArgRegisters:   []string{"DI", "SI", "DX", "CX", "R8", "R9"},
		integerReturnRegister: "AX",
		wordMove:              "MOVQ",
		wordBytes:             8,
		reservedRegisters: map[string]bool{
			"BP": true, "EBP": true, "BPL": true,
			"R14": true, "R14D": true, "R14W": true, "R14B": true,
		},
		jumpMnemonics: map[string]string{
			"ja": "JHI", "jae": "JCC", "jb": "JCS", "jbe": "JLS",
			"jc": "JCS", "je": "JEQ", "jecxz": "JECXZ", "jg": "JGT",
			"jge": "JGE", "jl": "JLT", "jle": "JLE", "jmp": "JMP",
			"jna": "JLS", "jnae": "JCS", "jnb": "JCC", "jnbe": "JHI",
			"jnc": "JCC", "jne": "JNE", "jng": "JLE", "jnge": "JLT",
			"jnl": "JGE", "jnle": "JGT", "jno": "JOC", "jnp": "JPC",
			"jns": "JPL", "jnz": "JNE", "jo": "JOS", "jp": "JPS",
			"jpe": "JPS", "jpo": "JPC", "jrcxz": "JRCXZ", "js": "JMI",
			"jz": "JEQ",
		},
	}
}

// ARM64 returns the AAPCS64 descriptor.
func ARM64() Descriptor {
	return Descriptor{
		name:                  "arm64",
		stackPointer:          "RSP",
		integerArgRegisters:   []string{"R0", "R1", "R2", "R3", "R4", "R5", "R6", "R7"},
		integerReturnRegister: "R0",
		wordMove:              "MOVD",
		wordBytes:             8,
		reservedRegisters: map[string]bool{
			"R18": true, "W18": true,
			"R27": true, "W27": true,
			"R28": true, "W28": true,
			"R29": true, "W29": true,
			"R30": true, "W30": true,
		},
	}
}

// Resolve picks a descriptor from the supported architecture names. It does
// not guess from substrings because selecting the wrong ABI corrupts calls.
func Resolve(target string) (Descriptor, error) {
	normalized := strings.ToLower(strings.TrimSpace(target))
	switch normalized {
	case "", "x86", "x86_64", "x86-64", "amd64":
		return AMD64(), nil
	case "arm64", "aarch64":
		return ARM64(), nil
	default:
		return Descriptor{}, fmt.Errorf("unknown target architecture %q", target)
	}
}
