package asm

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/faceair/c2goasm/arch"
)

// attachDirectEntries binds explicit companion declarations only after their
// native bodies satisfy the direct-leaf contract. A failure aborts conversion;
// it never falls back to an unsafe Go entry.
func attachDirectEntries(functions []*Function, entries map[string]*directEntry, desc arch.Descriptor) error {
	if len(entries) == 0 {
		return nil
	}
	byName := make(map[string]*Function, len(functions))
	for _, function := range functions {
		byName[function.Name] = function
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)

	pending := make([]*Function, 0, len(names))
	for _, name := range names {
		entry := entries[name]
		function := byName[entry.nativeName]
		if function == nil {
			return fmt.Errorf("direct leaf %s: native function %q not found", entry.goName, entry.nativeName)
		}
		if !function.Export {
			return fmt.Errorf("direct leaf %s: native function %q is not source-exported", entry.goName, entry.nativeName)
		}
		for index := range entry.args {
			if _, ok := desc.IntegerArgRegister(index); !ok {
				return fmt.Errorf(
					"direct leaf %s: %s C ABI cannot pass %d word arguments entirely in registers",
					entry.goName,
					desc.Name(),
					len(entry.args),
				)
			}
		}
		if err := certifyDirectLeaf(function, entry, desc); err != nil {
			return err
		}
		pending = append(pending, function)
	}
	for _, function := range pending {
		function.direct = entries[function.Name]
	}
	return nil
}

func certifyDirectLeaf(function *Function, entry *directEntry, desc arch.Descriptor) error {
	labels := make(map[string]bool)
	for _, node := range function.Body {
		switch node := node.(type) {
		case *LabelLine:
			labels[node.Name] = true
		case *Inst:
			if node.Label != "" {
				labels[node.Label] = true
			}
		}
	}

	hasReturn := false
	for _, node := range function.Body {
		switch node := node.(type) {
		case *Inst:
			if err := certifyDirectInstruction(function, entry, node, labels, desc); err != nil {
				return err
			}
			if node.Opcode == "ret" {
				hasReturn = true
			}
		case *SyntheticInst:
			if err := certifyDirectSynthetic(function, entry, node, desc); err != nil {
				return err
			}
		}
	}
	if !hasReturn {
		return fmt.Errorf("direct leaf %s: native function %q has no return instruction", entry.goName, function.Name)
	}
	return nil
}

func certifyDirectInstruction(function *Function, entry *directEntry, instruction *Inst, labels map[string]bool, desc arch.Descriptor) error {
	fail := func(reason string) error {
		return fmt.Errorf(
			"direct leaf %s: function %s line %d %q: %s",
			entry.goName,
			function.Name,
			instruction.Line,
			instruction.Raw,
			reason,
		)
	}
	for _, prefix := range instruction.Prefixes {
		if prefix == "fs" || prefix == "gs" {
			return fail("thread-local segment access is not allowed")
		}
	}
	for _, operand := range instruction.Operands {
		switch operand := operand.(type) {
		case Register:
			if directStackRegister(operand.Name) {
				return fail(fmt.Sprintf("stack-pointer register %s is not allowed", operand.Name))
			}
			if directThreadRegister(operand.Name) {
				return fail(fmt.Sprintf("thread/runtime register %s is not allowed", operand.Name))
			}
			if desc.ReservedRegister(operand.Name) {
				return fail(fmt.Sprintf("Go runtime reserved register %s is not allowed", operand.Name))
			}
		case Memory:
			for _, register := range []string{operand.Base, operand.Index, operand.Symbol} {
				if directThreadRegister(register) {
					return fail(fmt.Sprintf("memory through thread/runtime register %s is not allowed", register))
				}
				if directStackRegister(register) {
					return fail(fmt.Sprintf("stack-relative memory through %s is not allowed", register))
				}
				if desc.ReservedRegister(register) {
					return fail(fmt.Sprintf("memory through Go runtime reserved register %s is not allowed", register))
				}
			}
		}
	}

	switch instruction.Opcode {
	case "call", "callq", "lcall", "lcallw", "lcalll", "lcallq",
		"bl", "blr", "blraa", "blrab", "blraaz", "blrabz":
		return fail("function calls are not allowed")
	case "br", "braa", "brab", "braaz", "brabz":
		return fail("indirect tail control transfer is not allowed")
	case "push", "pushw", "pushl", "pushq", "pusha", "pushaw", "pushal", "pushad",
		"pushf", "pushfw", "pushfl", "pushfd", "pushfq",
		"pop", "popw", "popl", "popq", "popa", "popaw", "popal", "popad",
		"popf", "popfw", "popfl", "popfd", "popfq", "enter", "leave",
		"retf", "retfq", "lret", "lretw", "lretl", "lretq",
		"paciasp", "pacibsp", "autiasp", "autibsp":
		return fail("implicit stack-pointer mutation or use is not allowed")
	case "syscall", "sysenter", "sysexit", "sysret", "sysretl", "sysretq",
		"int", "int1", "int3", "into", "iret", "iretw", "iretd", "iretq",
		"svc", "hvc", "smc", "eret", "drps", "rsm":
		return fail("system control transfer is not allowed")
	case "rdfsbase", "wrfsbase", "rdgsbase", "wrgsbase", "swapgs", "mrs", "msr":
		return fail("thread/runtime register access is not allowed")
	case "xbegin", "xabort":
		return fail("hardware transaction control is not allowed")
	case "ud2", "hlt", "brk", "trap", "dcps1", "dcps2", "dcps3":
		return fail("trap instruction is not allowed")
	case "retaa", "retab":
		return fail("authenticated return with implicit stack-pointer use is not allowed")
	case "ret":
		if len(instruction.Operands) != 0 {
			return fail("return with a stack adjustment is not allowed")
		}
		return nil
	}

	if directBranchOpcode(instruction.Opcode, desc.Name()) {
		if len(instruction.Operands) == 0 {
			return fail("branch has no target")
		}
		target, ok := instruction.Operands[len(instruction.Operands)-1].(Symbol)
		if !ok {
			return fail("indirect control transfer is not allowed")
		}
		if target.Offset != 0 {
			return fail(fmt.Sprintf("branch target %q has offset %d", target.Name, target.Offset))
		}
		if !labels[target.Name] {
			return fail(fmt.Sprintf("non-local control target %q is not allowed", target.Name))
		}
	}
	return nil
}

