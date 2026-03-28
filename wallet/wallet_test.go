package wallet

import (
	"encoding/hex"
	"testing"

	"github.com/Bihan293/Noda/block"
	"github.com/Bihan293/Noda/crypto"
)

func TestGenerateNewKeyPair(t *testing.T) {
	kp, err := GenerateNewKeyPair()
	if err != nil {
		t.Fatalf("GenerateNewKeyPair() error: %v", err)
	}
	if kp.Address == "" {
		t.Error("Address should not be empty")
	}
	if kp.PublicKey == "" {
		t.Error("PublicKey should not be empty")
	}
	if kp.PrivateKey == "" {
		t.Error("PrivateKey should not be empty")
	}
	// Address and PublicKey should be the same for Ed25519.
	if kp.Address != kp.PublicKey {
		t.Errorf("Address (%s) != PublicKey (%s)", kp.Address, kp.PublicKey)
	}
}

func TestBuildUnsignedTx(t *testing.T) {
	from := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	to := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	utxos := []UTXOInfo{
		{TxID: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Index: 0, Amount: 100, Address: from},
	}

	tx, err := BuildUnsignedTx(from, utxos, to, 50, 1)
	if err != nil {
		t.Fatalf("BuildUnsignedTx() error: %v", err)
	}
	if tx == nil {
		t.Fatal("BuildUnsignedTx() returned nil")
	}
	if len(tx.Inputs) != 1 {
		t.Errorf("expected 1 input, got %d", len(tx.Inputs))
	}
	if len(tx.Outputs) != 2 {
		t.Errorf("expected 2 outputs (recipient + change), got %d", len(tx.Outputs))
	}
	if tx.Outputs[0].Amount != 50 {
		t.Errorf("output 0 amount = %f, want 50", tx.Outputs[0].Amount)
	}
	if tx.Outputs[0].Address != to {
		t.Errorf("output 0 address = %s, want %s", tx.Outputs[0].Address, to)
	}
	if tx.Outputs[1].Amount != 49 { // 100 - 50 - 1 fee
		t.Errorf("change amount = %f, want 49", tx.Outputs[1].Amount)
	}
	if tx.Outputs[1].Address != from {
		t.Errorf("change address = %s, want %s", tx.Outputs[1].Address, from)
	}
	if len(tx.Sighash) == 0 {
		t.Error("Sighash should not be empty")
	}
}

func TestBuildUnsignedTx_InsufficientBalance(t *testing.T) {
	from := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	to := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	utxos := []UTXOInfo{
		{TxID: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Index: 0, Amount: 10, Address: from},
	}

	_, err := BuildUnsignedTx(from, utxos, to, 100, 1)
	if err == nil {
		t.Error("BuildUnsignedTx() should fail for insufficient balance")
	}
}

func TestBuildUnsignedTx_NegativeAmount(t *testing.T) {
	from := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	to := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	utxos := []UTXOInfo{
		{TxID: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Index: 0, Amount: 100, Address: from},
	}

	_, err := BuildUnsignedTx(from, utxos, to, -10, 0)
	if err == nil {
		t.Error("BuildUnsignedTx() should fail for negative amount")
	}
}

func TestBuildUnsignedTx_NegativeFee(t *testing.T) {
	from := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	to := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	utxos := []UTXOInfo{
		{TxID: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Index: 0, Amount: 100, Address: from},
	}

	_, err := BuildUnsignedTx(from, utxos, to, 50, -1)
	if err == nil {
		t.Error("BuildUnsignedTx() should fail for negative fee")
	}
}

func TestBuildUnsignedTx_SelfSend(t *testing.T) {
	addr := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	utxos := []UTXOInfo{
		{TxID: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Index: 0, Amount: 100, Address: addr},
	}

	_, err := BuildUnsignedTx(addr, utxos, addr, 50, 0)
	if err == nil {
		t.Error("BuildUnsignedTx() should fail for self-send")
	}
}

func TestBuildUnsignedTx_NoUTXOs(t *testing.T) {
	from := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	to := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	_, err := BuildUnsignedTx(from, nil, to, 50, 0)
	if err == nil {
		t.Error("BuildUnsignedTx() should fail for no UTXOs")
	}
}

func TestSignTransaction(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privHex := hex.EncodeToString(kp.PrivateKey)

	to := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	utxos := []UTXOInfo{
		{TxID: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Index: 0, Amount: 100, Address: kp.Address},
	}

	unsignedTx, err := BuildUnsignedTx(kp.Address, utxos, to, 50, 0)
	if err != nil {
		t.Fatalf("BuildUnsignedTx() error: %v", err)
	}

	signedTx, err := SignTransaction(unsignedTx, privHex)
	if err != nil {
		t.Fatalf("SignTransaction() error: %v", err)
	}

	if signedTx == nil {
		t.Fatal("SignTransaction() returned nil")
	}
	if signedTx.ID == "" {
		t.Error("SignTransaction() should set tx ID")
	}

	// Verify signature.
	for _, in_ := range signedTx.Inputs {
		if in_.Signature == "" {
			t.Error("input signature should not be empty")
		}
	}

	// Verify the signed tx can be validated with sighash.
	tx := &block.Transaction{
		Version:  signedTx.Version,
		Inputs:   signedTx.Inputs,
		Outputs:  signedTx.Outputs,
		LockTime: signedTx.LockTime,
	}
	sighash := block.ComputeSighash(tx)
	for _, in_ := range signedTx.Inputs {
		if !crypto.VerifySighash(in_.PubKey, sighash, in_.Signature) {
			t.Error("signature verification failed")
		}
	}
}

func TestSignTransaction_WrongKey(t *testing.T) {
	kp1, _ := crypto.GenerateKeyPair()
	kp2, _ := crypto.GenerateKeyPair()
	priv2Hex := hex.EncodeToString(kp2.PrivateKey)

	to := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	utxos := []UTXOInfo{
		{TxID: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Index: 0, Amount: 100, Address: kp1.Address},
	}

	unsignedTx, err := BuildUnsignedTx(kp1.Address, utxos, to, 50, 0)
	if err != nil {
		t.Fatalf("BuildUnsignedTx() error: %v", err)
	}

	// Try signing with the wrong key.
	_, err = SignTransaction(unsignedTx, priv2Hex)
	if err == nil {
		t.Error("SignTransaction() should fail when key does not match input address")
	}
}

func TestSignTransaction_NilTx(t *testing.T) {
	_, err := SignTransaction(nil, "aabb")
	if err == nil {
		t.Error("SignTransaction() should fail for nil tx")
	}
}

func TestSignTransaction_EmptyKey(t *testing.T) {
	utx := &UnsignedTx{
		Version: 1,
		Inputs:  []block.TxInput{{PrevTxID: "tx1", PrevIndex: 0, PubKey: "addr"}},
		Outputs: []block.TxOutput{{Amount: 10, Address: "to"}},
		Sighash: []byte{1, 2, 3},
	}
	_, err := SignTransaction(utx, "")
	if err == nil {
		t.Error("SignTransaction() should fail for empty key")
	}
}

func TestBuildUnsignedTx_ExactAmount_NoChange(t *testing.T) {
	from := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	to := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	utxos := []UTXOInfo{
		{TxID: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Index: 0, Amount: 100, Address: from},
	}

	// Send exactly 100 with 0 fee = no change.
	tx, err := BuildUnsignedTx(from, utxos, to, 100, 0)
	if err != nil {
		t.Fatalf("BuildUnsignedTx() error: %v", err)
	}
	if len(tx.Outputs) != 1 {
		t.Errorf("expected 1 output (no change), got %d", len(tx.Outputs))
	}
}
