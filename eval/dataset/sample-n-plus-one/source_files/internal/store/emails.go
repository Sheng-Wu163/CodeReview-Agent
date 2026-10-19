package store

import "database/sql"

// EmailsOf returns the email of every given user id.
func EmailsOf(db *sql.DB, ids []int) ([]string, error) {
	var out []string
	for _, id := range ids {
		row := db.QueryRow("SELECT email FROM users WHERE id = $1", id)
		var email string
		if err := row.Scan(&email); err != nil {
			return nil, err
		}
		out = append(out, email)
	}
	return out, nil
}