package context

import (
	"bytes"
	"context"
	"go/ast"
	"go/printer"
	"go/token"
	"path/filepath"
	"strconv"
	"time"

	"go.uber.org/zap"
	"golang.org/x/tools/go/packages"
)

// Retrieval limits (FR-26).
const (
	// defaultMaxCallers caps the number of call sites returned.
	defaultMaxCallers = 10
	// defaultLoadTimeout bounds the go/packages load (CLAUDE.v1.1.md).
	defaultLoadTimeout = 10 * time.Second
	// maxSnippetLen caps a rendered call snippet.
	maxSnippetLen = 200
)

// CallerRetriever finds the direct call sites of structurally changed
// functions using golang.org/x/tools/go/packages (FR-26). Only the first
// hop is retrieved (never more than 3 hops, per spec); it degrades to an
// empty result on load failure or timeout (FR-29).
type CallerRetriever struct {
	// Root is the workspace root the packages are loaded from.
	Root string
	// MaxCallers caps the returned call sites (default 10).
	MaxCallers int
	// LoadTimeout bounds the load; defaults to 10s.
	LoadTimeout time.Duration
	// Logger records truncation and degradation warnings. May be nil.
	Logger *zap.Logger
}

// NewCallerRetriever creates a retriever rooted at root with default limits.
func NewCallerRetriever(root string, logger *zap.Logger) *CallerRetriever {
	return &CallerRetriever{
		Root:        root,
		MaxCallers:  defaultMaxCallers,
		LoadTimeout: defaultLoadTimeout,
		Logger:      logger,
	}
}

// FindCallers loads the workspace packages and returns the call sites of
// the target functions, capped at MaxCallers. It never returns an error:
// failures are logged as warnings and yield an empty slice (FR-29).
func (r *CallerRetriever) FindCallers(ctx context.Context, targets []StructuralChange) []CallSite {
	if r.Root == "" || len(targets) == 0 {
		return nil
	}
	target := TargetNames(targets)
	if len(target) == 0 {
		return nil
	}
	pkgs := loadPackages(ctx, r.Root, r.loadTimeout(), r.logger())
	if pkgs == nil {
		return nil
	}
	return r.Callers(pkgs, target)
}

// Callers returns the call sites of the target function names across the
// given already-loaded packages (used by the Builder, which loads once).
func (r *CallerRetriever) Callers(pkgs []*packages.Package, target map[string]bool) []CallSite {
	if len(target) == 0 {
		return nil
	}
	log := r.logger()
	limit := r.MaxCallers
	if limit <= 0 {
		limit = defaultMaxCallers
	}
	var out []CallSite
	seen := map[string]bool{}
	truncated := false
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			// Broken package (e.g. syntax error): skip it, keep the rest
			// (spec 12.4).
			log.Sugar().Warnw("context: skipping package with errors",
				"package", pkg.PkgPath, "errors", len(pkg.Errors))
			continue
		}
		for _, file := range pkg.Syntax {
			filename := pkg.Fset.Position(file.Pos()).Filename
			rel := r.relPath(filename)
			walkCallSites(pkg.Fset, file, rel, target, seen, limit, &out, &truncated)
		}
	}
	if truncated {
		log.Sugar().Warnw("context: caller list truncated",
			"max_callers", limit, "returned", len(out))
	}
	return out
}

// TargetNames collects the distinct function names of the changes.
func TargetNames(changes []StructuralChange) map[string]bool {
	out := make(map[string]bool, len(changes))
	for _, c := range changes {
		if c.FunctionName != "" {
			out[c.FunctionName] = true
		}
	}
	return out
}

// loadPackages loads the workspace packages with a bounded context. It
// returns nil (with a warning) on failure or timeout (FR-29).
func loadPackages(ctx context.Context, root string, timeout time.Duration, log *zap.Logger) []*packages.Package {
	if root == "" {
		return nil
	}
	if timeout <= 0 {
		timeout = defaultLoadTimeout
	}
	loadCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cfg := &packages.Config{
		Context: loadCtx,
		Dir:     root,
		Mode:    packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedSyntax,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		log.Sugar().Warnw("context: load packages failed, degrading to empty context",
			"error", err)
		return nil
	}
	if loadCtx.Err() != nil {
		log.Sugar().Warnw("context: package load timed out, degrading to empty context",
			"timeout", timeout)
		return nil
	}
	return pkgs
}

// loadTimeout returns the configured load timeout or the default.
func (r *CallerRetriever) loadTimeout() time.Duration {
	if r.LoadTimeout > 0 {
		return r.LoadTimeout
	}
	return defaultLoadTimeout
}

// walkCallSites records call sites of the target functions found in file.
func walkCallSites(fset *token.FileSet, file *ast.File, rel string, target, seen map[string]bool, limit int, out *[]CallSite, truncated *bool) {
	callerFunc := ""
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			if x.Name != nil {
				callerFunc = x.Name.Name
			}
		case *ast.CallExpr:
			if len(*out) >= limit {
				*truncated = true
				return false
			}
			name := calleeName(x)
			if name == "" || !target[name] {
				return true
			}
			line := fset.Position(x.Pos()).Line
			key := rel + ":" + strconv.Itoa(line)
			if seen[key] {
				return true
			}
			seen[key] = true
			*out = append(*out, CallSite{
				FilePath:   rel,
				Line:       line,
				Snippet:    renderCall(fset, x),
				CallerFunc: callerFunc,
			})
		}
		return true
	})
}

// calleeName extracts the called function/method name from a call
// expression.
func calleeName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		if fun.Sel != nil {
			return fun.Sel.Name
		}
	}
	return ""
}

// renderCall prints the call expression compactly, capped at maxSnippetLen.
func renderCall(fset *token.FileSet, call *ast.CallExpr) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, call); err != nil {
		return ""
	}
	s := buf.String()
	if len(s) > maxSnippetLen {
		s = s[:maxSnippetLen] + "..."
	}
	return s
}

// relPath converts an absolute package file path to a slash-separated path
// relative to the retriever root.
func (r *CallerRetriever) relPath(abs string) string {
	if r.Root == "" || abs == "" {
		return filepath.ToSlash(abs)
	}
	rel, err := filepath.Rel(r.Root, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
}

// logger returns the configured logger or a no-op logger.
func (r *CallerRetriever) logger() *zap.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return zap.NewNop()
}
