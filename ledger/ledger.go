// Package ledger manages the blockchain, UTXO set, mempool, and validates transactions.
//
// It combines:
//   - Blockchain (ordered sequence of blocks)
//   - UTXO set (unspent transaction outputs for balance tracking)
//   - Mempool (pool of unconfirmed transactions)
//   - Faucet: 100 coins per request, global cap 1,000,000 total (no per-address cooldown)
//   - Mining rewards with halving
//   - Wallet-level transaction builder (CRITICAL-2)
//
// CRITICAL-2: Transactions now use explicit UTXO inputs and outputs.
// The ledger provides a wallet-level builder (BuildTransaction) that
// automatically selects UTXOs for convenience, but the consensus layer
// only sees explicit inputs/outputs.
//
// CRITICAL-3: Transaction submission is decoupled from mining.
// SubmitTransaction only validates and adds to mempool.
// Mining is performed by the separate miner package.
//
// HIGH-1: Persistence uses crash-safe blockstore + chainstate instead of
// a monolithic JSON file. Blocks are stored individually; metadata and
// UTXO chainstate are written atomically. Legacy node_data.json files
// are migrated on first startup.
//
// Genesis ownership:
//
//	The genesis supply (1M) is assigned to an address derived from the configured
//	faucet/genesis private key. The genesis owner is stored in chain metadata and
//	verified on every restart. A mismatched key causes a fail-fast error.
package ledger

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/Bihan293/Noda/block"
	"github.com/Bihan293/Noda/chain"
	"github.com/Bihan293/Noda/crypto"
	"github.com/Bihan293/Noda/mempool"
	"github.com/Bihan293/Noda/storage"
	"github.com/Bihan293/Noda/utxo"
)

// ──────────────────────────────────────────────────────────────────────────────
// Constants
// ──────────────────────────────────────────────────────────────────────────────

const (
	// StorageFile is the default legacy file path (kept for backwards compat).
	StorageFile = "node_data.json"

	// StorageDir is the default directory for the new blockstore+chainstate storage.
	StorageDir = "noda_data"

	// FaucetAmount is how many coins the faucet distributes per request.
	FaucetAmount = 100.0

	// FaucetGlobalCap is the maximum total coins that can be distributed via faucet.
	// Once 1,000,000 coins have been distributed, the faucet is permanently disabled.
	FaucetGlobalCap = 1_000_000.0
)

// ──────────────────────────────────────────────────────────────────────────────
// Errors
// ──────────────────────────────────────────────────────────────────────────────

// ErrGenesisOwnerMismatch is returned when the provided faucet key does not
// match the genesis owner recorded in the chain.
var ErrGenesisOwnerMismatch = fmt.Errorf("genesis owner mismatch")

// ErrLegacyChainData is returned when the chain contains legacy account-model
// transactions that are incompatible with the UTXO input/output model.
var ErrLegacyChainData = fmt.Errorf("legacy chain data detected")

// ──────────────────────────────────────────────────────────────────────────────
// Ledger
// ──────────────────────────────────────────────────────────────────────────────

// Ledger holds the full blockchain, UTXO set, mempool, and faucet state.
type Ledger struct {
	Chain    *chain.Blockchain  `json:"chain"`
	Balances map[string]float64 `json:"balances"` // kept for JSON compat; rebuilt from UTXO
	mu       sync.RWMutex
	filePath string // legacy JSON path (kept for backward compat)

	// HIGH-1: crash-safe storage engine
	store *storage.Store

	// UTXO set — the source of truth for balances.
	UTXOSet *utxo.Set `json:"-"` // rebuilt from chain, not persisted as JSON field

	// Mempool — unconfirmed transaction pool.
	Mempool *mempool.Mempool `json:"-"` // transient, not persisted

	// Faucet state
	faucetPrivKey string // hex-encoded Ed25519 private key for faucet wallet
	faucetAddress string // derived from faucetPrivKey
}

// NewLedger creates a new ledger with a genesis blockchain using the legacy
// hardcoded genesis address. For new networks, use NewLedgerWithOwner.
func NewLedger(filePath string) *Ledger {
	return NewLedgerWithOwner(filePath, block.LegacyGenesisAddress)
}

// NewLedgerWithOwner creates a new ledger with a genesis blockchain where the
// genesis supply (1M) belongs to the specified owner address.
// The storePath is used as the storage directory for the new blockstore.
func NewLedgerWithOwner(storePath string, genesisOwner string) *Ledger {
	bc := chain.NewBlockchainWithOwner(genesisOwner)

	// Build UTXO set from genesis block.
	utxoSet, err := utxo.RebuildFromBlocks(bc.Blocks)
	if err != nil {
		slog.Warn("UTXO rebuild failed, starting with empty set", "error", err)
		utxoSet = utxo.NewSet()
	}

	// Derive balances from UTXO.
	balances := utxoSet.AllBalances()

	// Initialize storage engine.
	var st *storage.Store
	storeDir := storeDirFromPath(storePath)
	st, err = storage.NewStore(storeDir)
	if err != nil {
		slog.Warn("Failed to initialize storage, persistence disabled", "error", err)
	}

	l := &Ledger{
		Chain:    bc,
		Balances: balances,
		filePath: storePath,
		store:    st,
		UTXOSet:  utxoSet,
		Mempool:  mempool.New(mempool.DefaultMaxSize),
	}

	// Save genesis block and metadata to the new store.
	if st != nil && len(bc.Blocks) > 0 {
		for _, b := range bc.Blocks {
			_ = st.SaveBlock(b)
		}
		_ = l.saveMetadataToStore()
		_ = l.saveChainstateToStore()
	}

	slog.Info("New ledger created",
		"genesis_supply", block.GenesisSupply,
		"genesis_owner", genesisOwner,
		"utxo_count", utxoSet.Size(),
		"storage", storeDir,
	)
	return l
}

// ──────────────────────────────────────────────────────────────────────────────
// ChainAccess interface — used by the miner package (CRITICAL-3)
// ──────────────────────────────────────────────────────────────────────────────

// UTXOSetRef returns a pointer to the UTXO set.
func (l *Ledger) UTXOSetRef() *utxo.Set {
	return l.UTXOSet
}

// MempoolRef returns a pointer to the mempool.
func (l *Ledger) MempoolRef() *mempool.Mempool {
	return l.Mempool
}

