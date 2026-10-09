package service

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword returns a bcrypt hash of plain. The cost is the bcrypt default
// (10), which is a sensible balance for a single-user / small-team app.
func HashPassword(plain string) (string, error) {
	if plain == "" {
		return "", fmt.Errorf("password must not be empty")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// VerifyPassword reports whether plain matches the stored bcrypt hash. It
// always returns false for an empty hash, even when plain is also empty, so
// that pre-migration rows (password_hash = "") can never log in.
func VerifyPassword(hash, plain string) bool {
	if hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// NewSessionToken returns a 32-byte random hex-encoded token (256 bits of
// entropy) suitable for use as an opaque session identifier.
func NewSessionToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}
