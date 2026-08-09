package asm

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ConstTable is a read-only data segment (string pool, LCPI constants)
// lifted from the input into a Go DATA/GLOBL symbol.
type ConstTable struct {
	Name     string
	Labels   map[string]uint // original label -> byte offset
	Data     []byte
	Relocs   []Reloc // symbol references embedded in Data
	Writable bool    // writable data/bss must not be emitted as RODATA
}

// Reloc is a symbol reference at a byte offset inside a table.
type Reloc struct {
	Offset   uint
	Width    uint
	Symbol   string
	Addend   int64
	Subtract string
}

// collectConstTables scans the program for data segments (.cstring /
// .rodata / .const) and assembles their contents into tables. Only
// segments that contain labels referenced by code are kept; the caller
// filters by usage.
func collectConstTables(prog *Program) ([]*ConstTable, error) {
	var tables []*ConstTable
	var cur *ConstTable
	inData := false
	aliases := make(map[string]string)

	flush := func() {
		if cur != nil && len(cur.Data) > 0 {
			tables = append(tables, cur)
		}
		cur = nil
		inData = false
	}

	for _, node := range prog.Nodes {
		switch n := node.(type) {
		case *Directive:
			switch n.Name {
			case "section", "cstring", "text", "data", "const", "bss", "rodata":
				if isDataSection(n) {
					writable := isWritableDataSection(n)
					if cur == nil || cur.Writable != writable {
						flush()
						cur = &ConstTable{Name: fmt.Sprintf("LCDATA%d", len(tables)+1), Labels: map[string]uint{}, Writable: writable}
					}
					inData = true
				} else {
					flush()
				}
			case "zerofill":
				fields := strings.Split(strings.Join(n.Args, ""), ",")
				if len(fields) >= 4 {
					if cur == nil || !cur.Writable {
						flush()
						cur = &ConstTable{Name: fmt.Sprintf("LCDATA%d", len(tables)+1), Labels: map[string]uint{}, Writable: true}
					}
					inData = true
					align := int64(1)
					if len(fields) >= 5 {
						if exponent, err := strconv.ParseUint(strings.TrimSpace(fields[4]), 0, 8); err == nil && exponent < 63 {
							align = int64(1) << exponent
						}
					}
					if remainder := int64(len(cur.Data)) % align; remainder != 0 {
						cur.Data = append(cur.Data, make([]byte, align-remainder)...)
					}
					label := strings.TrimSpace(fields[2])
					if label != "" {
						cur.Labels[label] = uint(len(cur.Data))
					}
					if size, err := strconv.ParseInt(strings.TrimSpace(fields[3]), 0, 64); err == nil && size > 0 {
						cur.Data = append(cur.Data, make([]byte, int(size))...)
					}
				}
			case "comm", "lcomm":
				fields := splitDirectiveArgs(n.Args)
				if len(fields) >= 2 {
					if cur == nil || !cur.Writable {
						flush()
						cur = &ConstTable{Name: fmt.Sprintf("LCDATA%d", len(tables)+1), Labels: map[string]uint{}, Writable: true}
					}
					inData = true
					align := int64(1)
					if len(fields) >= 3 {
						if value, err := strconv.ParseInt(fields[2], 0, 64); err == nil && value > 0 {
							align = value
						}
					}
					if remainder := int64(len(cur.Data)) % align; remainder != 0 {
						cur.Data = append(cur.Data, make([]byte, align-remainder)...)
					}
					cur.Labels[fields[0]] = uint(len(cur.Data))
					if size, err := strconv.ParseInt(fields[1], 0, 64); err == nil && size > 0 {
						cur.Data = append(cur.Data, make([]byte, size)...)
					}
				}
			case "base64":
				if inData && cur != nil {
					payload := strings.TrimSpace(strings.Join(n.Args, ""))
					payload = strings.Trim(payload, `"`)
					if decoded, err := base64.StdEncoding.DecodeString(payload); err == nil {
						cur.Data = append(cur.Data, decoded...)
					}
				}
			case "set":
				fields := splitDirectiveArgs(n.Args)
				if len(fields) == 2 {
					aliases[fields[0]] = fields[1]
				}
			case "asciz", "ascii", "string":
				if inData && cur != nil {
					if s := parseStringLiteral(n.Args); s != nil {
						cur.Data = append(cur.Data, s...)
						if n.Name != "ascii" {
							cur.Data = append(cur.Data, 0)
						}
					}
				}
			case "byte", "short", "value", "long", "quad", "xword", "word":
				if inData && cur != nil {
					if err := appendNumericData(cur, n); err != nil {
						return nil, fmt.Errorf("collect constants line %d: %w", n.Line, err)
					}
				}
			case "space", "zero":
				if inData && cur != nil {
					if len(n.Args) > 0 {
						if v, err := strconv.ParseInt(n.Args[0], 0, 64); err == nil && v > 0 {
							cur.Data = append(cur.Data, make([]byte, v)...)
						}
					}
				}
			case "align", "balign", "p2align":
				if inData && cur != nil {
					pad := alignPad(cur.Data, n)
					cur.Data = append(cur.Data, pad...)
				}
			}
		case *LabelLine:
			if inData && cur != nil {
				// Data sections may use ordinary symbols such as
				// _unicode_cc_index, not only LCPI/string labels.
				cur.Labels[n.Name] = uint(len(cur.Data))
			}
		case *CommentLine, *BlankLine:
			// ignore
		}
	}
	flush()
	for range len(aliases) {
		resolved := false
		for alias, expression := range aliases {
			reloc, err := parseReloc(expression, 0, 0)
			if err != nil || reloc.Subtract != "" {
				continue
			}
			table, offset, ok := dataLabelLocation(tables, reloc.Symbol)
			if !ok {
				continue
			}
			offset += reloc.Addend
			if offset < 0 {
				return nil, fmt.Errorf("data alias %q resolves before %s", alias, table.Name)
			}
			table.Labels[alias] = uint(offset)
			delete(aliases, alias)
			resolved = true
		}
		if !resolved {
			break
		}
	}
	if err := resolveConstRelocs(tables); err != nil {
		return nil, err
	}
	return tables, nil
}

