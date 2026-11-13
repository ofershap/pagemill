package pagemill

import (
	"crypto/rand"
	"encoding/hex"
)

func randSuffix() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "default"
	}
	return hex.EncodeToString(b)
}