// ApplyMinedBlock applies a mined block to chain + UTXO, removes mempool txs, persists.
// This is called by the miner after successfully mining a block.
func (l *Ledger) ApplyMinedBlock(b *block.Block, txIDs []string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Save block to blockstore first (before chain state update).
	if err := l.saveBlockToStore(b); err != nil {
		slog.Warn("Failed to save mined block to store", "error", err)
	}

	// Add block to chain.
	if err := l.Chain.AddBlock(b); err != nil {
		return fmt.Errorf("failed to add mined block: %w", err)
	}

	// Apply block to UTXO set.
	if err := l.UTXOSet.ApplyBlock(b); err != nil {
		return fmt.Errorf("UTXO update failed for mined block: %w", err)
	}

	// Remove confirmed transactions from mempool.
	l.Mempool.RemoveBatch(txIDs)

	// Update balance cache from UTXO.
	l.Balances = l.UTXOSet.AllBalances()

	// Persist metadata + chainstate.
	_ = l.saveLocked()

	slog.Info("Mined block applied",
		"height", b.Header.Height,
		"txs", len(b.Transactions),
		"utxo_size", l.UTXOSet.Size(),
	)
	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Genesis Owner Management
// ──────────────────────────────────────────────────────────────────────────────

// GenesisOwner returns the address that owns the genesis supply, as recorded
// in the chain metadata or extracted from the genesis block.
func (l *Ledger) GenesisOwner() string {
	// Prefer the explicit metadata field.
	if l.Chain.GenesisOwner != "" {
		return l.Chain.GenesisOwner
	}
	// Fall back to inspecting the genesis block itself.
	if len(l.Chain.Blocks) > 0 {
		if owner, ok := block.GenesisOwnerFromBlock(l.Chain.Blocks[0]); ok {
			return owner
		}
	}
	return ""
}

// FaucetOwnerMatch returns true if the configured faucet address matches the
// genesis owner (i.e. the faucet actually controls the genesis funds).
func (l *Ledger) FaucetOwnerMatch() bool {
	if l.faucetAddress == "" {
		return false
	}
	return l.faucetAddress == l.GenesisOwner()
}

// UsableFaucetBalance returns the actual spendable balance on the genesis/faucet
// address from the UTXO set. If faucet address != genesis owner, returns 0.
func (l *Ledger) UsableFaucetBalance() float64 {
	if !l.FaucetOwnerMatch() {
		return 0
	}
	return l.UTXOSet.Balance(l.faucetAddress)
}

// SetFaucetKeyAndValidateGenesis configures the faucet wallet and ensures the
// key matches the genesis owner recorded in the chain.
//
// On a NEW chain (height 0, no faucet distributed):
//   - If the chain still uses the legacy hardcoded genesis address, perform a
//     one-time migration to rebind genesis to the provided key.
//
// On an EXISTING chain:
//   - If the key does not match the recorded genesis owner, return a fail-fast
//     error with a clear message.
func (l *Ledger) SetFaucetKeyAndValidateGenesis(privKeyHex string) error {
	addr, err := crypto.AddressFromPrivateKey(privKeyHex)
	if err != nil {
		return fmt.Errorf("invalid faucet private key: %w", err)
	}

	currentGenesisOwner := l.GenesisOwner()

	// ── Case 1: Key matches the existing genesis owner — happy path ──
	if addr == currentGenesisOwner {
		l.faucetPrivKey = privKeyHex
		l.faucetAddress = addr
		slog.Info("Faucet wallet configured (matches genesis owner)", "address", addr)
		return nil
	}

	// ── Case 2: Fresh chain or legacy chain at height 0 — safe migration ──
	if l.canMigrateLegacyGenesis() {
		slog.Info("Migrating genesis owner from legacy address to configured key",
			"old_owner", currentGenesisOwner,
			"new_owner", addr,
		)
		if err := l.migrateLegacyGenesis(addr); err != nil {
			return fmt.Errorf("legacy genesis migration failed: %w", err)
		}
		l.faucetPrivKey = privKeyHex
		l.faucetAddress = addr
		slog.Info("Faucet wallet configured after genesis migration", "address", addr)
		return nil
	}

	// ── Case 3: Existing chain with incompatible key — fail fast ──
	return fmt.Errorf("%w: the chain records genesis owner as %s, "+
		"but the provided faucet key derives address %s. "+
		"Either use the correct FAUCET_KEY for this chain, or start a new chain",
		ErrGenesisOwnerMismatch, shortAddr(currentGenesisOwner), shortAddr(addr))
}

// canMigrateLegacyGenesis returns true if the chain is eligible for a one-time
// migration of the genesis owner. Conditions:
//   - Chain height is 0 (only genesis block)
//   - No faucet coins have been distributed yet
//   - Current genesis owner is the legacy hardcoded address
func (l *Ledger) canMigrateLegacyGenesis() bool {
	if l.Chain.Height() > 0 {
		return false
	}
	if l.Chain.TotalFaucet > 0 {
		return false
	}
	currentOwner := l.GenesisOwner()
	return currentOwner == block.LegacyGenesisAddress
}

// migrateLegacyGenesis replaces the legacy genesis block with one that assigns
// the supply to the new owner address, and rebuilds the UTXO set.
func (l *Ledger) migrateLegacyGenesis(newOwnerAddress string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Replace the chain with a fresh one rooted at the new owner.
	bc := chain.NewBlockchainWithOwner(newOwnerAddress)

	// Rebuild UTXO set.
	utxoSet, err := utxo.RebuildFromBlocks(bc.Blocks)
	if err != nil {
		return fmt.Errorf("UTXO rebuild after genesis migration failed: %w", err)
	}

	l.Chain = bc
	l.UTXOSet = utxoSet
	l.Balances = utxoSet.AllBalances()

	// Save genesis block to blockstore (HIGH-1).
	if l.store != nil && len(bc.Blocks) > 0 {
		_ = l.store.SaveBlock(bc.Blocks[0])
	}

	// Persist immediately.
	if err := l.saveLocked(); err != nil {
		slog.Warn("Failed to persist after genesis migration", "error", err)
	}

	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Faucet Configuration (legacy entry point)
// ──────────────────────────────────────────────────────────────────────────────

// SetFaucetKey configures the faucet wallet from a hex-encoded private key.
// This is a simplified version that does NOT validate genesis ownership.
// Prefer SetFaucetKeyAndValidateGenesis for production startup.
func (l *Ledger) SetFaucetKey(privKeyHex string) error {
	addr, err := crypto.AddressFromPrivateKey(privKeyHex)
	if err != nil {
		return fmt.Errorf("invalid faucet private key: %w", err)
	}
	l.faucetPrivKey = privKeyHex
	l.faucetAddress = addr
	slog.Info("Faucet wallet configured", "address", addr)
	return nil
}

// FaucetAddress returns the faucet wallet address (empty if not configured).
func (l *Ledger) FaucetAddress() string {
	return l.faucetAddress
}

// FaucetTotalDistributed returns total coins distributed via faucet.
func (l *Ledger) FaucetTotalDistributed() float64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.Chain.TotalFaucet
}

// IsFaucetActive returns true if the faucet can still distribute coins.
// Now also requires that the faucet address matches the genesis owner.
func (l *Ledger) IsFaucetActive() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.faucetPrivKey != "" &&
		l.faucetAddress == l.Chain.GenesisOwner &&
		l.Chain.TotalFaucet < FaucetGlobalCap
}