func isDataSection(d *Directive) bool {
	if d.Name == "data" || d.Name == "bss" || d.Name == "rodata" || d.Name == "cstring" || d.Name == "const" {
		return true
	}
	joined := strings.ToLower(strings.Join(d.Args, " "))
	for _, marker := range []string{"cstring", "rodata", "const", "data.rel.ro", "literal", ",__data", ",__bss", ".data", ".bss"} {
		if strings.Contains(joined, marker) {
			return true
		}
	}
	return false
}

func isWritableDataSection(d *Directive) bool {
	if d.Name == "data" || d.Name == "bss" {
		return true
	}
	joined := strings.ToLower(strings.Join(d.Args, " "))
	if strings.Contains(joined, "data.rel.ro") {
		return false
	}
	fields := splitDirectiveArgs(d.Args)
	for _, field := range fields[1:] {
		flags := strings.Trim(strings.TrimSpace(field), `"'`)
		if strings.Contains(flags, "w") {
			return true
		}
	}
	if len(fields) >= 2 && strings.EqualFold(strings.TrimSpace(fields[0]), "__DATA") {
		switch strings.ToLower(strings.TrimSpace(fields[1])) {
		case "__data", "__bss", "__common", "__thread_data", "__thread_bss":
			return true
		default:
			return false
		}
	}
	if len(fields) > 0 {
		section := strings.ToLower(strings.TrimSpace(fields[0]))
		return section == "data" || section == ".data" ||
			section == "bss" || section == ".bss" ||
			strings.HasPrefix(section, ".data.")
	}
	return false
}

