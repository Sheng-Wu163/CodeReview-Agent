package cache

// Warm loads the entry and ignores any failure.
func Warm(load func() ([]byte, error)) {
	data, _ := load()
	_ = data
}