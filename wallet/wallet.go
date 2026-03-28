// Package wallet provides offline wallet operations for the Noda cryptocurrency.
//
// CRITICAL-5: All private key operations are performed locally (offline).
// The node's HTTP API does NOT accept private keys in production mode.
// Instead, users build and sign transactions locally using this package,
// then broadcast the raw signed transaction via POST /tx/broadcast.
//
// Workflow:
//  1. Generate a new key pair: wallet.GenerateKeyPair()
//  2. Build a raw unsigned transaction: wallet.BuildUnsignedTx(...)
//  3. Sign the transaction: wallet.SignTransaction(...)
//  4. Broadcast the signed transaction to the node: POST /tx/broadcast
//
// CLI usage:
//
//	noda wallet new                  — generate a new key pair
//	noda wallet build-tx             — build an unsigned transaction from UTXOs
//	noda wallet sign-tx              — sign a transaction with a private key
//	noda wallet broadcast            — send a raw signed transaction to a node
package wallet

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Bihan293/Noda/block"
	"github.com/Bihan293/Noda/crypto"
)

// KeyPairResult is the result of generating a new key pair.
type KeyPairResult struct {
	Address    string `json:"address"`
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key"`
}

// GenerateNewKeyPair creates a new Ed25519 key pair for offline use.
func GenerateNewKeyPair() (*KeyPairResult, error) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		return nil, fmt.Errorf("key generation failed: %w", err)
	}
	return &KeyPairResult{
		Address:    kp.Address,
		PublicKey:  kp.Address,
		PrivateKey: fmt.Sprintf("%x", kp.PrivateKey),
	}, nil
}

// UTXOInfo represents a UTXO fetched from the node for building transactions.
type UTXOInfo struct {
	TxID    string  `json:"tx_id"`
	Index   int     `json:"index"`
	Amount  float64 `json:"amount"`
	Address string  `json:"address"`
}

// UnsignedTx is a transaction structure ready to be signed.
type UnsignedTx struct {
	Version  uint32           `json:"version"`
	Inputs   []block.TxInput  `json:"inputs"`
	Outputs  []block.TxOutput `json:"outputs"`
	LockTime uint64           `json:"lock_time"`
	Sighash  []byte           `json:"sighash"`
}

// BuildUnsignedTx creates an unsigned transaction from the given UTXOs.
// The caller must provide the UTXOs to spend and the desired outputs.
// A change output is automatically added if needed.
//
// Parameters:
//   - fromAddress: the sender's address (hex-encoded public key)
//   - utxos: the UTXOs to spend (must belong to fromAddress)
//   - toAddress: the recipient's address
//   - amount: the amount to send
//   - fee: the transaction fee (must be >= 0)
func BuildUnsignedTx(fromAddress string, utxos []UTXOInfo, toAddress string, amount, fee float64) (*UnsignedTx, error) {
	if amount <= 0 {
		return nil, fmt.Errorf("amount must be positive")
	}
	if fee < 0 {
		return nil, fmt.Errorf("fee must be non-negative")
	}
	if fromAddress == "" || toAddress == "" {
		return nil, fmt.Errorf("both from and to addresses are required")
	}
	if fromAddress == toAddress {
		return nil, fmt.Errorf("cannot send to yourself")
	}
	if len(utxos) == 0 {
		return nil, fmt.Errorf("at least one UTXO is required")
	}

	// Calculate total input.
	var totalInput float64
	for _, u := range utxos {
		totalInput += u.Amount
	}

	needed := amount + fee
	if totalInput < needed {
		return nil, fmt.Errorf("insufficient balance: have %.6f, need %.6f (amount %.6f + fee %.6f)",
			totalInput, needed, amount, fee)
	}

	// Build inputs.
	inputs := make([]block.TxInput, len(utxos))
	for i, u := range utxos {
		inputs[i] = block.TxInput{
			PrevTxID:  u.TxID,
			PrevIndex: u.Index,
			PubKey:    fromAddress,
		}
	}

	// Build outputs.
	outputs := []block.TxOutput{
		{Amount: amount, Address: toAddress},
	}

	// Add change output if needed.
	change := totalInput - amount - fee
	if change > 0.00000001 {
		outputs = append(outputs, block.TxOutput{Amount: change, Address: fromAddress})
	}

	// Build the unsigned transaction.
	tx := &block.Transaction{
		Version:  block.TxVersion,
		Inputs:   inputs,
		Outputs:  outputs,
		LockTime: 0,
	}

	// Compute sighash.
	sighash := block.ComputeSighash(tx)

	return &UnsignedTx{
		Version:  tx.Version,
		Inputs:   tx.Inputs,
		Outputs:  tx.Outputs,
		LockTime: tx.LockTime,
		Sighash:  sighash,
	}, nil
}