// isDataLabel reports whether a label belongs to a data segment
// (string pool or constant labels), not to code.
func isDataLabel(name string) bool {
	if strings.HasPrefix(name, "l_.str") || strings.HasPrefix(name, "L_.str") {
		return true
	}
	if strings.HasPrefix(name, "LCPI") || strings.HasPrefix(name, ".LCPI") {
		return true
	}
	if strings.HasPrefix(name, "LJTI") || strings.HasPrefix(name, ".LJTI") {
		return true
	}
	return false
}

// parseStringLiteral decodes an .asciz/.ascii argument list into bytes.
func parseStringLiteral(args []string) []byte {
	if len(args) == 0 {
		return nil
	}
	out := make([]byte, 0)
	for _, arg := range args {
		arg = strings.TrimPrefix(arg, ",")
		arg = strings.TrimSpace(arg)
		if len(arg) < 2 || arg[0] != '"' || arg[len(arg)-1] != '"' {
			// Comma-separated literal list; try the first quoted chunk.
			if idx := strings.Index(arg, `"`); idx >= 0 {
				arg = arg[idx:]
				end := strings.Index(arg[1:], `"`)
				if end < 0 {
					continue
				}
				arg = arg[:end+2]
			} else {
				continue
			}
		}
		inner := arg[1 : len(arg)-1]
		for i := 0; i < len(inner); i++ {
			c := inner[i]
			if c != '\\' {
				out = append(out, c)
				continue
			}
			i++
			if i >= len(inner) {
				break
			}
			switch inner[i] {
			case 'n':
				out = append(out, '\n')
			case 't':
				out = append(out, '\t')
			case 'r':
				out = append(out, '\r')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case 'v':
				out = append(out, '\v')
			case 'a':
				out = append(out, '\a')
			case '\\':
				out = append(out, '\\')
			case '"':
				out = append(out, '"')
			case 'x':
				if i+2 < len(inner) {
					if v, err := strconv.ParseUint(inner[i+1:i+3], 16, 8); err == nil {
						out = append(out, byte(v))
						i += 2
					}
				}
			default:
				if inner[i] >= '0' && inner[i] <= '7' {
					end := i + 1
					for end < len(inner) && end < i+3 && inner[end] >= '0' && inner[end] <= '7' {
						end++
					}
					v, _ := strconv.ParseUint(inner[i:end], 8, 8)
					out = append(out, byte(v))
					i = end - 1
				} else {
					out = append(out, inner[i])
				}
			}
		}
	}
	return out
}

func splitDirectiveArgs(args []string) []string {
	joined := strings.Join(args, " ")
	fields := strings.Split(joined, ",")
	result := fields[:0]
	for _, field := range fields {
		if field = strings.TrimSpace(field); field != "" {
			result = append(result, field)
		}
	}
	return result
}

func appendNumericData(t *ConstTable, d *Directive) error {
	size := map[string]int{"byte": 1, "word": 2, "short": 2, "value": 2, "long": 4, "quad": 8, "xword": 8}[d.Name]
	for _, arg := range splitDirectiveArgs(d.Args) {
		arg = strings.TrimSpace(strings.TrimPrefix(arg, ","))
		if arg == "" {
			continue
		}
		if !isNumericLiteral(arg) {
			reloc, err := parseReloc(arg, uint(len(t.Data)), uint(size))
			if err != nil {
				return err
			}
			t.Relocs = append(t.Relocs, reloc)
			t.Data = append(t.Data, make([]byte, size)...)
			continue
		}
		v, err := strconv.ParseInt(arg, 0, 64)
		if err != nil {
			if u, uerr := strconv.ParseUint(arg, 0, 64); uerr == nil {
				v = int64(u)
			} else {
				return fmt.Errorf("parse %s value %q: %w", d.Name, arg, err)
			}
		}
		appendLittleEndian(&t.Data, v, size)
	}
	return nil
}

func appendLittleEndian(dst *[]byte, value int64, size int) {
	for i := 0; i < size; i++ {
		*dst = append(*dst, byte(value>>(8*i)))
	}
}

