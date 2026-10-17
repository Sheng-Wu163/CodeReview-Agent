package context

import (
	"context"
	"time"

	"go.uber.org/zap"
	"golang.org/x/tools/go/packages"

	"codereviewagent/internal/diff"
)

// Builder is the default ContextBuilder (FR-25..FR-29). It detects the
// structural changes in the diff, then performs a single light
// go/packages load to retrieve the callers of the changed functions and the
// summaries of the functions they call.
//
// It implements the ContextBuilder interface declared in builder.go.
type Builder struct {
	// Root is the workspace root the packages are loaded from.
	Root string
	// Logger records degradation warnings. May be nil.
	Logger *zap.Logger
	// MaxCallers caps the returned call sites (default 10).
	MaxCallers int
	// LoadTimeout bounds the go/packages load (default 10s).
	LoadTimeout time.Duration
}

// Compile-time check that Builder implements ContextBuilder.
var _ ContextBuilder = (*Builder)(nil)

// NewBuilder creates a Builder rooted at root with default limits.
func NewBuilder(root string, logger *zap.Logger) *Builder {
	return &Builder{
		Root:        root,
		Logger:      logger,
		MaxCallers:  defaultMaxCallers,
		LoadTimeout: defaultLoadTimeout,
	}
}

// Build implements ContextBuilder. It degrades to an empty bundle (never an
// error) when the package load fails or times out (FR-29).
func (b *Builder) Build(ctx context.Context, diffs []diff.FileDiff) (ContextBundle, error) {
	changes := DetectStructuralChanges(diffs)
	if len(changes) == 0 {
		// No structural change: no context section (AC-51/AC-58).
		return ContextBundle{}, nil
	}
	log := b.logger()

	pkgs := loadPackages(ctx, b.Root, b.loadTimeout(), log)
	if pkgs == nil {
		// go/packages failed: degrade to an empty context (FR-29).
		return ContextBundle{}, nil
	}

	targets := TargetNames(changes)
	retriever := &CallerRetriever{
		Root:        b.Root,
		MaxCallers:  b.MaxCallers,
		LoadTimeout: b.LoadTimeout,
		Logger:      b.Logger,
	}
	return ContextBundle{
		Changes: changes,
		Callers: retriever.Callers(pkgs, targets),
		Callees: b.extractCallees(pkgs, targets),
	}, nil
}

// extractCallees gathers the callee summaries across all healthy loaded
// packages, deduplicated by function name.
func (b *Builder) extractCallees(pkgs []*packages.Package, targets map[string]bool) []FunctionDef {
	seen := map[string]bool{}
	var out []FunctionDef
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			continue
		}
		for _, fd := range ExtractCallees(pkg.Name, pkg.Syntax, targets) {
			if seen[fd.Name] {
				continue
			}
			seen[fd.Name] = true
			out = append(out, fd)
		}
	}
	return out
}

// loadTimeout returns the configured load timeout or the default.
func (b *Builder) loadTimeout() time.Duration {
	if b.LoadTimeout > 0 {
		return b.LoadTimeout
	}
	return defaultLoadTimeout
}

// logger returns the configured logger or a no-op logger.
func (b *Builder) logger() *zap.Logger {
	if b.Logger != nil {
		return b.Logger
	}
	return zap.NewNop()
}
