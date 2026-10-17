package context

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"sort"
	"strings"

	"codereviewagent/internal/diff"
)

// DetectStructuralChanges inspects the diffs and returns the structural
// changes of the changed Go files (FR-25): exported function signature
// changes and interface method-set changes. Plain in-function edits and
// private-function changes yield nothing.
//
// It is a pure function of the diff (CLAUDE.v1.1.md), which makes it easy to
// unit-test with fixed diffs. Changed Go files whose fragments cannot be
// parsed are skipped (spec 12.4).
func DetectStructuralChanges(diffs []diff.FileDiff) []StructuralChange {
	var changes []StructuralChange
	for _, f := range diffs {
		path := f.Path
		if path == "" || f.IsBinary || !strings.HasSuffix(path, ".go") {
			continue
		}
		oldSrc, newSrc := reconstructSides(f)
		if oldSrc == "" && newSrc == "" {
			continue
		}

		oldFuncs := exportedFunctionSigs(oldSrc)
		newFuncs := exportedFunctionSigs(newSrc)
		for name, oldSig := range oldFuncs {
			newSig, ok := newFuncs[name]
			switch {
			case !ok:
				// Exported function removed: callers break.
				changes = append(changes, StructuralChange{
					FilePath: path, FunctionName: name, ChangeType: SignatureChanged,
				})
			case newSig != oldSig:
				changes = append(changes, StructuralChange{
					FilePath: path, FunctionName: name, ChangeType: SignatureChanged,
				})
			}
		}

		oldIfaces := exportedInterfaceMethodSets(oldSrc)
		newIfaces := exportedInterfaceMethodSets(newSrc)
		for name, oldSet := range oldIfaces {
			newSet, ok := newIfaces[name]
			if !ok || newSet != oldSet {
				changes = append(changes, StructuralChange{
					FilePath: path, FunctionName: name, ChangeType: InterfaceChanged,
				})
			}
		}
	}

	sort.Slice(changes, func(i, j int) bool {
		if changes[i].FilePath != changes[j].FilePath {
			return changes[i].FilePath < changes[j].FilePath
		}
		return changes[i].FunctionName < changes[j].FunctionName
	})
	return changes
}

// reconstructSides rebuilds the old and new source fragments of a file from
// its hunks: context lines belong to both sides, deleted lines to the old
// side and added lines to the new side.
func reconstructSides(f diff.FileDiff) (oldSrc, newSrc string) {
	var oldLines, newLines []string
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			switch l.Type {
			case diff.LineAdded:
				newLines = append(newLines, l.Content)
			case diff.LineDeleted:
				oldLines = append(oldLines, l.Content)
			case diff.LineContext:
				oldLines = append(oldLines, l.Content)
				newLines = append(newLines, l.Content)
			}
		}
	}
	return wrapSource(oldLines), wrapSource(newLines)
}

// wrapSource makes a diff fragment parseable: it prepends a synthetic
// package clause when the fragment lacks one and closes any still-open
// brace so that go/parser can recover a usable AST. Fragments are always
// partial, so parse errors are expected and tolerated.
func wrapSource(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	body := strings.Join(lines, "\n")
	if !strings.Contains(body, "package ") {
		body = "package __ctx\n\n" + body
	}
	if open := strings.Count(body, "{"); open > strings.Count(body, "}") {
		body += strings.Repeat("\n}", open-strings.Count(body, "}"))
	}
	return body
}

// exportedFunctionSigs returns a map of exported function/method name to a
// canonical signature string (receiver, parameter types, result types).
func exportedFunctionSigs(src string) map[string]string {
	out := map[string]string{}
	file := parseFragment(src)
	if file == nil {
		return out
	}
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil || !fd.Name.IsExported() {
			continue
		}
		out[fd.Name.Name] = functionSignature(fd)
	}
	return out
}

// exportedInterfaceMethodSets returns a map of exported interface name to a
// canonical, sorted method-set string.
func exportedInterfaceMethodSets(src string) map[string]string {
	out := map[string]string{}
	file := parseFragment(src)
	if file == nil {
		return out
	}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name == nil || !ts.Name.IsExported() {
				continue
			}
			it, ok := ts.Type.(*ast.InterfaceType)
			if !ok {
				continue
			}
			out[ts.Name.Name] = interfaceMethodSet(it)
		}
	}
	return out
}

// parseFragment parses a (possibly partial) Go fragment, tolerating syntax
// errors so long as a usable AST is recovered.
func parseFragment(src string) *ast.File {
	if strings.TrimSpace(src) == "" {
		return nil
	}
	fset := token.NewFileSet()
	file, _ := parser.ParseFile(fset, "fragment.go", src, parser.SkipObjectResolution)
	return file
}

// functionSignature renders fd as "recv.Name(paramTypes) (resultTypes)".
func functionSignature(fd *ast.FuncDecl) string {
	var b strings.Builder
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		b.WriteString(types.ExprString(fd.Recv.List[0].Type))
		b.WriteString(".")
	}
	b.WriteString(fd.Name.Name)
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

// fieldTypes joins the element types of a field list, comma separated.
func fieldTypes(fl *ast.FieldList) string {
	if fl == nil {
		return ""
	}
	parts := make([]string, 0, len(fl.List))
	for _, f := range fl.List {
		t := types.ExprString(f.Type)
		for range f.Names {
			parts = append(parts, t)
		}
		if len(f.Names) == 0 {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, ", ")
}

// interfaceMethodSet renders the interface's methods (and embedded types)
// as a canonical sorted string.
func interfaceMethodSet(it *ast.InterfaceType) string {
	if it.Methods == nil {
		return ""
	}
	methods := make([]string, 0, len(it.Methods.List))
	for _, m := range it.Methods.List {
		sig := types.ExprString(m.Type)
		if len(m.Names) == 0 {
			methods = append(methods, sig)
			continue
		}
		for _, n := range m.Names {
			methods = append(methods, n.Name+sig)
		}
	}
	sort.Strings(methods)
	return strings.Join(methods, "; ")
}
