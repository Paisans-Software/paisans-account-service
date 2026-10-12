// Package emailhash keys a declined address so a database copy alone cannot
// test a guessed address against it.
package emailhash

import (
	"crypto/hmac"
	"crypto/sha256"
	"strings"
)

func Sum(key []byte, email string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(strings.ToLower(strings.TrimSpace(email))))
	return m.Sum(nil)
}