// SignedTx is a fully signed transaction ready for broadcast.
type SignedTx struct {
	Version  uint32           `json:"version"`
	Inputs   []block.TxInput  `json:"inputs"`
	Outputs  []block.TxOutput `json:"outputs"`
	LockTime uint64           `json:"lock_time"`
	ID       string           `json:"id"`
}

// SignTransaction signs an unsigned transaction with the given private key.
// Returns a fully signed transaction ready for broadcast.
func SignTransaction(unsignedTx *UnsignedTx, privateKeyHex string) (*SignedTx, error) {
	if unsignedTx == nil {
		return nil, fmt.Errorf("unsigned transaction is required")
	}
	if privateKeyHex == "" {
		return nil, fmt.Errorf("private key is required")
	}

	// Verify that the private key derives the expected address.
	addr, err := crypto.AddressFromPrivateKey(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}

	// Check that at least one input uses this address.
	found := false
	for _, in_ := range unsignedTx.Inputs {
		if in_.PubKey == addr {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("private key does not match any input address (derived: %s)", addr[:16]+"...")
	}

	// Sign the sighash.
	sig, err := crypto.SignSighash(privateKeyHex, unsignedTx.Sighash)
	if err != nil {
		return nil, fmt.Errorf("signing failed: %w", err)
	}

	// Set the signature on all inputs that match the address.
	signedInputs := make([]block.TxInput, len(unsignedTx.Inputs))
	copy(signedInputs, unsignedTx.Inputs)
	for i := range signedInputs {
		if signedInputs[i].PubKey == addr {
			signedInputs[i].Signature = sig
		}
	}

	// Compute the transaction ID.
	tx := &block.Transaction{
		Version:  unsignedTx.Version,
		Inputs:   signedInputs,
		Outputs:  unsignedTx.Outputs,
		LockTime: unsignedTx.LockTime,
	}
	txID := block.HashTransaction(tx)

	return &SignedTx{
		Version:  unsignedTx.Version,
		Inputs:   signedInputs,
		Outputs:  unsignedTx.Outputs,
		LockTime: unsignedTx.LockTime,
		ID:       txID,
	}, nil
}

// BroadcastResult is the response from broadcasting a transaction.
type BroadcastResult struct {
	Message       string `json:"message"`
	TxID          string `json:"txid"`
	Status        string `json:"status"`
	Confirmations int    `json:"confirmations"`
	Error         string `json:"error,omitempty"`
}

// BroadcastTransaction sends a signed transaction to a node's /tx/broadcast endpoint.
func BroadcastTransaction(nodeURL string, signedTx *SignedTx) (*BroadcastResult, error) {
	if nodeURL == "" {
		return nil, fmt.Errorf("node URL is required")
	}
	if signedTx == nil {
		return nil, fmt.Errorf("signed transaction is required")
	}

	// Build the request body.
	body, err := json.Marshal(signedTx)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize transaction: %w", err)
	}

	url := nodeURL + "/tx/broadcast"
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("broadcast failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var result BroadcastResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %s", string(respBody))
	}

	if resp.StatusCode >= 400 {
		return &result, fmt.Errorf("broadcast rejected (HTTP %d): %s", resp.StatusCode, result.Error)
	}

	return &result, nil
}
