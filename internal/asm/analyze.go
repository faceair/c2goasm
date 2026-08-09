package asm

import (
	"fmt"
	"strings"
)

// analyze splits a parsed program into native C functions. Frame instructions
// remain in Body unchanged; the encoder emits SP-touching instructions as raw
// machine words so Go's linker cannot reinterpret the C stack geometry.
func analyze(prog *Program) ([]*Function, error) {
	funcs := findFunctions(prog.Nodes)
	if len(funcs) == 0 {
		return nil, nil
	}

	result := make([]*Function, 0, len(funcs))
	seenNames := make(map[string]string, len(funcs))
	seenNativeNames := make(map[string]string, len(funcs))
	for i, function := range funcs {
		name, err := Demangle(function.name)
		if err != nil {
			return nil, err
		}
		// Strip the platform symbol prefix (_ on macOS, absent on ELF)
		// so TEXT/companion names are identical across platforms, then
		// sanitize so the name is a valid Go symbol (re_string_find2.cold.1
		// -> re_string_find2_cold_1).
		name = sanitizeLabelPrefix(strings.TrimPrefix(name, "_"))
		if previous, exists := seenNames[name]; exists {
			return nil, fmt.Errorf(
				"analyze: source symbols %q and %q both map to Go assembly symbol %q",
				previous,
				function.name,
				name,
			)
		}
		seenNames[name] = function.name
		nativeName := NativeName(name)
		if previous, exists := seenNativeNames[nativeName]; exists {
			return nil, fmt.Errorf(
				"analyze: source symbols %q and %q both map to native Go assembly symbol %q",
				previous,
				function.name,
				nativeName,
			)
		}
		seenNativeNames[nativeName] = function.name
		result = append(result, &Function{
			Name:    name,
			RawName: function.name,
			Body:    sliceBetween(prog.Nodes, function.labelIndex+1, functionEnd(funcs, i)),
			Export:  function.export,
		})
	}
	return result, nil
}

type globalInfo struct {
	name       string
	labelIndex int
	endIndex   int
	export     bool
}

// findFunctions collects every function in the file. Signals:
//   - .globl marks exports;
//   - .size directives mark functions on ELF targets;
//   - "; -- Begin function <name>" comments mark functions on macOS.
//
// Bodies are sliced between consecutive function labels.
func findFunctions(nodes []Node) []globalInfo {
	exported := map[string]bool{}
	kinds := map[string]string{}
	for _, node := range nodes {
		directive, ok := node.(*Directive)
		if !ok || directive.Name != "type" {
			continue
		}
		name, kind := directiveType(directive.Args)
		if name != "" {
			kinds[strings.TrimPrefix(name, "_")] = kind
		}
	}

	var order []globalInfo
	seen := map[string]int{}
	add := func(name string, endIndex int) {
		if name == "" {
			return
		}
		key := strings.TrimPrefix(name, "_")
		if index, ok := seen[key]; ok {
			if endIndex >= 0 {
				order[index].endIndex = endIndex
			}
			order[index].export = order[index].export || exported[key]
			return
		}
		seen[key] = len(order)
		order = append(order, globalInfo{
			name:     name,
			endIndex: endIndex,
			export:   exported[key],
		})
	}
	for index, node := range nodes {
		switch n := node.(type) {
		case *Directive:
			switch n.Name {
			case "globl":
				if name := directiveSymbol(n.Args); name != "" {
					exported[strings.TrimPrefix(name, "_")] = true
				}
			case "size":
				name := directiveSymbol(n.Args)
				kind := kinds[strings.TrimPrefix(name, "_")]
				if kind == "" || kind == "function" {
					add(name, index)
				}
			}
			// macOS "Begin function" comments carry the C name without
			// the platform underscore; add it so analyze strips exactly
			// one prefix (C names may themselves start with _).
			if name := beginFunctionName(n.Comment); name != "" {
				add("_"+name, -1)
			}
		case *CommentLine:
			if name := beginFunctionName(n.Text); name != "" {
				add("_"+name, -1)
			}
		}
	}
	// Resolve label indices.
	for i, g := range order {
		order[i].labelIndex = findLabelIndex(nodes, g.name)
	}
	return order
}

func directiveSymbol(args []string) string {
	if len(args) == 0 {
		return ""
	}
	name := args[0]
	if comma := strings.IndexByte(name, ','); comma >= 0 {
		name = name[:comma]
	}
	return strings.TrimSpace(name)
}

func directiveType(args []string) (string, string) {
	raw := strings.Join(args, " ")
	comma := strings.IndexByte(raw, ',')
	if comma < 0 {
		return directiveSymbol(args), ""
	}
	name := strings.TrimSpace(raw[:comma])
	kind := strings.TrimSpace(raw[comma+1:])
	if fields := strings.Fields(kind); len(fields) > 0 {
		kind = fields[0]
	}
	return name, strings.TrimLeft(strings.TrimSuffix(kind, ","), "@%")
}

// beginFunctionName extracts the function name from a clang macOS
// "-- Begin function <name>" comment.
func beginFunctionName(comment string) string {
	const marker = "Begin function "
	idx := strings.Index(comment, marker)
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(comment[idx+len(marker):])
	if space := strings.IndexAny(rest, " \t;"); space >= 0 {
		rest = rest[:space]
	}
	return rest
}

func findLabelIndex(nodes []Node, name string) int {
	for i, node := range nodes {
		if l, ok := node.(*LabelLine); ok && l.Name == name {
			return i
		}
	}
	// macOS labels carry a leading underscore.
	prefixed := "_" + name
	for i, node := range nodes {
		if l, ok := node.(*LabelLine); ok && l.Name == prefixed {
			return i
		}
	}
	return -1
}

func functionEnd(funcs []globalInfo, i int) int {
	if funcs[i].endIndex > funcs[i].labelIndex {
		return funcs[i].endIndex
	}
	if i+1 < len(funcs) {
		return funcs[i+1].labelIndex
	}
	return -1 // rest of file
}

func sliceBetween(nodes []Node, start, end int) []Node {
	if end < 0 || end > len(nodes) {
		end = len(nodes)
	}
	if start < 0 {
		start = 0
	}
	if start >= end {
		return nil
	}
	return nodes[start:end]
}