func certifyDirectSynthetic(function *Function, entry *directEntry, instruction *SyntheticInst, desc arch.Descriptor) error {
	for _, token := range strings.FieldsFunc(strings.ToUpper(instruction.Text), func(char rune) bool {
		return !unicode.IsLetter(char) && !unicode.IsDigit(char)
	}) {
		switch token {
		case "CALL", "JMP", "BL", "BLR", "BR":
			return fmt.Errorf("direct leaf %s: function %s line %d: synthetic control transfer %q is not allowed", entry.goName, function.Name, instruction.Line, instruction.Text)
		}
		if directStackRegister(token) {
			return fmt.Errorf("direct leaf %s: function %s line %d: synthetic stack reference %q is not allowed", entry.goName, function.Name, instruction.Line, instruction.Text)
		}
		if directThreadRegister(token) {
			return fmt.Errorf("direct leaf %s: function %s line %d: synthetic thread/runtime-register reference %q is not allowed", entry.goName, function.Name, instruction.Line, instruction.Text)
		}
		if desc.ReservedRegister(token) {
			return fmt.Errorf("direct leaf %s: function %s line %d: synthetic reserved-register reference %q is not allowed", entry.goName, function.Name, instruction.Line, instruction.Text)
		}
	}
	return nil
}

func directThreadRegister(name string) bool {
	switch strings.ToUpper(name) {
	case "FS", "GS":
		return true
	default:
		return false
	}
}

func directStackRegister(name string) bool {
	switch strings.ToUpper(name) {
	case "SP", "RSP", "ESP", "SPL", "WSP":
		return true
	default:
		return false
	}
}

func directBranchOpcode(opcode, target string) bool {
	if target == "amd64" {
		return strings.HasPrefix(opcode, "j") || strings.HasPrefix(opcode, "loop")
	}
	switch opcode {
	case "b", "cbz", "cbnz", "tbz", "tbnz":
		return true
	default:
		return strings.HasPrefix(opcode, "b.") || strings.HasPrefix(opcode, "bc.")
	}
}

// emitDirectWrapper emits a stable Go ABI0 entry. Its only call targets a
// certified frameless native leaf, so the linker can account for the entire
// nosplit chain while the native body continues to use the platform C ABI.
func emitDirectWrapper(function *Function, desc arch.Descriptor) []string {
	entry := function.direct
	if entry == nil {
		return nil
	}
	wordBytes := desc.WordBytes()
	argBytes := len(entry.args) * wordBytes
	frameBytes := argBytes + len(entry.results)*wordBytes
	lines := []string{fmt.Sprintf("TEXT ·%s(SB), 4, $0-%d", entry.goName, frameBytes), ""}
	for index, argument := range entry.args {
		register, ok := desc.IntegerArgRegister(index)
		if !ok {
			panic("certified direct leaf lost its register argument")
		}
		lines = append(lines, fmt.Sprintf("    %s %s+%d(FP), %s", desc.WordMove(), argument.name, index*wordBytes, register))
	}
	lines = append(lines, fmt.Sprintf("    CALL ·%s(SB)", NativeName(function.Name)))
	if desc.Name() == "amd64" {
		// X15 is Go's zero register across calls but is caller-saved in
		// System V; restore the Go ABI invariant after the C leaf returns.
		lines = append(lines, "    PXOR X15, X15")
	}
	if len(entry.results) == 1 {
		// The result becomes initialized only after the wrapper's sole call;
		// no live pointer result crosses a call safepoint.
		lines = append(lines, fmt.Sprintf(
			"    %s %s, %s+%d(FP)",
			desc.WordMove(),
			desc.IntegerReturnRegister(),
			entry.results[0].name,
			argBytes,
		))
	}
	return append(lines, "    RET")
}
