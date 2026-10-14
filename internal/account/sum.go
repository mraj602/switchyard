package account

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
)

// fileSum is the hex SHA-256 of a launcher file's content.
func fileSum(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
