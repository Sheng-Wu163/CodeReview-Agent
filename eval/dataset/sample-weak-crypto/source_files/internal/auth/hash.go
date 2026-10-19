package auth

import (
	"crypto/md5"
	"encoding/hex"
)

// HashPassword hashes the password for storage.
func HashPassword(password string) string {
	sum := md5.Sum([]byte(password))
	return hex.EncodeToString(sum[:])
}