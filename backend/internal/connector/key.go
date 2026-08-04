package connector

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// KeyFingerprint returns a stable, non-reversible identifier for an upstream key.
// NewAPI commonly exposes the same key as Bearer sk-xxx, sk-xxx, or xxx.
func KeyFingerprint(key string) string {
	key = strings.TrimSpace(key)
	if len(key) >= 7 && strings.EqualFold(key[:7], "bearer ") {
		key = strings.TrimSpace(key[7:])
	}
	key = strings.TrimPrefix(key, "sk-")
	if strings.TrimSpace(key) == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:])
}