// FaucetRemaining returns how many coins the faucet can still distribute.
func (l *Ledger) FaucetRemaining() float64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	remaining := FaucetGlobalCap - l.Chain.TotalFaucet
	if remaining < 0 {
		return 0
	}
	return remaining
}

// ──────────────────────────────────────────────────────────────────────────────
// Balance Queries (from UTXO set)
// ──────────────────────────────────────────────────────────────────────────────

// GetBalance returns the balance for a given address from the UTXO set.
func (l *Ledger) GetBalance(address string) float64 {
	return l.UTXOSet.Balance(address)
}

// GetAllBalances returns a copy of all balances derived from the UTXO set.
func (l *Ledger) GetAllBalances() map[string]float64 {
	return l.UTXOSet.AllBalances()
}

// ──────────────────────────────────────────────────────────────────────────────
// Wallet-Level Transaction Builder (CRITICAL-2)
// ──────────────────────────────────────────────────────────────────────────────

// BuildTransaction creates a fully formed UTXO transaction by:
//  1. Selecting enough UTXOs from the sender's address to cover the amount
//  2. Creating outputs: one for the recipient and one for change (if needed)
//  3. Computing the sighash and signing all inputs
//  4. Computing the transaction ID
//
// This is a wallet-level convenience function. The resulting transaction
// has explicit inputs/outputs suitable for consensus validation.
func (l *Ledger) BuildTransaction(privateKeyHex, fromAddr, toAddr string, amount float64) (*block.Transaction, error) {
	if amount <= 0 {
		return nil, fmt.Errorf("amount must be positive")
	}
	if fromAddr == "" || toAddr == "" {
		return nil, fmt.Errorf("both 'from' and 'to' addresses are required")
	}
	if fromAddr == toAddr {
		return nil, fmt.Errorf("cannot send to yourself")
	}

	// Select UTXOs to cover the amount.
	selectedOPs, totalInput, err := l.UTXOSet.FindUTXOsForAmount(fromAddr, amount)
	if err != nil {
		return nil, fmt.Errorf("UTXO selection failed: %w", err)
	}

	// Check mempool for double-spend on selected outpoints.
	for _, op := range selectedOPs {
		if l.Mempool.IsOutpointSpent(op.TxID, op.Index) {
			return nil, fmt.Errorf("outpoint %s:%d is already being spent by a pending transaction",
				shortAddr(op.TxID), op.Index)
		}
	}

	// Build inputs.
	inputs := make([]block.TxInput, len(selectedOPs))
	for i, op := range selectedOPs {
		inputs[i] = block.TxInput{
			PrevTxID:  op.TxID,
			PrevIndex: op.Index,
			PubKey:    fromAddr, // pub key = address in Ed25519
		}
	}

	// Build outputs.
	outputs := []block.TxOutput{
		{Amount: amount, Address: toAddr},
	}

	// Add change output if needed.
	change := totalInput - amount
	if change > 0.00000001 { // avoid dust
		outputs = append(outputs, block.TxOutput{Amount: change, Address: fromAddr})
	}

	// Construct the unsigned transaction.
	tx := &block.Transaction{
		Version:  block.TxVersion,
		Inputs:   inputs,
		Outputs:  outputs,
		LockTime: 0,
	}

	// Compute sighash and sign all inputs.
	sighash := block.ComputeSighash(tx)
	sig, err := crypto.SignSighash(privateKeyHex, sighash)
	if err != nil {
		return nil, fmt.Errorf("signing failed: %w", err)
	}

	// Set the signature on all inputs.
	for i := range tx.Inputs {
		tx.Inputs[i].Signature = sig
	}

	// Compute the transaction ID.
	tx.ID = block.HashTransaction(tx)

	return tx, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Transaction Fee Computation (CRITICAL-3)
// ──────────────────────────────────────────────────────────────────────────────

// ComputeTxFee computes the transaction fee from the UTXO set.
// fee = sum(input values from UTXO) - sum(output values)
// Returns the fee and an error if any input is not found.
func (l *Ledger) ComputeTxFee(tx block.Transaction) (float64, error) {
	var totalInput float64
	for i, in_ := range tx.Inputs {
		op := utxo.OutPoint{TxID: in_.PrevTxID, Index: in_.PrevIndex}
		utxoOut := l.UTXOSet.Get(op)
		if utxoOut == nil {
			return 0, fmt.Errorf("input %d: utxo %s:%d not found", i, shortAddr(in_.PrevTxID), in_.PrevIndex)
		}
		totalInput += utxoOut.Amount
	}
	totalOutput := tx.TotalOutputValue()
	fee := totalInput - totalOutput
	return fee, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Transaction Validation (CRITICAL-2: explicit inputs/outputs)
// ──────────────────────────────────────────────────────────────────────────────

// ValidateUserTx checks a user-submitted transaction without applying it.
// Validates: outputs, inputs (UTXO existence, amounts), signature (sighash),
// and ensures fee is non-negative (CRITICAL-3).
func (l *Ledger) ValidateUserTx(tx block.Transaction) error {
	// Must have at least one input and one output.
	if len(tx.Inputs) == 0 {
		return fmt.Errorf("transaction must have at least one input")
	}
	if len(tx.Outputs) == 0 {
		return fmt.Errorf("transaction must have at least one output")
	}

	// Validate output amounts.
	for i, out := range tx.Outputs {
		if out.Amount <= 0 {
			return fmt.Errorf("output %d: amount must be positive, got %f", i, out.Amount)
		}
		if out.Address == "" {
			return fmt.Errorf("output %d: address is required", i)
		}
	}

	// Validate inputs: each must reference an existing UTXO.
	var totalInput float64
	var senderAddr string
	for i, in_ := range tx.Inputs {
		op := utxo.OutPoint{TxID: in_.PrevTxID, Index: in_.PrevIndex}
		utxoOut := l.UTXOSet.Get(op)
		if utxoOut == nil {
			return fmt.Errorf("input %d: utxo %s:%d not found", i, shortAddr(in_.PrevTxID), in_.PrevIndex)
		}
		totalInput += utxoOut.Amount

		// All inputs must be from the same address (simplification for v1).
		if senderAddr == "" {
			senderAddr = utxoOut.Address
		} else if utxoOut.Address != senderAddr {
			return fmt.Errorf("input %d: all inputs must be from the same address", i)
		}

		// Check that the pubkey matches the UTXO owner.
		if in_.PubKey != utxoOut.Address {
			return fmt.Errorf("input %d: pubkey does not match UTXO owner", i)
		}

		// Check mempool double-spend.
		if l.Mempool.IsOutpointSpent(in_.PrevTxID, in_.PrevIndex) {
			return fmt.Errorf("input %d: outpoint %s:%d already spent by pending tx",
				i, shortAddr(in_.PrevTxID), in_.PrevIndex)
		}
	}

	// Total outputs must not exceed total inputs (fee >= 0).
	totalOutput := tx.TotalOutputValue()
	if totalOutput > totalInput {
		return fmt.Errorf("output total (%.6f) exceeds input total (%.6f) — negative fee not allowed", totalOutput, totalInput)
	}

	// Signature verification using sighash.
	sighash := block.ComputeSighash(&tx)
	for i, in_ := range tx.Inputs {
		if !crypto.VerifySighash(in_.PubKey, sighash, in_.Signature) {
			return fmt.Errorf("input %d: Ed25519 signature verification failed for %s",
				i, shortAddr(in_.PubKey))
		}
	}

	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Transaction Processing (CRITICAL-3: decoupled from mining)
// ──────────────────────────────────────────────────────────────────────────────

// SubmitTransaction validates a user transaction and adds it to the mempool.
// CRITICAL-3: does NOT mine a block — transactions stay in mempool until the
// miner worker picks them up.
func (l *Ledger) SubmitTransaction(tx block.Transaction) error {
	// Validate the transaction.
	if err := l.ValidateUserTx(tx); err != nil {
		return err
	}

	// Ensure ID is computed.
	if tx.ID == "" {
		tx.ID = block.HashTransaction(&tx)
	}

	// Compute the transaction fee.
	fee, err := l.ComputeTxFee(tx)
	if err != nil {
		return fmt.Errorf("fee computation failed: %w", err)
	}
	if fee < 0 {
		return fmt.Errorf("negative fee (%.6f) not allowed", fee)
	}

	// Add to mempool with fee information.
	if err := l.Mempool.AddWithFee(tx, fee); err != nil {
		return fmt.Errorf("mempool rejection: %w", err)
	}

	slog.Info("TX accepted to mempool",
		"txid", shortAddr(tx.ID),
		"inputs", len(tx.Inputs),
		"outputs", len(tx.Outputs),
		"fee", fee,
		"status", "pending",
	)

	return nil
}

// ValidateAndProcessUserTx is the legacy entry point — wraps SubmitTransaction.
func (l *Ledger) ValidateAndProcessUserTx(tx block.Transaction) error {
	return l.SubmitTransaction(tx)
}

// ──────────────────────────────────────────────────────────────────────────────
// Faucet — Global cap 1M, 100 coins/request, no per-address cooldown
// ──────────────────────────────────────────────────────────────────────────────

// ProcessFaucet sends FaucetAmount coins from the faucet wallet to the given address.
// Enforces only the global faucet cap (1M total). No per-address cooldown.
// Multiple claims allowed from any address until the global cap is reached.
// CRITICAL-3: Faucet transactions now go through the mempool like any other transaction.
func (l *Ledger) ProcessFaucet(toAddress string) (*block.Transaction, error) {
	// Check faucet is configured.
	if l.faucetPrivKey == "" || l.faucetAddress == "" {
		return nil, fmt.Errorf("faucet not configured: start node with -faucet-key flag")
	}

	// Verify faucet address controls genesis funds.
	if !l.FaucetOwnerMatch() {
		return nil, fmt.Errorf("faucet address %s does not match genesis owner %s — faucet cannot spend genesis funds",
			shortAddr(l.faucetAddress), shortAddr(l.GenesisOwner()))
	}

	if toAddress == "" {
		return nil, fmt.Errorf("invalid address: 'to' address is required")
	}
	if toAddress == l.faucetAddress {
		return nil, fmt.Errorf("invalid address: cannot send faucet coins to the faucet itself")
	}

	// Check global faucet cap.
	l.mu.RLock()
	totalDistributed := l.Chain.TotalFaucet
	l.mu.RUnlock()

	if totalDistributed >= FaucetGlobalCap {
		return nil, fmt.Errorf("faucet exhausted: all %.0f coins have been distributed — faucet is permanently disabled", FaucetGlobalCap)
	}

	// Calculate actual amount (may be less than FaucetAmount near the cap).
	amount := FaucetAmount
	remaining := FaucetGlobalCap - totalDistributed
	if amount > remaining {
		amount = remaining
	}

	// Check faucet wallet has enough balance.
	faucetBalance := l.UTXOSet.Balance(l.faucetAddress)
	if faucetBalance < amount {
		return nil, fmt.Errorf("faucet wallet insufficient balance: has %.2f, needs %.2f", faucetBalance, amount)
	}

	// Build and sign the transaction using the wallet builder.
	tx, err := l.BuildTransaction(l.faucetPrivKey, l.faucetAddress, toAddress, amount)
	if err != nil {
		return nil, fmt.Errorf("faucet transaction build failed: %w", err)
	}

	// Submit through normal validation (adds to mempool, no mining).
	if err := l.SubmitTransaction(*tx); err != nil {
		return nil, fmt.Errorf("faucet transaction failed: %w", err)
	}

	// Update faucet tracking.
	l.mu.Lock()
	l.Chain.TotalFaucet += amount
	l.mu.Unlock()

	slog.Info("Faucet distribution",
		"to", shortAddr(toAddress),
		"amount", amount,
		"total_distributed", totalDistributed+amount,
		"global_cap", FaucetGlobalCap,
		"status", "pending",
	)

	return tx, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Chain Sync — CRITICAL-4: selection by cumulative work, not length
// ──────────────────────────────────────────────────────────────────────────────

// ReplaceChain replaces the current chain if the new one has MORE CUMULATIVE
// WORK and is valid. CRITICAL-4: no longer compares by length.
// Rebuilds the UTXO set from the new chain.
func (l *Ledger) ReplaceChain(newChain *chain.Blockchain) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	// CRITICAL-4: Compare by cumulative work, not length.
	newWork := newChain.CumulativeWork()
	oldWork := l.Chain.CumulativeWork()
	if newWork.Cmp(oldWork) <= 0 {
		slog.Debug("Sync rejected: new chain does not have more cumulative work",
			"old_work", oldWork.String(),
			"new_work", newWork.String(),
		)
		return false
	}

	// Check for legacy data.
	if hasLegacy, height := chain.ContainsLegacyBlocks(newChain); hasLegacy {
		slog.Warn("Sync rejected: chain contains legacy account-model transactions",
			"block_height", height)
		return false
	}

	// Validate the full chain.
	if err := chain.ValidateChain(newChain); err != nil {
		slog.Warn("Sync rejected: invalid chain", "error", err)
		return false
	}

	// Rebuild UTXO set from the new chain.
	newUTXO, err := utxo.RebuildFromBlocks(newChain.Blocks)
	if err != nil {
		slog.Warn("Sync rejected: UTXO rebuild failed", "error", err)
		return false
	}

	// Rebuild balances from UTXO.
	balances := newUTXO.AllBalances()

	// Collect transactions from old chain blocks that are NOT in new chain
	// and return them to the mempool if still valid.
	oldBlocks := make(map[string]bool)
	for _, b := range l.Chain.Blocks {
		oldBlocks[b.Hash] = true
	}
	newBlocks := make(map[string]bool)
	for _, b := range newChain.Blocks {
		newBlocks[b.Hash] = true
	}
	var returnToMempool []block.Transaction
	for _, b := range l.Chain.Blocks {
		if !newBlocks[b.Hash] && b.Header.Height > 0 {
			for _, tx := range b.Transactions {
				if !tx.IsCoinbase() && !tx.IsGenesis() {
					returnToMempool = append(returnToMempool, tx)
				}
			}
		}
	}

	l.Chain = newChain
	l.UTXOSet = newUTXO
	l.Balances = balances

	// Rebuild index.
	l.Chain.RebuildIndex()

	// Save all blocks to blockstore (HIGH-1).
	if l.store != nil {
		for _, b := range newChain.Blocks {
			if !l.store.HasBlock(b.Hash) {
				_ = l.store.SaveBlock(b)
			}
		}
	}

	_ = l.saveLocked()

	slog.Info("Chain replaced (cumulative work)",
		"blocks", newChain.Len(),
		"utxo_count", newUTXO.Size(),
		"cumulative_work", newWork.String(),
		"returned_to_mempool", len(returnToMempool),
	)

	// Try to return disconnected transactions to mempool.
	for _, tx := range returnToMempool {
		if tx.ID == "" {
			tx.ID = block.HashTransaction(&tx)
		}
		// Best effort — ignore errors (tx may be invalid against new UTXO state).
		_ = l.Mempool.Add(tx)
	}

	return true
}

// ──────────────────────────────────────────────────────────────────────────────
// Block Processing — CRITICAL-4: Process individual blocks with reorg support
// ──────────────────────────────────────────────────────────────────────────────

// ProcessBlock validates a block and integrates it into the chain. If the block
// causes a chain reorg (a side branch accumulating more work than the main chain),
// the UTXO set is rolled back to the fork point and reapplied on the new best chain.
//
// Returns true if the block was accepted (main chain or orphan), false if rejected.
func (l *Ledger) ProcessBlock(b *block.Block) (accepted bool, isOrphan bool, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	idx := l.Chain.Index
	if idx == nil {
		return false, false, fmt.Errorf("block index not initialized")
	}

	// Check for duplicate.
	if idx.HasBlock(b.Hash) || idx.HasOrphan(b.Hash) {
		return false, false, nil // duplicate
	}

	// Basic block validation (header, merkle, PoW) — but not chain linkage
	// because the block may be on a side branch.
	if b.Header.Height > 0 {
		computed := block.HashBlockHeader(b.Header)
		if b.Hash != computed {
			return false, false, fmt.Errorf("hash mismatch at height %d", b.Header.Height)
		}
		target := block.TargetFromBits(b.Header.Bits)
		if !block.MeetsTarget(b.Hash, target) {
			return false, false, fmt.Errorf("PoW not satisfied at height %d", b.Header.Height)
		}
		if err := block.ValidateBlockMerkle(b); err != nil {
			return false, false, err
		}
	}

	// Add to block index — this handles orphan detection and best-tip comparison.
	result := idx.AddBlock(b)
	if !result.Added {
		return false, false, nil // duplicate
	}

	// Save block to blockstore (HIGH-1).
	if err := l.saveBlockToStore(b); err != nil {
		slog.Warn("Failed to save block to store", "height", b.Header.Height, "error", err)
	}

	if result.IsOrphan {
		slog.Debug("Block added to orphan pool",
			"hash", b.Hash[:16],
			"height", b.Header.Height,
		)
		return true, true, nil
	}

	// Block has a known parent. Determine if it extends the main chain or causes reorg.
	if result.ReorgOccurred {
		// A side chain now has more cumulative work — perform reorg.
		slog.Info("Performing chain reorganization",
			"old_tip", result.OldTip[:16],
			"new_tip", result.NewTip[:16],
		)
		if err := l.performReorgLocked(result.OldTip, result.NewTip); err != nil {
			slog.Error("Reorg failed — keeping old chain", "error", err)
			// TODO: roll back index state on failure
			return false, false, fmt.Errorf("reorg failed: %w", err)
		}
	} else {
		// Extends the current main chain — simple append.
		if b.Header.PrevBlockHash == l.Chain.LastHash() {
			// Snapshot inputs before applying.
			snap := l.UTXOSet.SnapshotInputs(b)
			_ = snap // stored for potential future rollback

			if errApply := l.UTXOSet.ApplyBlock(b); errApply != nil {
				return false, false, fmt.Errorf("UTXO apply failed: %w", errApply)
			}

			// Track mined coins.
			if len(b.Transactions) > 0 && b.Transactions[0].IsCoinbase() && b.Header.Height > 0 {
				l.Chain.TotalMined += b.Transactions[0].TotalOutputValue()
			}

			l.Chain.Blocks = append(l.Chain.Blocks, b)

			// Remove confirmed transactions from mempool.
			for _, tx := range b.Transactions {
				l.Mempool.Remove(tx.ID)
			}

			l.Balances = l.UTXOSet.AllBalances()
			_ = l.saveLocked()

			slog.Debug("Block added to main chain",
				"height", b.Header.Height,
				"hash", b.Hash[:16],
			)
		}
		// If not extending main chain tip, it's a side-chain block — already indexed.
	}

	// Process any orphans that were waiting for this block.
	for _, orphan := range result.ConnectedOrphans {
		slog.Debug("Processing connected orphan",
			"hash", orphan.Hash[:16],
			"height", orphan.Header.Height,
		)
		// Recursive call without holding lock (already held).
		// We need to process these through the index again.
		orphanResult := idx.AddBlock(orphan)
		if orphanResult.Added && !orphanResult.IsOrphan {
			if orphanResult.ReorgOccurred {
				if err := l.performReorgLocked(orphanResult.OldTip, orphanResult.NewTip); err != nil {
					slog.Error("Orphan reorg failed", "error", err)
				}
			} else if orphan.Header.PrevBlockHash == l.Chain.LastHash() {
				if errApply := l.UTXOSet.ApplyBlock(orphan); errApply == nil {
					if len(orphan.Transactions) > 0 && orphan.Transactions[0].IsCoinbase() && orphan.Header.Height > 0 {
						l.Chain.TotalMined += orphan.Transactions[0].TotalOutputValue()
					}
					l.Chain.Blocks = append(l.Chain.Blocks, orphan)
					for _, tx := range orphan.Transactions {
						l.Mempool.Remove(tx.ID)
					}
					l.Balances = l.UTXOSet.AllBalances()
					_ = l.saveLocked()
				}
			}
		}
	}

	return true, false, nil
}

// performReorgLocked performs a chain reorganization. Must hold l.mu.
// It finds the fork point between the old and new best chain tips,
// rolls back the UTXO set to the fork point, and applies the new chain.
func (l *Ledger) performReorgLocked(oldTipHash, newTipHash string) error {
	idx := l.Chain.Index
	forkPoint, disconnect, connect, err := idx.FindForkPoint(oldTipHash, newTipHash)
	if err != nil {
		return fmt.Errorf("find fork point: %w", err)
	}

	slog.Info("Reorg details",
		"fork_point", forkPoint.Hash[:16],
		"fork_height", forkPoint.Height,
		"disconnect", len(disconnect),
		"connect", len(connect),
	)

	// Rebuild UTXO from genesis up to (and including) the fork point.
	// This is the safest approach — rebuild from scratch up to fork.
	var blocksToFork []*block.Block
	current := forkPoint
	var path []*chain.BlockNode
	for current != nil {
		path = append(path, current)
		parent := idx.GetNode(current.ParentHash)
		if parent == nil {
			break
		}
		current = parent
	}
	// Reverse to get genesis-first order.
	for i := len(path) - 1; i >= 0; i-- {
		if path[i].Block != nil {
			blocksToFork = append(blocksToFork, path[i].Block)
		}
	}

	newUTXO, err := utxo.RebuildFromBlocks(blocksToFork)
	if err != nil {
		return fmt.Errorf("UTXO rebuild to fork point failed: %w", err)
	}

	// Collect transactions from disconnected blocks to potentially return to mempool.
	var returnToMempool []block.Transaction
	for _, node := range disconnect {
		if node.Block != nil {
			for _, tx := range node.Block.Transactions {
				if !tx.IsCoinbase() && !tx.IsGenesis() {
					returnToMempool = append(returnToMempool, tx)
				}
			}
		}
	}

	// Apply the new branch blocks (connect) to the UTXO set.
	var newBlocks []*block.Block
	newBlocks = append(newBlocks, blocksToFork...)
	for _, node := range connect {
		if node.Block == nil {
			return fmt.Errorf("missing block data for %s", node.Hash[:16])
		}
		if err := newUTXO.ApplyBlock(node.Block); err != nil {
			return fmt.Errorf("apply connect block %d: %w", node.Height, err)
		}
		newBlocks = append(newBlocks, node.Block)
	}

	// Update chain state.
	l.Chain.Blocks = newBlocks
	l.UTXOSet = newUTXO
	l.Balances = newUTXO.AllBalances()
	l.Chain.RecalcTotalMined()
	idx.UpdateMainChainStatus()

	// Save new branch blocks to blockstore (HIGH-1).
	if l.store != nil {
		for _, node := range connect {
			if node.Block != nil && !l.store.HasBlock(node.Block.Hash) {
				_ = l.store.SaveBlock(node.Block)
			}
		}
	}

	_ = l.saveLocked()

	slog.Info("Reorg completed",
		"new_height", l.Chain.Height(),
		"disconnected", len(disconnect),
		"connected", len(connect),
		"utxo_count", newUTXO.Size(),
	)

	// Return disconnected transactions to mempool (best effort).
	for _, tx := range returnToMempool {
		if tx.ID == "" {
			tx.ID = block.HashTransaction(&tx)
		}
		_ = l.Mempool.Add(tx)
	}

	return nil
}

// GetBlockIndex returns the chain's block index.
func (l *Ledger) GetBlockIndex() *chain.BlockIndex {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.Chain.Index
}

// ──────────────────────────────────────────────────────────────────────────────
// Persistence — HIGH-1: crash-safe blockstore + chainstate
// ──────────────────────────────────────────────────────────────────────────────

// Save persists the ledger state to the blockstore (thread-safe wrapper).
func (l *Ledger) Save() error {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.saveLocked()
}

// saveLocked writes metadata + chainstate to the store. Must be called with lock held.
// Individual blocks are saved when they are added (not in bulk).
func (l *Ledger) saveLocked() error {
	if l.store == nil {
		// Fallback to legacy JSON if store is not available.
		return l.saveLegacyJSON()
	}

	if err := l.saveMetadataToStore(); err != nil {
		slog.Warn("Failed to save metadata", "error", err)
		return err
	}
	if err := l.saveChainstateToStore(); err != nil {
		slog.Warn("Failed to save chainstate", "error", err)
		return err
	}
	return nil
}

// saveMetadataToStore writes node metadata atomically.
func (l *Ledger) saveMetadataToStore() error {
	if l.store == nil {
		return nil
	}
	meta := &storage.NodeMetadata{
		BestTipHash:  l.Chain.LastHash(),
		BestHeight:   l.Chain.Height(),
		TargetHex:    l.Chain.TargetHex,
		TotalMined:   l.Chain.TotalMined,
		TotalFaucet:  l.Chain.TotalFaucet,
		GenesisOwner: l.Chain.GenesisOwner,
	}
	return l.store.SaveMetadata(meta)
}

// saveChainstateToStore writes the UTXO set snapshot atomically.
func (l *Ledger) saveChainstateToStore() error {
	if l.store == nil || l.UTXOSet == nil {
		return nil
	}
	snap := utxoSetToSnapshot(l.UTXOSet, l.Chain.Height(), l.Chain.LastHash())
	return l.store.SaveChainstate(snap)
}

// saveBlockToStore saves a single block to the blockstore.
func (l *Ledger) saveBlockToStore(b *block.Block) error {
	if l.store == nil {
		return nil
	}
	return l.store.SaveBlock(b)
}

// saveLegacyJSON is the fallback for when the store is not available.
func (l *Ledger) saveLegacyJSON() error {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(l.filePath, data, 0644)
}

// utxoSetToSnapshot converts a UTXO set to a storage snapshot.
func utxoSetToSnapshot(s *utxo.Set, height uint64, hash string) *storage.ChainstateSnapshot {
	allUTXOs := s.GetAllUTXOs()
	entries := make([]storage.UTXOEntry, 0, len(allUTXOs))
	for _, u := range allUTXOs {
		entries = append(entries, storage.UTXOEntry{
			TxID:    u.OutPoint.TxID,
			Index:   u.OutPoint.Index,
			Address: u.Output.Address,
			Amount:  u.Output.Amount,
		})
	}
	return &storage.ChainstateSnapshot{
		Height: height,
		Hash:   hash,
		UTXOs:  entries,
	}
}

// utxoSetFromSnapshot rebuilds a UTXO set from a storage snapshot.
func utxoSetFromSnapshot(snap *storage.ChainstateSnapshot) *utxo.Set {
	s := utxo.NewSet()
	for _, entry := range snap.UTXOs {
		op := utxo.OutPoint{TxID: entry.TxID, Index: entry.Index}
		out := utxo.Output{Address: entry.Address, Amount: entry.Amount}
		s.Add(op, out)
	}
	return s
}

// storeDirFromPath derives the storage directory from the legacy file path.
// If the path ends with ".json", we use a sibling directory.
// Otherwise, we use the path directly as the directory.
func storeDirFromPath(path string) string {
	if filepath.Ext(path) == ".json" {
		dir := filepath.Dir(path)
		base := filepath.Base(path)
		name := base[:len(base)-len(filepath.Ext(base))]
		return filepath.Join(dir, name+"_store")
	}
	return path + "_store"
}

// GetStore returns the underlying storage engine (may be nil).
func (l *Ledger) GetStore() *storage.Store {
	return l.store
}

// LoadLedger reads a ledger from the storage. Falls back to a new ledger on failure.
// HIGH-1: First attempts to load from the new blockstore. If the blockstore is
// empty, it checks for a legacy node_data.json and migrates it.
func LoadLedger(filePath string) *Ledger {
	return LoadLedgerWithOwner(filePath, block.LegacyGenesisAddress)
}

// LoadLedgerWithOwner reads a ledger from storage with a specific genesis owner.
// If no data exists, creates a new chain with the given owner.
// HIGH-1: Uses crash-safe blockstore + chainstate instead of monolithic JSON.
func LoadLedgerWithOwner(filePath string, genesisOwner string) *Ledger {
	storeDir := storeDirFromPath(filePath)

	// Initialize storage engine.
	st, err := storage.NewStore(storeDir)
	if err != nil {
		slog.Error("Failed to initialize storage, falling back to legacy",
			"error", err, "store_dir", storeDir)
		return loadLedgerLegacy(filePath, genesisOwner)
	}

	// Attempt legacy migration if blockstore is empty.
	if st.BlockCount() == 0 {
		migrated, migrErr := st.MigrateFromLegacy(filePath)
		if migrErr != nil {
			slog.Warn("Legacy migration failed", "error", migrErr)
		} else if migrated {
			slog.Info("Legacy data migrated to new blockstore",
				"legacy_path", filePath, "store_dir", storeDir)
		}
	}

	// If blockstore is still empty, create a new chain.
	if st.BlockCount() == 0 {
		slog.Info("No stored data found, starting fresh with configured genesis owner",
			"store_dir", storeDir, "genesis_owner", genesisOwner)
		l := NewLedgerWithOwner(filePath, genesisOwner)
		return l
	}

	// Load metadata.
	meta, err := st.LoadMetadata()
	if err != nil {
		slog.Warn("Failed to load metadata, rebuilding from blockstore", "error", err)
		meta = nil
	}

	// Load all blocks from blockstore (ordered by height).
	blocks, err := st.LoadAllBlocksOrdered()
	if err != nil {
		slog.Error("Failed to load blocks from blockstore", "error", err)
		return NewLedgerWithOwner(filePath, genesisOwner)
	}

	// Check for legacy chain data.
	for _, b := range blocks {
		if block.IsLegacyBlock(b) {
			slog.Error("FATAL: blockstore contains legacy account-model transactions",
				"block_height", b.Header.Height)
			slog.Error("The chain data format is incompatible with the current UTXO input/output model.")
			slog.Error("To migrate: delete the data directory and restart with a fresh chain.")
			return NewLedgerWithOwner(filePath, genesisOwner)
		}
	}

	// Reconstruct the Blockchain.
	bc := &chain.Blockchain{
		Blocks:  blocks,
		Index:   chain.NewBlockIndex(),
	}

	// Apply metadata if available.
	if meta != nil {
		bc.TotalMined = meta.TotalMined
		bc.TotalFaucet = meta.TotalFaucet
		bc.GenesisOwner = meta.GenesisOwner
		if meta.TargetHex != "" {
			bc.Target = block.TargetFromBits(meta.TargetHex)
			bc.TargetHex = meta.TargetHex
		} else {
			bc.Target = block.InitialTarget
			bc.TargetHex = block.BitsFromTarget(block.InitialTarget)
		}
	} else {
		bc.Target = block.InitialTarget
		bc.TargetHex = block.BitsFromTarget(block.InitialTarget)
		// Back-fill from genesis block.
		if len(blocks) > 0 {
			if owner, ok := block.GenesisOwnerFromBlock(blocks[0]); ok {
				bc.GenesisOwner = owner
			}
		}
	}

	// Build block index.
	bc.Index.BuildFromBlocks(blocks)

	// Rebuild or load UTXO set.
	var utxoSet *utxo.Set

	// First try the chainstate snapshot.
	snap, snapErr := st.LoadChainstate()
	if snapErr == nil && snap != nil && snap.Hash == bc.LastHash() {
		// Snapshot matches current tip — use it directly.
		utxoSet = utxoSetFromSnapshot(snap)
		slog.Info("UTXO set loaded from chainstate snapshot",
			"height", snap.Height, "utxos", len(snap.UTXOs))
	} else {
		// Snapshot missing or stale — rebuild from blocks.
		if snapErr != nil {
			slog.Warn("Chainstate load failed, rebuilding UTXO from blockstore", "error", snapErr)
		} else if snap != nil {
			slog.Info("Chainstate snapshot stale, rebuilding UTXO from blockstore",
				"snap_hash", shortAddr(snap.Hash), "tip_hash", shortAddr(bc.LastHash()))
		} else {
			slog.Info("No chainstate snapshot, rebuilding UTXO from blockstore")
		}
		utxoSet, err = utxo.RebuildFromBlocks(blocks)
		if err != nil {
			slog.Warn("UTXO rebuild from blockstore failed", "error", err)
			utxoSet = utxo.NewSet()
		}
	}

	balances := utxoSet.AllBalances()

	l := &Ledger{
		Chain:    bc,
		Balances: balances,
		filePath: filePath,
		store:    st,
		UTXOSet:  utxoSet,
		Mempool:  mempool.New(mempool.DefaultMaxSize),
	}

	// Save updated chainstate if it was rebuilt.
	if snap == nil || (snap != nil && snap.Hash != bc.LastHash()) {
		_ = l.saveChainstateToStore()
	}

	slog.Info("Ledger loaded from blockstore",
		"blocks", bc.Len(),
		"store_dir", storeDir,
		"utxo_count", utxoSet.Size(),
		"genesis_owner", l.GenesisOwner(),
	)
	return l
}

// loadLedgerLegacy is the fallback loader using the old monolithic JSON format.
// Used only when the new storage engine cannot be initialized.
func loadLedgerLegacy(filePath string, genesisOwner string) *Ledger {
	data, err := os.ReadFile(filePath)
	if err != nil {
		slog.Info("No legacy data file found, starting fresh",
			"path", filePath, "genesis_owner", genesisOwner)
		return newLedgerWithOwnerNoStore(filePath, genesisOwner)
	}

	var l Ledger
	if err := json.Unmarshal(data, &l); err != nil {
		slog.Warn("Failed to parse legacy data file, starting fresh",
			"path", filePath, "error", err)
		return newLedgerWithOwnerNoStore(filePath, genesisOwner)
	}
	l.filePath = filePath
	if l.Balances == nil {
		l.Balances = make(map[string]float64)
	}

	if l.Chain != nil && l.Chain.Target == nil {
		if l.Chain.TargetHex != "" {
			l.Chain.Target = block.TargetFromBits(l.Chain.TargetHex)
		} else {
			l.Chain.Target = block.InitialTarget
		}
	}

	if l.Chain != nil && l.Chain.GenesisOwner == "" && len(l.Chain.Blocks) > 0 {
		if owner, ok := block.GenesisOwnerFromBlock(l.Chain.Blocks[0]); ok {
			l.Chain.GenesisOwner = owner
		}
	}

	if l.Chain != nil {
		if hasLegacy, height := chain.ContainsLegacyBlocks(l.Chain); hasLegacy {
			slog.Error("FATAL: legacy chain data contains account-model transactions",
				"block_height", height)
			return newLedgerWithOwnerNoStore(filePath, genesisOwner)
		}
	}

	if l.Chain != nil {
		utxoSet, err := utxo.RebuildFromBlocks(l.Chain.Blocks)
		if err != nil {
			slog.Warn("UTXO rebuild failed", "error", err)
			utxoSet = utxo.NewSet()
		} else {
			l.Balances = utxoSet.AllBalances()
		}
		l.UTXOSet = utxoSet
	} else {
		l.UTXOSet = utxo.NewSet()
	}

	l.Mempool = mempool.New(mempool.DefaultMaxSize)

	if l.Chain != nil && l.Chain.Index == nil {
		l.Chain.Index = chain.NewBlockIndex()
		l.Chain.Index.BuildFromBlocks(l.Chain.Blocks)
	}

	slog.Info("Ledger loaded (legacy fallback)",
		"blocks", l.Chain.Len(),
		"path", filePath,
		"utxo_count", l.UTXOSet.Size(),
		"genesis_owner", l.GenesisOwner(),
	)
	return &l
}

// newLedgerWithOwnerNoStore creates a new ledger without the storage engine.
func newLedgerWithOwnerNoStore(filePath string, genesisOwner string) *Ledger {
	bc := chain.NewBlockchainWithOwner(genesisOwner)
	utxoSet, _ := utxo.RebuildFromBlocks(bc.Blocks)
	if utxoSet == nil {
		utxoSet = utxo.NewSet()
	}
	return &Ledger{
		Chain:    bc,
		Balances: utxoSet.AllBalances(),
		filePath: filePath,
		UTXOSet:  utxoSet,
		Mempool:  mempool.New(mempool.DefaultMaxSize),
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Accessors
// ──────────────────────────────────────────────────────────────────────────────

// GetChain returns a pointer to the underlying blockchain.
func (l *Ledger) GetChain() *chain.Blockchain {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.Chain
}

// GetChainHeight returns the current blockchain height.
func (l *Ledger) GetChainHeight() uint64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.Chain.Height()
}

// GetBlockReward returns the mining reward for the next block.
func (l *Ledger) GetBlockReward() float64 {
	return l.Chain.GetBlockReward()
}

// GetMempoolSize returns the number of transactions in the mempool.
func (l *Ledger) GetMempoolSize() int {
	return l.Mempool.Size()
}

// GetPendingTransactions returns up to limit pending transactions from the mempool.
func (l *Ledger) GetPendingTransactions(limit int) []block.Transaction {
	return l.Mempool.GetPending(limit)
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

// shortAddr returns the first 8 and last 4 chars of an address for logging.
func shortAddr(addr string) string {
	if len(addr) <= 16 {
		return addr
	}
	return addr[:8] + "..." + addr[len(addr)-4:]
}
