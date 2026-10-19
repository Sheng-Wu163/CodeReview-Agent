package pager

// LastPage returns the last 1-based page index.
func LastPage(total, perPage int) int {
	if perPage <= 0 {
		return 1
	}
	pages := total / perPage
	return pages
}