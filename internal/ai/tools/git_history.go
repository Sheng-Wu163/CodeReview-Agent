package tools

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/tmc/langchaingo/tools"
)

const (
	// maxBlameLines caps a requested git_blame range (v1.14 FR-78).
	maxBlameLines = 500
	// maxLogCommits caps git_log_file output (v1.14 FR-78).
	maxLogCommits = 20
)

// GitBlameTool returns line authorship for a tracked file using go-git. It
// never shells out to git (v1.14 FR-78).
type GitBlameTool struct {
	// Root is the review workspace (a git worktree).
	Root string
	// MaxOutputChars caps the returned text; zero uses
	// DefaultToolOutputHardLimit.
	MaxOutputChars int
}

// Compile-time check that GitBlameTool implements tools.Tool.
var _ tools.Tool = GitBlameTool{}

// Name implements tools.Tool.
func (t GitBlameTool) Name() string { return "git_blame" }

// Description implements tools.Tool.
func (t GitBlameTool) Description() string {
	return "Show git authorship for a tracked file. Input: \"<path>\" for the whole file, " +
		"or \"<path>:<start_line>:<end_line>\" for a 1-based inclusive range of at most 500 lines. " +
		"Example: \"internal/config/config.go:10:40\""
}

// Call implements tools.Tool.
func (t GitBlameTool) Call(_ context.Context, input string) (string, error) {
	rawPath, start, end, err := parseGitRangeInput(input)
	if err != nil {
		return "", fmt.Errorf("git_blame: %w", err)
	}
	repo, rel, err := openRepoPath(t.Root, rawPath)
	if err != nil {
		return "", fmt.Errorf("git_blame: %w", err)
	}
	head, err := repo.Head()
	if err != nil {
		return "", fmt.Errorf("git_blame: resolve HEAD: %w", err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return "", fmt.Errorf("git_blame: load HEAD commit: %w", err)
	}
	blame, err := git.Blame(commit, rel)
	if err != nil {
		return "", fmt.Errorf("git_blame: %q is not tracked in git history: %w", rel, err)
	}
	if end == 0 {
		end = len(blame.Lines)
	}
	start, end, err = clampBlameRange(start, end, len(blame.Lines))
	if err != nil {
		return "", fmt.Errorf("git_blame: %w", err)
	}

	var b strings.Builder
	for i := start; i <= end; i++ {
		line := blame.Lines[i-1]
		hash := line.Hash.String()
		if len(hash) > 12 {
			hash = hash[:12]
		}
		fmt.Fprintf(&b, "%s:%d: %s %s %s %s\n",
			rel, i, hash, line.AuthorName, line.Date.Format(time.RFC3339), line.Text)
	}
	return limitOutput(b.String(), t.MaxOutputChars), nil
}

// GitLogFileTool returns the commit history of one tracked file using
// go-git (v1.14 FR-78).
type GitLogFileTool struct {
	// Root is the review workspace (a git worktree).
	Root string
	// MaxOutputChars caps the returned text; zero uses
	// DefaultToolOutputHardLimit.
	MaxOutputChars int
}

// Compile-time check that GitLogFileTool implements tools.Tool.
var _ tools.Tool = GitLogFileTool{}

// Name implements tools.Tool.
func (t GitLogFileTool) Name() string { return "git_log_file" }

// Description implements tools.Tool.
func (t GitLogFileTool) Description() string {
	return "Show recent commits that touched a tracked file. Input: \"<path>\" or " +
		"\"<path>:<limit>\"; limit defaults to 20 and is capped at 20. " +
		"Example: \"internal/harness/harness_impl.go:5\""
}

// Call implements tools.Tool.
func (t GitLogFileTool) Call(_ context.Context, input string) (string, error) {
	rawPath, limit, err := parseGitLogInput(input)
	if err != nil {
		return "", fmt.Errorf("git_log_file: %w", err)
	}
	repo, rel, err := openRepoPath(t.Root, rawPath)
	if err != nil {
		return "", fmt.Errorf("git_log_file: %w", err)
	}
	iter, err := repo.Log(&git.LogOptions{FileName: &rel})
	if err != nil {
		return "", fmt.Errorf("git_log_file: read history for %q: %w", rel, err)
	}
	defer iter.Close()

	var b strings.Builder
	count := 0
	for count < limit {
		commit, err := iter.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("git_log_file: read history for %q: %w", rel, err)
		}
		fmt.Fprintf(&b, "%s %s %s: %s\n",
			shortHash(commit.Hash.String()),
			commit.Author.When.Format(time.RFC3339),
			commit.Author.Name,
			firstLine(commit.Message))
		count++
	}
	if count == 0 {
		return "no history found", nil
	}
	return limitOutput(b.String(), t.MaxOutputChars), nil
}

