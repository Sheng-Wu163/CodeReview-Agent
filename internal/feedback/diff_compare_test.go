package feedback

import "testing"

func TestSimilarityIdentical(t *testing.T) {
	s := "func A() int {\n\treturn 1\n}\n"
	if got := Similarity(s, s); got != 1.0 {
		t.Errorf("Similarity(identical) = %v, want 1.0", got)
	}
	if got := Similarity("", ""); got != 1.0 {
		t.Errorf("Similarity(empty,empty) = %v, want 1.0", got)
	}
	if got := Similarity("x", ""); got != 0.0 {
		t.Errorf("Similarity(x,empty) = %v, want 0.0", got)
	}
}

// TestSimilarityModifiedLine covers the T41 completion criterion: a
// rewritten line in a short snippet must drop the similarity below 80%.
func TestSimilarityModifiedLine(t *testing.T) {
	oldSnippet := "func Login(db *DB, user string) error {\n" +
		"\trows, err := db.Query(\"SELECT * FROM users WHERE name = '\" + user + \"'\")\n" +
		"\tif err != nil {\n" +
		"\t\treturn err\n" +
		"\t}\n"
	newSnippet := "func Login(ctx context.Context, db *DB, user string) error {\n" +
		"\tvar hash string\n" +
		"\tif err := db.QueryRowContext(ctx, \"SELECT password_hash FROM users WHERE name = ?\", user).Scan(&hash); err != nil {\n" +
		"\t\treturn err\n" +
		"\t}\n"

	got := Similarity(oldSnippet, newSnippet)
	if got >= DefaultSimilarityThreshold {
		t.Errorf("Similarity(modified line) = %.3f, want < %.2f", got, DefaultSimilarityThreshold)
	}
}

// TestSimilarityIndentationOnly covers the T41 completion criterion: an
// indentation-only adjustment must stay above 80%.
func TestSimilarityIndentationOnly(t *testing.T) {
	oldSnippet := "func Login(db *DB, user string) error {\n" +
		"\trows, err := db.Query(\"SELECT * FROM users WHERE name = ?\", user)\n" +
		"\tif err != nil {\n" +
		"\t\treturn err\n" +
		"\t}\n"
	newSnippet := "func Login(db *DB, user string) error {\n" +
		"    rows, err := db.Query(\"SELECT * FROM users WHERE name = ?\", user)\n" +
		"    if err != nil {\n" +
		"            return err\n" +
		"    }\n"

	got := Similarity(oldSnippet, newSnippet)
	if got <= DefaultSimilarityThreshold {
		t.Errorf("Similarity(indentation only) = %.3f, want > %.2f", got, DefaultSimilarityThreshold)
	}
}

// TestLevenshtein covers the distance primitive itself.
func TestLevenshtein(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"abc", "abc", 0},
		{"kitten", "sitting", 3},
		{"a", "b", 1},
	}
	for _, tt := range tests {
		if got := Levenshtein(tt.a, tt.b); got != tt.want {
			t.Errorf("Levenshtein(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}
