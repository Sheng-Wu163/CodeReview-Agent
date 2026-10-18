package feedback

// DefaultSimilarityThreshold is the fallback similarity threshold
// (CLAUDE.v.1.4.md): a new snippet whose similarity to the old one is
// strictly below this value is considered modified. Configurable via
// feedback.similarity_threshold.
const DefaultSimilarityThreshold = 0.8

// Similarity scores how similar two local code snippets are, in [0,1]:
// it is 1 minus the normalized Levenshtein edit distance. It is a pure
// function with no I/O (CLAUDE.v.1.4.md).
//
// A snippet pair that differs only in indentation scores close to 1, while
// a rewritten line in a short snippet drops well below 1 (FR-42).
func Similarity(oldSnippet, newSnippet string) float64 {
	if oldSnippet == "" && newSnippet == "" {
		return 1
	}
	if oldSnippet == "" || newSnippet == "" {
		return 0
	}
	maxLen := len(oldSnippet)
	if len(newSnippet) > maxLen {
		maxLen = len(newSnippet)
	}
	dist := Levenshtein(oldSnippet, newSnippet)
	return float64(maxLen-dist) / float64(maxLen)
}

// Levenshtein computes the edit distance between two strings (rune aware,
// pure function, T41).
func Levenshtein(a, b string) int {
	ar := []rune(a)
	br := []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}

	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := 0; j <= len(br); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}