// parseGitRangeInput parses "<path>" or "<path>:<start>:<end>" and rejects
// option-like paths before any filesystem work (v1.14 FR-78/AC-148).
func parseGitRangeInput(input string) (string, int, int, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", 0, 0, fmt.Errorf("empty input")
	}
	if strings.HasPrefix(trimmed, "-") {
		return "", 0, 0, fmt.Errorf("path must not start with '-': %q", trimmed)
	}
	parts := strings.Split(trimmed, ":")
	if len(parts) >= 3 {
		start, startErr := strconv.Atoi(parts[len(parts)-2])
		end, endErr := strconv.Atoi(parts[len(parts)-1])
		if startErr == nil && endErr == nil {
			rawPath := strings.Join(parts[:len(parts)-2], ":")
			if strings.HasPrefix(rawPath, "-") {
				return "", 0, 0, fmt.Errorf("path must not start with '-': %q", rawPath)
			}
			if start < 1 || end < start {
				return "", 0, 0, fmt.Errorf("invalid line range %d:%d", start, end)
			}
			if end-start+1 > maxBlameLines {
				return "", 0, 0, fmt.Errorf("line range %d:%d exceeds the %d-line limit", start, end, maxBlameLines)
			}
			return rawPath, start, end, nil
		}
	}
	return trimmed, 1, 0, nil
}

// parseGitLogInput parses "<path>" or "<path>:<limit>" and caps limit at 20.
func parseGitLogInput(input string) (string, int, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", 0, fmt.Errorf("empty input")
	}
	if strings.HasPrefix(trimmed, "-") {
		return "", 0, fmt.Errorf("path must not start with '-': %q", trimmed)
	}
	limit := maxLogCommits
	if idx := strings.LastIndex(trimmed, ":"); idx > 0 {
		if parsed, err := strconv.Atoi(trimmed[idx+1:]); err == nil {
			if parsed < 1 {
				return "", 0, fmt.Errorf("limit must be positive")
			}
			if parsed > maxLogCommits {
				parsed = maxLogCommits
			}
			limit = parsed
			trimmed = trimmed[:idx]
		}
	}
	if strings.HasPrefix(trimmed, "-") {
		return "", 0, fmt.Errorf("path must not start with '-': %q", trimmed)
	}
	return trimmed, limit, nil
}

// clampBlameRange validates the requested blame window against the file
// length, enforcing the 500-line cap.
func clampBlameRange(start, end, total int) (int, int, error) {
	if total == 0 {
		return 0, 0, fmt.Errorf("file has no blame lines")
	}
	if start < 1 || end < start {
		return 0, 0, fmt.Errorf("invalid line range %d:%d", start, end)
	}
	if end > total {
		end = total
	}
	if start > total {
		return 0, 0, fmt.Errorf("start line %d is beyond the file's %d lines", start, total)
	}
	if end-start+1 > maxBlameLines {
		return 0, 0, fmt.Errorf("line range exceeds the %d-line limit", maxBlameLines)
	}
	return start, end, nil
}

// openRepoPath validates a workspace-relative path and resolves it to a path
// relative to the enclosing git worktree.
func openRepoPath(root, rel string) (*git.Repository, string, error) {
	full, err := resolvePath(root, rel)
	if err != nil {
		return nil, "", err
	}
	repo, err := git.PlainOpen(root)
	if err != nil {
		return nil, "", fmt.Errorf("open git repository: %w", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		return nil, "", fmt.Errorf("open git worktree: %w", err)
	}
	repoRoot := worktree.Filesystem.Root()
	repoRel, err := filepath.Rel(repoRoot, full)
	if err != nil {
		return nil, "", fmt.Errorf("resolve repository path: %w", err)
	}
	repoRel = filepath.ToSlash(repoRel)
	if strings.HasPrefix(repoRel, "../") || repoRel == ".." {
		return nil, "", fmt.Errorf("path %q escapes the repository", rel)
	}
	return repo, repoRel, nil
}

// shortHash truncates a commit hash for display.
func shortHash(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

// firstLine returns the first line of a commit message.
func firstLine(message string) string {
	if idx := strings.IndexByte(message, '\n'); idx >= 0 {
		message = message[:idx]
	}
	return strings.TrimSpace(message)
}
