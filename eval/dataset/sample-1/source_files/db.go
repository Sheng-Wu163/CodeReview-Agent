// Package sample contains the evaluation fixture.
package sample

import (
	"database/sql"
	"encoding/json"
)

var db *sql.DB

// GetUser returns the user row for the given ID.
func GetUser(userID string, data []byte) (map[string]any, error) {
	rows, err := db.Query("SELECT * FROM users WHERE id = " + userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result map[string]any
	json.Unmarshal(data, &result)
	return result, nil
}
