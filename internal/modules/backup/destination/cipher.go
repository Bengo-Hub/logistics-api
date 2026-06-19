package destination

import (
	"context"
	"crypto/sha256"
	"os"
)

// secretKeyCipher is a Cipher backed by a single 32-byte key derived from the
// process SECRET_KEY env var (sha256 of its value). logistics has no payment
// gateway KeyProvider, so this gives the destination Store the same AES-256-GCM
// encrypt/decrypt key material from the platform secret. The derived key is both
// the PrimaryKey (used to encrypt NEW data) and the sole CandidateKey.
type secretKeyCipher struct {
	key [32]byte
}

// NewSecretKeyCipher derives a 32-byte AES-256 key as sha256(SECRET_KEY). It is
// deterministic for a given SECRET_KEY, so configs encrypted by one replica
// decrypt on any other replica sharing the same secret. An empty SECRET_KEY still
// yields a (fixed) key so dev environments work, but production MUST set it.
func NewSecretKeyCipher() Cipher {
	return &secretKeyCipher{key: sha256.Sum256([]byte(os.Getenv("SECRET_KEY")))}
}

// PrimaryKey returns the 32-byte key used to encrypt NEW data.
func (c *secretKeyCipher) PrimaryKey(ctx context.Context) []byte {
	k := c.key
	return k[:]
}

// CandidateKeys returns the decryption keys in priority order — here, just the
// single SECRET_KEY-derived key.
func (c *secretKeyCipher) CandidateKeys(ctx context.Context) [][]byte {
	k := c.key
	return [][]byte{k[:]}
}
