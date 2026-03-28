// Package crypto provides Ed25519 key generation, transaction signing, and verification.
// Ed25519 is chosen for its speed, small key/signature size, and resistance to side-channel attacks.
//
// Signing model (CRITICAL-2):
//   - SignSighash signs a precomputed sighash (the hash of the tx structure).
//   - VerifySighash verifies a signature against a sighash and public key.
//   - Legacy SignTransaction/Verify are preserved for backward compatibility but
//     new code should use the sighash-based functions.
package crypto

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// KeyPair holds a public/private Ed25519 key pair.
// The Address is the hex-encoded public key used to identify wallets.
type KeyPair struct {
	PrivateKey ed25519.PrivateKey `json:"-"`         // never serialized
	PublicKey  ed25519.PublicKey  `json:"public_key"` // 32-byte public key
	Address    string            `json:"address"`     // hex-encoded public key
}

// GenerateKeyPair creates a new Ed25519 key pair.
// Returns an error if the system random source fails.
func GenerateKeyPair() (*KeyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("key generation failed: %w", err)
	}
	return &KeyPair{
		PrivateKey: priv,
		PublicKey:  pub,
		Address:    hex.EncodeToString(pub),
	}, nil
}

// Sign produces an Ed25519 signature over the given message bytes.
// The signature is returned as a hex-encoded string.
func Sign(privateKey ed25519.PrivateKey, message []byte) string {
	sig := ed25519.Sign(privateKey, message)
	return hex.EncodeToString(sig)
}

// SignSighash signs a transaction sighash using a hex-encoded private key.
// The sighash should be computed via block.ComputeSighash().
// Returns the hex-encoded signature or an error.
func SignSighash(privateKeyHex string, sighash []byte) (string, error) {
	privBytes, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		return "", fmt.Errorf("invalid private key hex: %w", err)
	}
	if len(privBytes) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("invalid private key length: got %d bytes, want %d", len(privBytes), ed25519.PrivateKeySize)
	}

	priv := ed25519.PrivateKey(privBytes)
	sig := ed25519.Sign(priv, sighash)
	return hex.EncodeToString(sig), nil
}

// VerifySighash checks an Ed25519 signature against a sighash and hex-encoded public key.
// Returns true only when the signature is valid.
func VerifySighash(pubKeyHex string, sighash []byte, signatureHex string) bool {
	pubBytes, err := hex.DecodeString(pubKeyHex)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		return false
	}
	sigBytes, err := hex.DecodeString(signatureHex)
	if err != nil || len(sigBytes) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pubBytes), sighash, sigBytes)
}

// SignTransaction signs a transaction using a hex-encoded private key.
// The signed message format is "from:to:amount" — matching the legacy verification logic.
// DEPRECATED: Use SignSighash with block.ComputeSighash for new UTXO transactions.
func SignTransaction(privateKeyHex, from, to string, amount float64) (string, error) {
	privBytes, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		return "", fmt.Errorf("invalid private key hex: %w", err)
	}
	if len(privBytes) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("invalid private key length: got %d bytes, want %d", len(privBytes), ed25519.PrivateKeySize)
	}

	// Build the same message format used by ledger.ProcessTransaction for verification.
	msg := fmt.Sprintf("%s:%s:%f", from, to, amount)
	priv := ed25519.PrivateKey(privBytes)
	sig := ed25519.Sign(priv, []byte(msg))
	return hex.EncodeToString(sig), nil
}

// Verify checks an Ed25519 signature against a message and hex-encoded public key.
// Returns true only when the signature is valid.
func Verify(pubKeyHex string, message []byte, signatureHex string) bool {
	pubBytes, err := hex.DecodeString(pubKeyHex)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		return false
	}
	sigBytes, err := hex.DecodeString(signatureHex)
	if err != nil || len(sigBytes) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pubBytes), message, sigBytes)
}

// AddressFromPrivateKey extracts the hex-encoded public key (address)
// from a hex-encoded private key. Useful for deriving "from" automatically.
func AddressFromPrivateKey(privateKeyHex string) (string, error) {
	privBytes, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		return "", fmt.Errorf("invalid private key hex: %w", err)
	}
	if len(privBytes) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("invalid private key length: got %d bytes, want %d", len(privBytes), ed25519.PrivateKeySize)
	}
	priv := ed25519.PrivateKey(privBytes)
	pub := priv.Public().(ed25519.PublicKey)
	return hex.EncodeToString(pub), nil
}
