package diff

import (
	"reflect"
	"testing"
)

func TestParseAddedFile(t *testing.T) {
	input := `diff --git a/cmd/server/main.go b/cmd/server/main.go
new file mode 100644
index 0000000..83db48f
--- /dev/null
+++ b/cmd/server/main.go
@@ -0,0 +1,5 @@
+package main
+
+import "fmt"
+
+func main() {}
`
	got, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []FileDiff{
		{
			Path:       "cmd/server/main.go",
			ChangeType: ChangeAdded,
			Hunks: []Hunk{
				{
					OldStartLine: 0,
					OldLines:     0,
					NewStartLine: 1,
					NewLines:     5,
					Lines: []Line{
						{Type: LineAdded, Content: "package main", NewNumber: 1},
						{Type: LineAdded, Content: "", NewNumber: 2},
						{Type: LineAdded, Content: `import "fmt"`, NewNumber: 3},
						{Type: LineAdded, Content: "", NewNumber: 4},
						{Type: LineAdded, Content: "func main() {}", NewNumber: 5},
					},
				},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestParseModifiedFileMultipleHunks(t *testing.T) {
	input := `diff --git a/internal/config/config.go b/internal/config/config.go
index 5e40a37..2c47bf9 100644
--- a/internal/config/config.go
+++ b/internal/config/config.go
@@ -10,7 +10,8 @@ func Load() {
 	ServerPort: "8080",
-	WebhookSecret: os.Getenv("WEBHOOK_SECRET"),
+	WebhookSecret: os.Getenv("GH_WEBHOOK_SECRET"),
+	Timeout: 30,
 	GitHubToken: os.Getenv("GITHUB_TOKEN"),
 	DatabaseURL: "",
 }
@@ -40,6 +41,7 @@ func validate() error {
 	if err != nil {
 		return err
 	}
+	return validateURL(cfg.DatabaseURL)
 }
`
	got, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	f := got[0]
	if f.Path != "internal/config/config.go" || f.OldPath != f.Path {
		t.Errorf("paths = (%q, %q), want equal config paths", f.OldPath, f.Path)
	}
	if f.ChangeType != ChangeModified {
		t.Errorf("ChangeType = %v, want modified", f.ChangeType)
	}
	if len(f.Hunks) != 2 {
		t.Fatalf("hunks = %d, want 2", len(f.Hunks))
	}

	h0 := f.Hunks[0]
	if h0.OldStartLine != 10 || h0.OldLines != 7 || h0.NewStartLine != 10 || h0.NewLines != 8 {
		t.Errorf("hunk 0 header = %+v, want 10,7 10,8", h0)
	}
	wantLines0 := []Line{
		{Type: LineContext, Content: `	ServerPort: "8080",`, OldNumber: 10, NewNumber: 10},
		{Type: LineDeleted, Content: `	WebhookSecret: os.Getenv("WEBHOOK_SECRET"),`, OldNumber: 11},
		{Type: LineAdded, Content: `	WebhookSecret: os.Getenv("GH_WEBHOOK_SECRET"),`, NewNumber: 11},
		{Type: LineAdded, Content: "	Timeout: 30,", NewNumber: 12},
		{Type: LineContext, Content: `	GitHubToken: os.Getenv("GITHUB_TOKEN"),`, OldNumber: 12, NewNumber: 13},
		{Type: LineContext, Content: `	DatabaseURL: "",`, OldNumber: 13, NewNumber: 14},
		{Type: LineContext, Content: `}`, OldNumber: 14, NewNumber: 15},
	}
	if !reflect.DeepEqual(h0.Lines, wantLines0) {
		t.Errorf("hunk 0 lines mismatch:\n got %+v\nwant %+v", h0.Lines, wantLines0)
	}

	h1 := f.Hunks[1]
	if h1.OldStartLine != 40 || h1.NewStartLine != 41 {
		t.Errorf("hunk 1 start = (%d, %d), want (40, 41)", h1.OldStartLine, h1.NewStartLine)
	}
	wantLines1 := []Line{
		{Type: LineContext, Content: `	if err != nil {`, OldNumber: 40, NewNumber: 41},
		{Type: LineContext, Content: `		return err`, OldNumber: 41, NewNumber: 42},
		{Type: LineContext, Content: `	}`, OldNumber: 42, NewNumber: 43},
		{Type: LineAdded, Content: `	return validateURL(cfg.DatabaseURL)`, NewNumber: 44},
		{Type: LineContext, Content: `}`, OldNumber: 43, NewNumber: 45},
	}
	if !reflect.DeepEqual(h1.Lines, wantLines1) {
		t.Errorf("hunk 1 lines mismatch:\n got %+v\nwant %+v", h1.Lines, wantLines1)
	}
}

func TestParseDeletedFile(t *testing.T) {
	input := `diff --git a/legacy/old.go b/legacy/old.go
deleted file mode 100644
index 83db48f..0000000
--- a/legacy/old.go
+++ /dev/null
@@ -1,3 +0,0 @@
-package legacy
-
-func Old() {}
`
	got, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	f := got[0]
	if f.Path != "" {
		t.Errorf("Path = %q, want empty (deleted file)", f.Path)
	}
	if f.OldPath != "legacy/old.go" {
		t.Errorf("OldPath = %q, want legacy/old.go", f.OldPath)
	}
	if f.ChangeType != ChangeDeleted {
		t.Errorf("ChangeType = %v, want deleted", f.ChangeType)
	}
	if len(f.Hunks) != 1 || len(f.Hunks[0].Lines) != 3 {
		t.Fatalf("hunks = %+v, want 1 hunk with 3 deleted lines", f.Hunks)
	}
	for i, l := range f.Hunks[0].Lines {
		if l.Type != LineDeleted || l.OldNumber != i+1 {
			t.Errorf("line %d = %+v, want deleted at old line %d", i, l, i+1)
		}
	}
}

func TestParseBinaryFile(t *testing.T) {
	input := `diff --git a/assets/logo.png b/assets/logo.png
new file mode 100644
index 0000000..e69de29
Binary files /dev/null and b/assets/logo.png differ
`
	got, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 1 || !got[0].IsBinary {
		t.Fatalf("got %+v, want 1 binary file", got)
	}
	if len(got[0].Hunks) != 0 {
		t.Errorf("binary file should have no hunks, got %d", len(got[0].Hunks))
	}
}

func TestParseMultipleFiles(t *testing.T) {
	input := `diff --git a/README.md b/README.md
index 83db48f..2c47bf9 100644
--- a/README.md
+++ b/README.md
@@ -1,3 +1,4 @@
 # CodeReviewAgent
+Some documentation line.

 Use it.
diff --git a/vendor/github.com/foo/bar.go b/vendor/github.com/foo/bar.go
deleted file mode 100644
index 83db48f..0000000
--- a/vendor/github.com/foo/bar.go
+++ /dev/null
@@ -1 +0,0 @@
-package bar
diff --git a/main.go b/main.go
new file mode 100644
index 0000000..2c47bf9
--- /dev/null
+++ b/main.go
@@ -0,0 +1 @@
+package main
`
	got, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}
	if got[0].ChangeType != ChangeModified || got[0].Path != "README.md" {
		t.Errorf("file 0 = %v/%v, want modified README.md", got[0].ChangeType, got[0].Path)
	}
	if got[1].ChangeType != ChangeDeleted || got[1].OldPath != "vendor/github.com/foo/bar.go" {
		t.Errorf("file 1 = %v/%v, want deleted vendor file", got[1].ChangeType, got[1].OldPath)
	}
	if got[2].ChangeType != ChangeAdded || got[2].Path != "main.go" {
		t.Errorf("file 2 = %v/%v, want added main.go", got[2].ChangeType, got[2].Path)
	}
}

func TestParseModeOnlyChange(t *testing.T) {
	input := `diff --git a/scripts/run.sh b/scripts/run.sh
old mode 100644
new mode 100755
`
	got, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	f := got[0]
	if f.Path != "scripts/run.sh" || f.OldPath != "scripts/run.sh" {
		t.Errorf("paths = (%q, %q), want scripts/run.sh", f.OldPath, f.Path)
	}
	if f.ChangeType != ChangeModified {
		t.Errorf("ChangeType = %v, want modified", f.ChangeType)
	}
	if len(f.Hunks) != 0 {
		t.Errorf("hunks = %d, want 0 (mode-only change)", len(f.Hunks))
	}
}

func TestParseRenamedFile(t *testing.T) {
	input := `diff --git a/old_name.go b/new_name.go
similarity index 95%
rename from old_name.go
rename to new_name.go
index 83db48f..2c47bf9 100644
--- a/old_name.go
+++ b/new_name.go
@@ -1,3 +1,3 @@
 package main
-context line
+changed line
`
	got, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	f := got[0]
	if f.ChangeType != ChangeRenamed {
		t.Errorf("ChangeType = %v, want renamed", f.ChangeType)
	}
	if f.OldPath != "old_name.go" || f.Path != "new_name.go" {
		t.Errorf("paths = (%q, %q), want (old_name.go, new_name.go)", f.OldPath, f.Path)
	}
	if len(f.Hunks) != 1 {
		t.Fatalf("hunks = %d, want 1", len(f.Hunks))
	}
}

func TestParseNoNewlineMarker(t *testing.T) {
	input := "diff --git a/x.txt b/x.txt\n" +
		"index 83db48f..2c47bf9 100644\n" +
		"--- a/x.txt\n" +
		"+++ b/x.txt\n" +
		"@@ -1 +1 @@\n" +
		"-old line\n" +
		"\\ No newline at end of file\n" +
		"+new line\n" +
		"\\ No newline at end of file\n"
	got, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	lines := got[0].Hunks[0].Lines
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2 (no-newline markers must be skipped)", len(lines))
	}
	if lines[0].Type != LineDeleted || lines[1].Type != LineAdded {
		t.Errorf("types = %v, %v, want deleted, added", lines[0].Type, lines[1].Type)
	}
}

func TestParseEmptyInput(t *testing.T) {
	got, err := Parse("")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0", len(got))
	}
}

func TestParseMalformedHunkHeader(t *testing.T) {
	input := `diff --git a/x.go b/x.go
index 83db48f..2c47bf9 100644
--- a/x.go
+++ b/x.go
@@ -not-a-number @@
+line
`
	if _, err := Parse(input); err == nil {
		t.Error("Parse should fail on malformed hunk header")
	}
}

func TestHasChanges(t *testing.T) {
	tests := []struct {
		name string
		file FileDiff
		want bool
	}{
		{
			name: "context only",
			file: FileDiff{Hunks: []Hunk{{Lines: []Line{{Type: LineContext}}}}},
			want: false,
		},
		{
			name: "has added line",
			file: FileDiff{Hunks: []Hunk{{Lines: []Line{{Type: LineAdded}}}}},
			want: true,
		},
		{
			name: "has deleted line",
			file: FileDiff{Hunks: []Hunk{{Lines: []Line{{Type: LineDeleted}}}}},
			want: true,
		},
		{
			name: "no hunks",
			file: FileDiff{},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.file.HasChanges(); got != tt.want {
				t.Errorf("HasChanges() = %v, want %v", got, tt.want)
			}
		})
	}
}
