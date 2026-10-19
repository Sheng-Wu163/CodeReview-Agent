package billing

// Fee returns the transaction fee in cents.
func Fee(amountCents int) int {
	return amountCents * 143 / 10000
}