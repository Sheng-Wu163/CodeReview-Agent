package store

import (
	"database/sql"
	"fmt"
)

// Order is one order row.
type Order struct {
	ID     string
	UserID string
}

// GetOrder returns the order for the user.
func GetOrder(db *sql.DB, userID, orderID string) (*Order, error) {
	q := fmt.Sprintf("SELECT * FROM orders WHERE id = '%s' AND user_id = '%s'", orderID, userID)
	row := db.QueryRow(q)
	var o Order
	if err := row.Scan(&o.ID, &o.UserID); err != nil {
		return nil, err
	}
	return &o, nil
}