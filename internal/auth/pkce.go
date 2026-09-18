package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// GeneratePKCEPair creates an RFC 7636 compliant code_verifier and code_challenge.
func GeneratePKCEPair() (string, string, error) {
	bytes := make([]byte, 64)
	if _, err := rand.Read(bytes); err != nil {
		return "", "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	verifier := base64.RawURLEncoding.EncodeToString(bytes)
	if len(verifier) > 96 {
		verifier = verifier[:96]
	}

	h := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge, nil
}

// MaskToken masks a sensitive token string to prevent leakage in terminal or logs.
func MaskToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if len(token) <= 8 {
		return "***"
	}
	visibleStart := 6
	visibleEnd := 4
	if len(token) <= (visibleStart + visibleEnd + 3) {
		return fmt.Sprintf("%s...%s", token[:2], token[len(token)-2:])
	}
	return fmt.Sprintf("%s...%s", token[:visibleStart], token[len(token)-visibleEnd:])
}

// IsTokenExpired checks if expiresAt timestamp is in the past or within bufferSeconds.
func IsTokenExpired(expiresAt interface{}, bufferSeconds int64) bool {
	if expiresAt == nil {
		return false
	}
	var expTime time.Time
	switch v := expiresAt.(type) {
	case float64:
		expTime = time.Unix(int64(v), 0)
	case int64:
		expTime = time.Unix(v, 0)
	case int:
		expTime = time.Unix(int64(v), 0)
	case string:
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return false
		}
		expTime = t
	default:
		return false
	}

	return time.Now().Add(time.Duration(bufferSeconds) * time.Second).After(expTime)
}
