// Package context builds the local context bundle injected into the review
// agents: the structural changes detected in the diff plus a light one-hop
// caller/callee lookup (v1.1 FR-25..FR-29).
//
// The public types below are FROZEN as of v1.1 (spec 12.3). Do not add or
// remove fields without a change proposal under specs/changes/.
package context

// Structural change types reported in StructuralChange.ChangeType (FR-25).
const (
	// SignatureChanged means an exported function's signature (parameters
	// or results) changed.
	SignatureChanged = "signature_changed"
	// InterfaceChanged means an interface's method set changed.
	InterfaceChanged = "interface_changed"
)

// StructuralChange represents a structural change in the diff (v1.1 spec
// 12.3, FROZEN). Only structural changes trigger the local caller/callee
// lookup; plain in-function edits do not.
type StructuralChange struct {
	// FilePath is the changed file path.
	FilePath string
	// FunctionName is the changed exported function or interface name.
	FunctionName string
	// ChangeType is one of SignatureChanged or InterfaceChanged.
	ChangeType string
}

// CallSite represents one call site of a structurally changed function
// (v1.1 spec 12.3, FROZEN).
type CallSite struct {
	// FilePath is the file containing the call.
	FilePath string
	// Line is the 1-based line number of the call.
	Line int
	// Snippet is the surrounding source of the call.
	Snippet string
	// CallerFunc is the name of the function containing the call.
	CallerFunc string
}

// FunctionDef is a callee definition summary (v1.1 spec 12.3, FROZEN).
type FunctionDef struct {
	// Package is the callee's package name.
	Package string
	// Name is the callee function name.
	Name string
	// Signature is the callee's signature.
	Signature string
	// Doc is the callee's doc comment (first line).
	Doc string
}

// ContextBundle is the local context injected into the review agents
// (v1.1 spec 12.3, FROZEN).
type ContextBundle struct { //nolint:revive // name is frozen by spec 12.3
	// Changes are the structural changes detected in the diff.
	Changes []StructuralChange
	// Callers are the call sites of the changed functions (max 10).
	Callers []CallSite
	// Callees are the summaries of functions called by the changed ones.
	Callees []FunctionDef
}

// IsEmpty reports whether the bundle carries no context at all. An empty
// bundle means the prompt must NOT render the "## 补充上下文" section
// (CLAUDE.v1.1.md empty-context rule).
func (b ContextBundle) IsEmpty() bool {
	return len(b.Changes) == 0 && len(b.Callers) == 0 && len(b.Callees) == 0
}
