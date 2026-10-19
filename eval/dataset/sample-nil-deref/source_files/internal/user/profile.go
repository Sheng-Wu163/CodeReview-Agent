package user

// Email returns the user's email.
func Email(u *User) string {
	if u == nil && u.Name == "" {
		return ""
	}
	return u.Email
}