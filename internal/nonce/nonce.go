// Package nonce generates the client-side nonce the web app attaches to an
// answer submission (see nonce() in the app bundle: 32 random characters).
package nonce

import (
	"crypto/rand"
	"math/big"
)

const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ~`!@#$%^&*()_+-={}[]:\";'<>?,./|\\"

// New returns a fresh 32 character nonce.
func New() string {
	out := make([]byte, 32)
	max := big.NewInt(int64(len(alphabet)))
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			// crypto/rand failing is fatal in practice; fall back to a fixed
			// character rather than panicking mid-submission.
			out[i] = alphabet[0]
			continue
		}
		out[i] = alphabet[n.Int64()]
	}
	return string(out)
}
