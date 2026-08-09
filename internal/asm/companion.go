package asm

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"path"
	"strconv"
	"strings"
)

// directEntry is an explicit request for a cgo-free Go ABI0 wrapper around a
// converted C function. Only bodyless declarations named _<c-name> request
// this path; ordinary Go functions in the companion file are ignored.
type directEntry struct {
	goName     string
	nativeName string
	args       []directParam
	results    []directParam
}

type directParam struct {
	name string
}

func parseCompanion(src []byte) (map[string]*directEntry, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "companion.go", src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("companion parse: %w", err)
	}
	bindings := inspectDirectTypeBindings(file)
	entries := make(map[string]*directEntry)
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body != nil {
			continue
		}
		position := fset.Position(function.Pos())
		name := function.Name.Name
		if function.Doc != nil {
			for _, comment := range function.Doc.List {
				text := strings.TrimSpace(strings.TrimPrefix(comment.Text, "//"))
				if strings.HasPrefix(text, "go:linkname") {
					return nil, fmt.Errorf("companion line %d: direct leaf %s must not use //go:linkname", position.Line, name)
				}
			}
		}
		if function.Recv != nil {
			return nil, fmt.Errorf("companion line %d: direct leaf %s must not be a method", position.Line, name)
		}
		if function.Type.TypeParams != nil && len(function.Type.TypeParams.List) != 0 {
			return nil, fmt.Errorf("companion line %d: direct leaf %s must not have type parameters", position.Line, name)
		}
		if len(name) < 2 || name[0] != '_' {
			return nil, fmt.Errorf("companion line %d: bodyless declaration %s must be named _<c-function> to request a direct leaf", position.Line, name)
		}
		nativeName := name[1:]
		if _, exists := entries[nativeName]; exists {
			return nil, fmt.Errorf("companion line %d: duplicate direct leaf declaration for %s", position.Line, nativeName)
		}
		usedNames := make(map[string]bool)
		args, err := parseDirectParams(function.Type.Params, "arg", usedNames, bindings)
		if err != nil {
			return nil, fmt.Errorf("companion %s params: %w", name, err)
		}
		results, err := parseDirectParams(function.Type.Results, "ret", usedNames, bindings)
		if err != nil {
			return nil, fmt.Errorf("companion %s results: %w", name, err)
		}
		if len(results) > 1 {
			return nil, fmt.Errorf("companion %s: direct leaf supports at most one result, got %d", name, len(results))
		}
		entries[nativeName] = &directEntry{
			goName:     name,
			nativeName: nativeName,
			args:       args,
			results:    results,
		}
	}
	return entries, nil
}

func parseDirectParams(fields *ast.FieldList, prefix string, usedNames map[string]bool, bindings directTypeBindings) ([]directParam, error) {
	if fields == nil {
		return nil, nil
	}
	var params []directParam
	for _, field := range fields.List {
		typeName := renderGoExpr(field.Type)
		if err := bindings.validate(typeName); err != nil {
			return nil, err
		}
		names := field.Names
		if len(names) == 0 {
			names = []*ast.Ident{{Name: directSlotName(prefix, len(params))}}
		}
		for _, identifier := range names {
			name := identifier.Name
			if name == "_" {
				name = directSlotName(prefix, len(params))
			}
			if usedNames[name] {
				return nil, fmt.Errorf("slot name %s is declared more than once", name)
			}
			usedNames[name] = true
			params = append(params, directParam{name: name})
		}
	}
	return params, nil
}

func directSlotName(prefix string, index int) string {
	if index == 0 {
		return prefix
	}
	return fmt.Sprintf("%s%d", prefix, index)
}

type directTypeBindings struct {
	shadowed      map[string]bool
	unsafeImports int
}

func inspectDirectTypeBindings(file *ast.File) directTypeBindings {
	bindings := directTypeBindings{shadowed: make(map[string]bool)}
	for _, declaration := range file.Decls {
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			bindings.shadowed[declaration.Name.Name] = true
		case *ast.GenDecl:
			for _, spec := range declaration.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					bindings.shadowed[spec.Name.Name] = true
				case *ast.ValueSpec:
					for _, name := range spec.Names {
						bindings.shadowed[name.Name] = true
					}
				case *ast.ImportSpec:
					importPath, err := strconv.Unquote(spec.Path.Value)
					if err != nil {
						continue
					}
					localName := path.Base(importPath)
					if spec.Name != nil {
						localName = spec.Name.Name
					}
					if localName == "_" || localName == "." {
						continue
					}
					if localName == "unsafe" && importPath == "unsafe" {
						bindings.unsafeImports++
						continue
					}
					bindings.shadowed[localName] = true
				}
			}
		}
	}
	return bindings
}

func (bindings directTypeBindings) validate(typeName string) error {
	switch typeName {
	case "int", "uint", "int64", "uint64", "uintptr":
		if bindings.shadowed[typeName] {
			return fmt.Errorf("type %s is shadowed in the companion package", typeName)
		}
		return nil
	case "unsafe.Pointer":
		if bindings.unsafeImports != 1 || bindings.shadowed["unsafe"] {
			return fmt.Errorf("type unsafe.Pointer requires exactly one unaliased import of package \"unsafe\"")
		}
		return nil
	default:
		return fmt.Errorf(
			"type %s is not a supported 64-bit integer or pointer; supported types are int, uint, int64, uint64, uintptr, and unsafe.Pointer",
			typeName,
		)
	}
}

func renderGoExpr(expression ast.Expr) string {
	var output strings.Builder
	if err := format.Node(&output, token.NewFileSet(), expression); err != nil {
		return fmt.Sprintf("%T", expression)
	}
	return output.String()
}