func parseReloc(expression string, offset, width uint) (Reloc, error) {
	compact := strings.Join(strings.Fields(expression), "")
	if compact == "" {
		return Reloc{}, fmt.Errorf("empty relocation expression")
	}
	operator := -1
	for i := 1; i < len(compact); i++ {
		if compact[i] == '+' || compact[i] == '-' {
			operator = i
			break
		}
	}
	symbol := compact
	var suffix string
	if operator >= 0 {
		symbol, suffix = compact[:operator], compact[operator+1:]
	}
	if !isAssemblySymbol(symbol) || (operator >= 0 && suffix == "") {
		return Reloc{}, fmt.Errorf("unsupported relocation expression %q", expression)
	}
	reloc := Reloc{
		Offset: offset,
		Width:  width,
		Symbol: strings.TrimPrefix(symbol, "_"),
	}
	if operator < 0 {
		return reloc, nil
	}
	if isNumericLiteral(suffix) {
		addend, err := strconv.ParseInt(suffix, 0, 64)
		if err != nil {
			return Reloc{}, fmt.Errorf("parse relocation addend %q: %w", suffix, err)
		}
		if compact[operator] == '-' {
			addend = -addend
		}
		reloc.Addend = addend
		return reloc, nil
	}
	if compact[operator] != '-' || !isAssemblySymbol(suffix) {
		return Reloc{}, fmt.Errorf("unsupported relocation expression %q", expression)
	}
	reloc.Subtract = strings.TrimPrefix(suffix, "_")
	return reloc, nil
}

func isAssemblySymbol(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '.' || c == '$' {
			continue
		}
		return false
	}
	return true
}

func dataLabelLocation(tables []*ConstTable, symbol string) (*ConstTable, int64, bool) {
	for _, table := range tables {
		for label, offset := range table.Labels {
			if label == symbol || strings.TrimPrefix(label, "_") == symbol {
				return table, int64(offset), true
			}
		}
	}
	return nil, 0, false
}

func resolveConstRelocs(tables []*ConstTable) error {
	for _, table := range tables {
		pending := table.Relocs[:0]
		for _, reloc := range table.Relocs {
			width := reloc.Width
			if width == 0 {
				width = 8
			}
			if width != 1 && width != 2 && width != 4 && width != 8 {
				return fmt.Errorf("data relocation at %s+%d has invalid width %d", table.Name, reloc.Offset, width)
			}
			if int(reloc.Offset+width) > len(table.Data) {
				return fmt.Errorf("data relocation at %s+%d exceeds table size %d", table.Name, reloc.Offset, len(table.Data))
			}
			reloc.Width = width
			if reloc.Subtract != "" {
				leftTable, left, leftOK := dataLabelLocation(tables, reloc.Symbol)
				if reloc.Symbol == "." {
					leftTable, left, leftOK = table, int64(reloc.Offset), true
				}
				rightTable, right, rightOK := dataLabelLocation(tables, reloc.Subtract)
				if reloc.Subtract == "." {
					rightTable, right, rightOK = table, int64(reloc.Offset), true
				}
				if !leftOK || !rightOK || leftTable != rightTable {
					return fmt.Errorf("data relocation %q-%q cannot be resolved within one table", reloc.Symbol, reloc.Subtract)
				}
				value := left - right + reloc.Addend
				for i := uint(0); i < width; i++ {
					table.Data[reloc.Offset+i] = byte(value >> (8 * i))
				}
				continue
			}
			if width != 8 {
				return fmt.Errorf("unsupported %d-byte absolute relocation for %q", width, reloc.Symbol)
			}
			if targetTable, targetOffset, ok := dataLabelLocation(tables, reloc.Symbol); ok && targetOffset+reloc.Addend < 0 {
				return fmt.Errorf("data relocation for %q resolves before %s", reloc.Symbol, targetTable.Name)
			}
			pending = append(pending, reloc)
		}
		table.Relocs = pending
	}
	return nil
}

