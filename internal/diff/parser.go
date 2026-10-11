package diff

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// hunkHeaderRe matches unified diff hunk headers, e.g.
// "@@ -12,5 +12,7 @@ optional section heading". The line counts are optional
// ("@@ -1 +1 @@") and default to 1.
var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// Parse parses a unified diff text (e.g. the body of a GitHub PR .diff) into
// a structured list of FileDiff, one entry per file.
func Parse(input string) ([]FileDiff, error) {
	return ParseReader(strings.NewReader(input))
}

// ParseReader parses unified diff content read from r. See Parse.
func ParseReader(r io.Reader) ([]FileDiff, error) {
	scanner := bufio.NewScanner(r)
	// Diffs for large PRs can exceed the default 64 KiB line buffer.
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	var files []FileDiff
	var current *FileDiff
	var hunk *Hunk

	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")

		switch {
		case strings.HasPrefix(line, "diff --git "):
			current = &FileDiff{}
			if a, b, ok := parseDiffGitHeader(line); ok {
				current.OldPath, current.Path = a, b
			}
			hunk = nil
			files = append(files, *current)
			current = &files[len(files)-1]

		case current == nil:
			// Content before the first "diff --git" header; GitHub PR diffs
			// never start this way, so tolerate and skip.

		case strings.HasPrefix(line, "new file mode"):
			current.ChangeType = ChangeAdded
			current.OldPath = ""

		case strings.HasPrefix(line, "deleted file mode"):
			current.ChangeType = ChangeDeleted

		case strings.HasPrefix(line, "rename from "):
			current.ChangeType = ChangeRenamed
			current.OldPath = unquotePath(strings.TrimPrefix(line, "rename from "))

		case strings.HasPrefix(line, "rename to "):
			current.Path = unquotePath(strings.TrimPrefix(line, "rename to "))

		case strings.HasPrefix(line, "GIT binary patch"),
			strings.HasPrefix(line, "Binary files "):
			current.IsBinary = true
			hunk = nil

		case strings.HasPrefix(line, "--- "):
			p := unquotePath(strings.TrimPrefix(line, "--- "))
			if p != "/dev/null" {
				p = strings.TrimPrefix(p, "a/")
				if current.ChangeType == ChangeAdded {
					// Old side of an added file must be empty; a "--- a/..."
					// here would contradict "new file mode".
					return nil, fmt.Errorf("inconsistent diff: added file with old path %q", p)
				}
				current.OldPath = p
				if current.Path == "" {
					current.Path = p
				}
			}

		case strings.HasPrefix(line, "+++ "):
			p := unquotePath(strings.TrimPrefix(line, "+++ "))
			if p == "/dev/null" {
				// New side of a deleted file: the file no longer exists.
				current.Path = ""
				break
			}
			current.Path = strings.TrimPrefix(p, "b/")

		case strings.HasPrefix(line, "@@ "):
			h := parseHunkHeader(line)
			if h == nil {
				return nil, fmt.Errorf("malformed hunk header: %q", line)
			}
			current.Hunks = append(current.Hunks, *h)
			hunk = &current.Hunks[len(current.Hunks)-1]

		case hunk != nil && (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") ||
			strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\\")):
			if strings.HasPrefix(line, "\\") {
				// "\ No newline at end of file" marker; nothing to record.
				break
			}
			hunk.Lines = append(hunk.Lines, parseContentLine(line, hunk))
			// Keep the slice header in current.Hunks in sync after append.
			current.Hunks[len(current.Hunks)-1] = *hunk

		default:
			// "index", "old mode", "new mode", "similarity index" lines and
			// anything else carry no information we need.
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan diff input: %w", err)
	}
	return files, nil
}

// parseContentLine converts one hunk body line into a Line, tracking both
// file version line numbers.
func parseContentLine(line string, hunk *Hunk) Line {
	// Line numbers so far (lines already consumed within this hunk).
	oldNo, newNo := hunk.OldStartLine, hunk.NewStartLine
	for _, l := range hunk.Lines {
		switch l.Type {
		case LineContext:
			oldNo++
			newNo++
		case LineAdded:
			newNo++
		case LineDeleted:
			oldNo++
		}
	}

	prefix, content := line[0], line[1:]
	switch prefix {
	case '+':
		return Line{Type: LineAdded, Content: content, NewNumber: newNo}
	case '-':
		return Line{Type: LineDeleted, Content: content, OldNumber: oldNo}
	default:
		return Line{Type: LineContext, Content: content, OldNumber: oldNo, NewNumber: newNo}
	}
}

// parseHunkHeader parses "@@ -o[,ol] +n[,nl] @@..." into a Hunk. It returns
// nil when the header does not match the unified diff format.
func parseHunkHeader(line string) *Hunk {
	m := hunkHeaderRe.FindStringSubmatch(line)
	if m == nil {
		return nil
	}
	oldStart, _ := strconv.Atoi(m[1])
	oldLines := 1
	if m[2] != "" {
		oldLines, _ = strconv.Atoi(m[2])
	}
	newStart, _ := strconv.Atoi(m[3])
	newLines := 1
	if m[4] != "" {
		newLines, _ = strconv.Atoi(m[4])
	}
	return &Hunk{
		OldStartLine: oldStart,
		OldLines:     oldLines,
		NewStartLine: newStart,
		NewLines:     newLines,
	}
}

// parseDiffGitHeader extracts the old and new paths from a
// "diff --git a/x b/x" header line. Paths may be quoted when they contain
// spaces. It reports ok=false for malformed headers.
func parseDiffGitHeader(line string) (oldPath, newPath string, ok bool) {
	rest := strings.TrimPrefix(line, "diff --git ")
	if rest == line {
		return "", "", false
	}
	if strings.HasPrefix(rest, `"a/`) {
		// Quoted paths: "a/foo bar" "b/foo bar".
		idx := strings.Index(rest, `" "b/`)
		if idx < 0 {
			return "", "", false
		}
		return strings.TrimPrefix(unquotePath(rest[:idx+1]), "a/"),
			strings.TrimPrefix(unquotePath(rest[idx+3:]), "b/"), true
	}
	sep := strings.Index(rest, " b/")
	if sep < 0 {
		return "", "", false
	}
	return strings.TrimPrefix(unquotePath(rest[:sep]), "a/"),
		strings.TrimPrefix(unquotePath(rest[sep+3:]), "b/"), true
}

// unquotePath removes surrounding double quotes and unescapes backslash
// escapes used by git for paths with special characters.
func unquotePath(p string) string {
	if len(p) >= 2 && strings.HasPrefix(p, `"`) && strings.HasSuffix(p, `"`) {
		p = p[1 : len(p)-1]
		p = strings.ReplaceAll(p, `\"`, `"`)
		p = strings.ReplaceAll(p, `\\`, `\`)
	}
	return p
}
