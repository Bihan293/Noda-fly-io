package crypto

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
)

// TestGenerateKeyPair verifies key pair generation produces valid Ed25519 keys.
func TestGenerateKeyPair(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair() error: %v", err)
	}
	if kp == nil {
		t.Fatal("GenerateKeyPair() returned nil")
	}

	// Check private key length.
	if len(kp.PrivateKey) != ed25519.PrivateKeySize {
		t.Errorf("PrivateKey length = %d, want %d", len(kp.PrivateKey), ed25519.PrivateKeySize)
	}

	// Check public key length.
	if len(kp.PublicKey) != ed25519.PublicKeySize {
		t.Errorf("PublicKey length = %d, want %d", len(kp.PublicKey), ed25519.PublicKeySize)
	}

	// Address must be hex-encoded public key.
	if kp.Address != hex.EncodeToString(kp.PublicKey) {
		t.Errorf("Address = %s, want hex of PublicKey", kp.Address)
	}

	// Address must be 64 hex characters (32 bytes).
	if len(kp.Address) != 64 {
		t.Errorf("Address length = %d, want 64", len(kp.Address))
	}
}

// TestGenerateKeyPairUniqueness ensures each call produces different keys.
func TestGenerateKeyPairUniqueness(t *testing.T) {
	kp1, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("first GenerateKeyPair() error: %v", err)
	}
	kp2, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("second GenerateKeyPair() error: %v", err)
	}
	if kp1.Address == kp2.Address {
		t.Error("two generated key pairs have the same address")
	}
}

// TestSignAndVerify checks the sign/verify round-trip.
func TestSignAndVerify(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair() error: %v", err)
	}

	message := []byte("test message for signing")
	sigHex := Sign(kp.PrivateKey, message)

	if sigHex == "" {
		t.Fatal("Sign() returned empty string")
	}

	// Signature must be valid hex.
	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil {
		t.Fatalf("Sign() returned invalid hex: %v", err)
	}
	if len(sigBytes) != ed25519.SignatureSize {
		t.Errorf("signature length = %d, want %d", len(sigBytes), ed25519.SignatureSize)
	}

	// Verify should succeed.
	if !Verify(kp.Address, message, sigHex) {
		t.Error("Verify() returned false for valid signature")
	}
}

// TestVerifyFailsWithWrongMessage ensures verification fails with tampered message.
func TestVerifyFailsWithWrongMessage(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair() error: %v", err)
	}

	sigHex := Sign(kp.PrivateKey, []byte("original message"))

	if Verify(kp.Address, []byte("tampered message"), sigHex) {
		t.Error("Verify() should return false for wrong message")
	}
}

// TestVerifyFailsWithWrongKey ensures verification fails with different key.
func TestVerifyFailsWithWrongKey(t *testing.T) {
	kp1, _ := GenerateKeyPair()
	kp2, _ := GenerateKeyPair()

	msg := []byte("test message")
	sigHex := Sign(kp1.PrivateKey, msg)

	if Verify(kp2.Address, msg, sigHex) {
		t.Error("Verify() should return false for wrong public key")
	}
}

// TestVerifyInvalidInputs tests Verify with malformed inputs.
func TestVerifyInvalidInputs(t *testing.T) {
	tests := []struct {
		name   string
		pubKey string
		msg    []byte
		sig    string
	}{
		{"empty public key", "", []byte("msg"), "aabbccdd"},
		{"invalid hex public key", "zzzz", []byte("msg"), "aabbccdd"},
		{"short public key", "aabb", []byte("msg"), "aabbccdd"},
		{"empty signature", "aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd", []byte("msg"), ""},
		{"invalid hex signature", "aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd", []byte("msg"), "zzzz"},
		{"short signature", "aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd", []byte("msg"), "aabb"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if Verify(tt.pubKey, tt.msg, tt.sig) {
				t.Error("Verify() should return false for invalid input")
			}
		})
	}
}

// TestSignTransaction tests transaction signing with hex-encoded private key.
func TestSignTransaction(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair() error: %v", err)
	}

	privKeyHex := hex.EncodeToString(kp.PrivateKey)
	from := kp.Address
	to := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	amount := 100.0

	sig, err := SignTransaction(privKeyHex, from, to, amount)
	if err != nil {
		t.Fatalf("SignTransaction() error: %v", err)
	}

	if sig == "" {
		t.Fatal("SignTransaction() returned empty signature")
	}

	// Verify the signature using the same message format.
	msg := []byte(from + ":" + to + ":100.000000")
	if !Verify(from, msg, sig) {
		t.Error("SignTransaction() produced signature that doesn't verify")
	}
}

// TestSignTransactionInvalidKey tests SignTransaction with bad private keys.
func TestSignTransactionInvalidKey(t *testing.T) {
	tests := []struct {
		name    string
		privKey string
	}{
		{"empty key", ""},
		{"invalid hex", "not-hex"},
		{"too short", "aabbccdd"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := SignTransaction(tt.privKey, "from", "to", 10)
			if err == nil {
				t.Error("SignTransaction() expected error for invalid key")
			}
		})
	}
}

// TestAddressFromPrivateKey verifies address derivation from private key.
func TestAddressFromPrivateKey(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair() error: %v", err)
	}

	privKeyHex := hex.EncodeToString(kp.PrivateKey)
	addr, err := AddressFromPrivateKey(privKeyHex)
	if err != nil {
		t.Fatalf("AddressFromPrivateKey() error: %v", err)
	}

	if addr != kp.Address {
		t.Errorf("AddressFromPrivateKey() = %s, want %s", addr, kp.Address)
	}
}

// TestAddressFromPrivateKeyInvalid tests error cases.
func TestAddressFromPrivateKeyInvalid(t *testing.T) {
	tests := []struct {
		name    string
		privKey string
	}{
		{"empty", ""},
		{"invalid hex", "not-valid-hex"},
		{"too short", "aabb"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := AddressFromPrivateKey(tt.privKey)
			if err == nil {
				t.Error("AddressFromPrivateKey() expected error")
			}
		})
	}
}
