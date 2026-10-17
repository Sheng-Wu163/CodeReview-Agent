package context

import (
	"go/ast"
	"sort"
	"strings"
)

// ExtractCallees returns summaries of the same-package functions called
// from the bodies of the target functions (FR-27). It is a pure function
// over the parsed package files (CLAUDE.v1.1.md), which makes it easy to
// unit-test with a fixed AST.
//
// Only package-level function calls (plain identifiers, e.g. helper()) are
// considered; method calls via a selector are ignored.
func ExtractCallees(pkgName string, files []*ast.File, targets map[string]bool) []FunctionDef {
	decls := indexFunctions(files)
	if len(decls) == 0 || len(targets) == 0 {
		return nil
	}

	seen := map[string]bool{}
	var out []FunctionDef
	for _, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name == nil || !targets[fd.Name.Name] || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				ident, ok := call.Fun.(*ast.Ident)
				if !ok || seen[ident.Name] {
					return true
				}
				callee, ok := decls[ident.Name]
				if !ok {
					return true
				}
				seen[ident.Name] = true
				out = append(out, functionDef(pkgName, callee))
				return true
			})
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// indexFunctions maps top-level function names to their declarations across
// the given files.
func indexFunctions(files []*ast.File) map[string]*ast.FuncDecl {
	out := map[string]*ast.FuncDecl{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name == nil {
				continue
			}
			if _, exists := out[fd.Name.Name]; !exists {
				out[fd.Name.Name] = fd
			}
		}
	}
	return out
}

// functionDef builds a FunctionDef summary from a declaration.
func functionDef(pkgName string, fd *ast.FuncDecl) FunctionDef {
	return FunctionDef{
		Package:   pkgName,
		Name:      fd.Name.Name,
		Signature: typeSignature(fd),
		Doc:       firstDocLine(fd),
	}
}

// typeSignature renders the declaration's parameter and result types
// without the function name, e.g. "(int, int) (int)".
func typeSignature(fd *ast.FuncDecl) string {
	var b strings.Builder
	b.WriteString("(")
	b.WriteString(fieldTypes(fd.Type.Params))
	b.WriteString(")")
	if res := fieldTypes(fd.Type.Results); res != "" {
		b.WriteString(" (")
		b.WriteString(res)
		b.WriteString(")")
	}
	return b.String()
}

// firstDocLine returns the first line of a declaration's doc comment.
func firstDocLine(fd *ast.FuncDecl) string {
	if fd.Doc == nil {
		return ""
	}
	text := strings.TrimSpace(fd.Doc.Text())
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	return strings.TrimSpace(text)
}
