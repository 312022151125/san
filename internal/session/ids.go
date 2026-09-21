package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

func generateSessionID() string {
	var uuid [16]byte
	_, _ = rand.Read(uuid[:])
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	uuid[8] = (uuid[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:])
}

func NewSessionID() string {
	return generateSessionID()
}

// GenerateTestSessionID returns a deterministic synthetic session ID for
// test/debug use. Production uses NewSessionID.
// ponytail: test/debug-only synthetic IDs; production uses NewSessionID.
// Ceiling: 26-char ses_ shape only — upstream's 64-hex promptCacheKey strip
// does not apply.
func GenerateTestSessionID(id string) string {
	sum := sha256.Sum256([]byte(id))
	hexPart := hex.EncodeToString(sum[0:6]) // 12 hex chars
	const chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	tail := make([]byte, 14)
	for i := range tail {
		tail[i] = chars[sum[6+i]%62] // first 14 of sum[6:22]; 6+14=20 <= 32, in bounds
	}
	return "ses_" + hexPart + string(tail)
}
