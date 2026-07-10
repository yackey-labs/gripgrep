package parity

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
)

// GGFlag is one entry from cmd/gg/flags.go's v1Flags (buildV1Flags): a
// flag gg actually implements.
type GGFlag struct {
	Long    string
	Short   string // single-char string, "" = no short form
	Negated string
	Aliases []string
}

// GGNotImplemented is one entry from cmd/gg/flags.go's notImplementedFlags:
// a real rg flag gg deliberately rejects with a targeted error.
type GGNotImplemented struct {
	Long  string
	Short string
	Label string
}

// ParseGGFlags reads cmd/gg/flags.go (path) and extracts both flag lists.
// cmd/gg is package main, so it can't be imported directly -- this parses
// the source with go/parser and walks the go/ast tree looking for the
// buildV1Flags function's returned []*flagSpec composite literal and the
// notImplementedFlags variable's []notImplementedFlag composite literal.
// Line-splitting or regex would be fragile against reformatting; go/ast
// walks the actual syntax tree.
func ParseGGFlags(path string) (implemented []GGFlag, notImplemented []GGNotImplemented, err error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	var foundBuild, foundNotImpl bool
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name.Name != "buildV1Flags" {
				continue
			}
			foundBuild = true
			implemented, err = parseFlagSpecs(d)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: buildV1Flags: %w", path, err)
			}
		case *ast.GenDecl:
			if d.Tok != token.VAR {
				continue
			}
			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if name.Name != "notImplementedFlags" {
						continue
					}
					foundNotImpl = true
					notImplemented, err = parseNotImplemented(vs.Values[i])
					if err != nil {
						return nil, nil, fmt.Errorf("%s: notImplementedFlags: %w", path, err)
					}
				}
			}
		}
	}
	if !foundBuild {
		return nil, nil, fmt.Errorf("%s: buildV1Flags function not found", path)
	}
	if !foundNotImpl {
		return nil, nil, fmt.Errorf("%s: notImplementedFlags variable not found", path)
	}
	return implemented, notImplemented, nil
}

func parseFlagSpecs(decl *ast.FuncDecl) ([]GGFlag, error) {
	var out []GGFlag
	for _, stmt := range decl.Body.List {
		ret, ok := stmt.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			continue
		}
		cl, ok := ret.Results[0].(*ast.CompositeLit)
		if !ok {
			continue
		}
		for _, el := range cl.Elts {
			inner, ok := el.(*ast.CompositeLit)
			if !ok {
				return nil, fmt.Errorf("flagSpec element is %T, want *ast.CompositeLit", el)
			}
			gf, err := flagSpecFields(inner)
			if err != nil {
				return nil, err
			}
			out = append(out, gf)
		}
	}
	return out, nil
}

func flagSpecFields(cl *ast.CompositeLit) (GGFlag, error) {
	var gf GGFlag
	for _, e := range cl.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		var err error
		switch key.Name {
		case "long":
			gf.Long, err = stringLit(kv.Value)
		case "short":
			gf.Short, err = charLit(kv.Value)
		case "negated":
			gf.Negated, err = stringLit(kv.Value)
		case "aliases":
			gf.Aliases, err = stringSliceLit(kv.Value)
		}
		if err != nil {
			return gf, fmt.Errorf("field %s: %w", key.Name, err)
		}
	}
	if gf.Long == "" {
		return gf, fmt.Errorf("flagSpec with no long name")
	}
	return gf, nil
}

func parseNotImplemented(expr ast.Expr) ([]GGNotImplemented, error) {
	cl, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, fmt.Errorf("value is %T, want *ast.CompositeLit", expr)
	}
	var out []GGNotImplemented
	for _, el := range cl.Elts {
		inner, ok := el.(*ast.CompositeLit)
		if !ok {
			return nil, fmt.Errorf("notImplementedFlag element is %T, want *ast.CompositeLit", el)
		}
		var nf GGNotImplemented
		for _, e := range inner.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			var err error
			switch key.Name {
			case "long":
				nf.Long, err = stringLit(kv.Value)
			case "short":
				nf.Short, err = charLit(kv.Value)
			case "label":
				nf.Label, err = stringLit(kv.Value)
			}
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", key.Name, err)
			}
		}
		if nf.Long == "" {
			return nil, fmt.Errorf("notImplementedFlag with no long name")
		}
		out = append(out, nf)
	}
	return out, nil
}

func stringLit(e ast.Expr) (string, error) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", fmt.Errorf("expected string literal, got %T", e)
	}
	return strconv.Unquote(bl.Value)
}

// charLit unquotes a Go rune literal (e.g. 'F') into its single-character
// string form, matching parity.RgFlag.Short's representation.
func charLit(e ast.Expr) (string, error) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.CHAR {
		return "", fmt.Errorf("expected char literal, got %T", e)
	}
	return strconv.Unquote(bl.Value)
}

func stringSliceLit(e ast.Expr) ([]string, error) {
	cl, ok := e.(*ast.CompositeLit)
	if !ok {
		return nil, fmt.Errorf("expected composite literal, got %T", e)
	}
	var out []string
	for _, el := range cl.Elts {
		s, err := stringLit(el)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}