func isNumericLiteral(s string) bool {
	if s == "" {
		return false
	}
	c := s[0]
	if c == '-' || c == '+' {
		s = s[1:]
	}
	if len(s) > 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') {
		return true
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

func alignPad(data []byte, d *Directive) []byte {
	fields := splitDirectiveArgs(d.Args)
	if len(fields) == 0 {
		return nil
	}
	value, err := strconv.Atoi(fields[0])
	if err != nil || value <= 0 {
		return nil
	}
	align := value
	if d.Name == "p2align" {
		if value >= strconv.IntSize-1 {
			return nil
		}
		align = 1 << uint(value)
	}
	if len(data)%align == 0 {
		return nil
	}
	return make([]byte, align-len(data)%align)
}

// EmitTables renders DATA/GLOBL lines for all tables.
func EmitTables(tables []*ConstTable) []string {
	var out []string
	for _, t := range tables {
		bytes := t.Data
		if t.Writable && len(t.Relocs) == 0 && allZero(bytes) {
			size := (len(bytes) + 7) &^ 7
			if size > 0 {
				out = append(out, fmt.Sprintf("GLOBL %s<>(SB), 16, $%d", t.Name, size))
			}
			continue
		}
		for len(bytes)%8 != 0 {
			bytes = append(bytes, 0)
		}
		relocs := append([]Reloc(nil), t.Relocs...)
		sort.Slice(relocs, func(i, j int) bool { return relocs[i].Offset < relocs[j].Offset })
		for pos, ri := 0, 0; pos < len(bytes); {
			if ri < len(relocs) && int(relocs[ri].Offset) == pos {
				width := relocs[ri].Width
				if width == 0 {
					width = 8
				}
				out = append(out, fmt.Sprintf("DATA %s<>+0x%03x(SB)/%d, $%s", t.Name, pos, width, relocExpr(tables, relocs[ri])))
				pos += int(width)
				ri++
				continue
			}
			end := len(bytes)
			if ri < len(relocs) && int(relocs[ri].Offset) > pos {
				end = int(relocs[ri].Offset)
			}
			width := 8
			for width > 1 && (pos%width != 0 || pos+width > end) {
				width /= 2
			}
			hex := ""
			for j := pos + width - 1; j >= pos; j-- {
				hex += fmt.Sprintf("%02x", bytes[j])
			}
			out = append(out, fmt.Sprintf("DATA %s<>+0x%03x(SB)/%d, $0x%s", t.Name, pos, width, hex))
			pos += width
		}
		flags := 16 // C data is outside Go's pointer ownership.
		if !t.Writable {
			flags |= 8
		}
		out = append(out, fmt.Sprintf("GLOBL %s<>(SB), %d, $%d", t.Name, flags, len(bytes)))
	}
	return out
}

func allZero(data []byte) bool {
	for _, b := range data {
		if b != 0 {
			return false
		}
	}
	return true
}

func relocExpr(tables []*ConstTable, reloc Reloc) string {
	if table, offset, ok := dataLabelLocation(tables, reloc.Symbol); ok {
		offset += reloc.Addend
		switch {
		case offset > 0:
			return fmt.Sprintf("%s<>+0x%x(SB)", table.Name, offset)
		case offset < 0:
			return fmt.Sprintf("%s<>-0x%x(SB)", table.Name, -offset)
		default:
			return fmt.Sprintf("%s<>(SB)", table.Name)
		}
	}
	target := "·" + NativeName(sanitizeLabelPrefix(reloc.Symbol))
	switch {
	case reloc.Addend > 0:
		return fmt.Sprintf("%s+%d(SB)", target, reloc.Addend)
	case reloc.Addend < 0:
		return fmt.Sprintf("%s%d(SB)", target, reloc.Addend)
	default:
		return target + "(SB)"
	}
}
