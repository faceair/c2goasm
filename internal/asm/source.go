package asm

import (
	"fmt"
	"strings"
)

// ParseSource splits raw assembly lines into typed nodes.
// Any parse failure is a hard error (fail-fast).
func ParseSource(lines []string, arch string) (*Program, error) {
	prog := &Program{Nodes: make([]Node, 0, len(lines))}
	for i, line := range lines {
		lineno := i + 1
		code, comment := splitLineComment(line, arch)
		trimmed := strings.TrimSpace(code)
		if trimmed == "" {
			if strings.TrimSpace(comment) != "" {
				prog.Nodes = append(prog.Nodes, &CommentLine{Text: strings.TrimSpace(comment), Line: lineno})
			} else {
				prog.Nodes = append(prog.Nodes, &BlankLine{Line: lineno})
			}
			continue
		}

		if label, rest, ok := splitLabel(trimmed); ok {
			rest = strings.TrimSpace(rest)
			if rest == "" {
				prog.Nodes = append(prog.Nodes, &LabelLine{Name: label, Line: lineno, Raw: trimmed})
				continue
			}
			// Label with trailing instruction on the same line: attach the
			// label to the instruction.
			inst, err := ParseLine(rest, arch, lineno)
			if err != nil {
				return nil, err
			}
			if inst != nil {
				inst.Label = label
				inst.Comment = comment
				prog.Nodes = append(prog.Nodes, inst)
				continue
			}
			prog.Nodes = append(prog.Nodes, &LabelLine{Name: label, Line: lineno, Raw: trimmed})
			continue
		}

		if strings.HasPrefix(trimmed, ".") {
			name, args := splitDirective(trimmed)
			prog.Nodes = append(prog.Nodes, &Directive{Name: name, Args: args, Line: lineno, Raw: trimmed, Comment: comment})
			continue
		}

		inst, err := ParseLine(trimmed, arch, lineno)
		if err != nil {
			return nil, err
		}
		if inst != nil {
			inst.Comment = comment
			prog.Nodes = append(prog.Nodes, inst)
		}
	}
	return prog, nil
}

// splitLineComment separates code from a trailing comment while preserving
// comment-like bytes inside quoted directive strings.
func splitLineComment(line string, arch string) (string, string) {
	arm64 := isArm64Arch(arch)
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		width := 0
		switch {
		case c == '/' && i+1 < len(line) && line[i+1] == '/':
			width = 2
		case c == '#' && i+1 < len(line) && line[i+1] == '#':
			width = 2
		case !arm64 && c == '#':
			width = 1
		case arm64 && c == ';':
			width = 1
		case arm64 && c == '@' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
			width = 1
		}
		if width != 0 {
			return strings.TrimRight(line[:i], " \t"), strings.TrimSpace(line[i+width:])
		}
	}
	return strings.TrimRight(line, " \t"), ""
}

func isArm64Arch(arch string) bool {
	lower := strings.ToLower(arch)
	return lower == "arm64" || lower == "aarch64"
}

// splitLabel recognizes a leading "name:" label. Returns the label name
// and the remainder of the line.
func splitLabel(trimmed string) (string, string, bool) {
	idx := strings.IndexRune(trimmed, ':')
	if idx <= 0 {
		return "", "", false
	}
	name := strings.TrimSpace(trimmed[:idx])
	if name == "" || strings.ContainsAny(name, " \t") {
		return "", "", false
	}
	rest := strings.TrimSpace(trimmed[idx+1:])
	return name, rest, true
}

func splitDirective(trimmed string) (string, []string) {
	fields := splitQuotedFields(trimmed)
	if len(fields) == 0 {
		return "", nil
	}
	name := strings.TrimPrefix(fields[0], ".")
	return name, fields[1:]
}

func splitQuotedFields(line string) []string {
	var fields []string
	start := -1
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		if start < 0 {
			if c == ' ' || c == '\t' {
				continue
			}
			start = i
		}
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		if c == ' ' || c == '\t' {
			fields = append(fields, line[start:i])
			start = -1
		}
	}
	if start >= 0 {
		fields = append(fields, line[start:])
	}
	return fields
}

// Demangle converts an Itanium-mangled symbol (_ZN.../__Z...) to a plain
// name; other symbols pass through unchanged.
func Demangle(name string) (string, error) {
	if !strings.HasPrefix(name, "_ZN") && !strings.HasPrefix(name, "__Z") {
		return name, nil
	}
	firstLengthPrefix := -1
	for i, ch := range name {
		if ch >= '0' && ch <= '9' {
			firstLengthPrefix = i
			break
		}
	}
	if firstLengthPrefix == -1 {
		return "", fmt.Errorf("demangle %q: missing length-prefixed segment", name)
	}
	var parts []string
	for index := firstLengthPrefix; index < len(name); {
		size, part, err := demanglePart(name[index:])
		if err != nil {
			return "", err
		}
		if size == 0 {
			break
		}
		parts = append(parts, part)
		index += size
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("demangle %q: no parsable name segments", name)
	}
	return strings.Join(parts, ""), nil
}

// demanglePart parses "<digits><name>" and returns consumed length.
func demanglePart(part string) (int, string, error) {
	if part == "" {
		return 0, "", nil
	}
	digits := 0
	for _, d := range part {
		if d >= '0' && d <= '9' {
			digits++
		} else {
			break
		}
	}
	if digits == 0 {
		return 0, "", nil
	}
	length := 0
	for i := 0; i < digits; i++ {
		length = length*10 + int(part[i]-'0')
	}
	end := digits + length
	if end > len(part) {
		return 0, "", fmt.Errorf("demangle %q: length prefix %d exceeds remaining %d", part, length, len(part)-digits)
	}
	return end, part[digits:end], nil
}
