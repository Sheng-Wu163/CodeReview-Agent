package queue

// Take returns at most n pending ids.
func Take(pending []int, n int) []int {
	var out []int
	for _, id := range pending {
		if len(out) >= n {
			break
		}
		out = append(out, id)
	}
	return out
}