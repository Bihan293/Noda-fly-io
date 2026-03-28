// Package utxo implements an Unspent Transaction Output set.
//
// The UTXO set tracks every unspent output in the blockchain. Each output is
// identified by a composite key (txID + output index). When a transaction
// consumes an output, it is marked as spent and removed from the set.
//
// CRITICAL-2: Transactions now carry explicit inputs and outputs.
// ApplyBlock spends exactly the outpoints listed in each transaction's inputs
// and creates new outputs as listed in the transaction's outputs.
//
// The UTXO model enables:
//   - Efficient balance lookups (sum of unspent outputs for an address)
//   - Double-spend detection (an output can only be spent once)
//   - Rebuilding the full set from the blockchain
//   - Thread-safe concurrent access
package utxo

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/Bihan293/Noda/block"
)

// ──────────────────────────────────────────────────────────────────────────────
// Types
// ──────────────────────────────────────────────────────────────────────────────

// OutPoint uniquely identifies a transaction output.
type OutPoint struct {
	TxID  string `json:"tx_id"`  // transaction hash
	Index int    `json:"index"`  // output index within the transaction
}

// String returns a human-readable representation of the outpoint.
func (op OutPoint) String() string {
	return fmt.Sprintf("%s:%d", op.TxID, op.Index)
}

// Key returns a string key for map lookups.
func (op OutPoint) Key() string {
	return fmt.Sprintf("%s:%d", op.TxID, op.Index)
}

// Output represents a single unspent transaction output.
type Output struct {
	Address string  `json:"address"` // owner address (public key hex)
	Amount  float64 `json:"amount"`  // coin amount
}

// ──────────────────────────────────────────────────────────────────────────────
// UTXO Set
// ──────────────────────────────────────────────────────────────────────────────

// Set is the main UTXO set — a map from OutPoint keys to Outputs.
type Set struct {
	utxos map[string]*utxoEntry // key = OutPoint.Key()
	mu    sync.RWMutex
}

// utxoEntry pairs an OutPoint with its Output for internal tracking.
type utxoEntry struct {
	OutPoint OutPoint `json:"outpoint"`
	Output   Output   `json:"output"`
}

// NewSet creates an empty UTXO set.
func NewSet() *Set {
	return &Set{
		utxos: make(map[string]*utxoEntry),
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Add / Spend
// ──────────────────────────────────────────────────────────────────────────────

// Add inserts an unspent output into the set.
func (s *Set) Add(op OutPoint, out Output) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.utxos[op.Key()] = &utxoEntry{
		OutPoint: op,
		Output:   out,
	}
}

// Spend removes an output from the set (marks it as spent).
// Returns the spent output, or an error if the output doesn't exist (double-spend).
func (s *Set) Spend(op OutPoint) (*Output, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := op.Key()
	entry, exists := s.utxos[key]
	if !exists {
		return nil, fmt.Errorf("utxo not found: %s (possible double-spend)", key)
	}

	out := entry.Output
	delete(s.utxos, key)
	return &out, nil
}

// Has checks whether an output exists in the UTXO set.
func (s *Set) Has(op OutPoint) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, exists := s.utxos[op.Key()]
	return exists
}

// Get returns the output at the given outpoint, or nil if not found.
func (s *Set) Get(op OutPoint) *Output {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, exists := s.utxos[op.Key()]
	if !exists {
		return nil
	}
	out := entry.Output
	return &out
}

// ──────────────────────────────────────────────────────────────────────────────
// Balance Queries
// ──────────────────────────────────────────────────────────────────────────────

// Balance returns the total unspent amount for a given address.
func (s *Set) Balance(address string) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var total float64
	for _, entry := range s.utxos {
		if entry.Output.Address == address {
			total += entry.Output.Amount
		}
	}
	return total
}

// AllBalances returns a map of address -> total unspent balance for all addresses.
func (s *Set) AllBalances() map[string]float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	balances := make(map[string]float64)
	for _, entry := range s.utxos {
		balances[entry.Output.Address] += entry.Output.Amount
	}
	return balances
}

// GetUTXOsForAddress returns all unspent outputs belonging to the given address.
func (s *Set) GetUTXOsForAddress(address string) []struct {
	OutPoint OutPoint
	Output   Output
} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []struct {
		OutPoint OutPoint
		Output   Output
	}
	for _, entry := range s.utxos {
		if entry.Output.Address == address {
			result = append(result, struct {
				OutPoint OutPoint
				Output   Output
			}{
				OutPoint: entry.OutPoint,
				Output:   entry.Output,
			})
		}
	}
	return result
}

// GetAllUTXOs returns all unspent outputs in the set.
// HIGH-1: Used for serialising the UTXO set to the chainstate snapshot.
func (s *Set) GetAllUTXOs() []struct {
	OutPoint OutPoint
	Output   Output
} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]struct {
		OutPoint OutPoint
		Output   Output
	}, 0, len(s.utxos))
	for _, entry := range s.utxos {
		result = append(result, struct {
			OutPoint OutPoint
			Output   Output
		}{
			OutPoint: entry.OutPoint,
			Output:   entry.Output,
		})
	}
	return result
}

// FindUTXOsForAmount finds enough UTXOs from the given address to cover the
// requested amount. Returns the selected UTXOs and the total value.
// Returns an error if insufficient funds.
func (s *Set) FindUTXOsForAmount(address string, amount float64) ([]OutPoint, float64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var selected []OutPoint
	var total float64

	for _, entry := range s.utxos {
		if entry.Output.Address != address {
			continue
		}
		selected = append(selected, entry.OutPoint)
		total += entry.Output.Amount
		if total >= amount {
			return selected, total, nil
		}
	}

	if total < amount {
		return nil, total, fmt.Errorf("insufficient funds: address %s has %.6f, needs %.6f",
			shortAddr(address), total, amount)
	}
	return selected, total, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Size & Stats
// ──────────────────────────────────────────────────────────────────────────────

// Size returns the number of unspent outputs in the set.
func (s *Set) Size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.utxos)
}

// ──────────────────────────────────────────────────────────────────────────────
// Block Processing — Apply / Rollback (CRITICAL-2: explicit inputs/outputs)
// ──────────────────────────────────────────────────────────────────────────────

// ApplyBlock processes all transactions in a block, updating the UTXO set.
// For each transaction:
//   - Coinbase/genesis (IsCoinbase()/IsGenesis()): only creates outputs, no inputs to consume
//   - Regular: spends exactly the outpoints listed in tx.Inputs and creates tx.Outputs
//
// This is the CRITICAL-2 model: no more searching for "sender UTXOs by address".
// Each transaction explicitly declares which outputs it spends via its Inputs.
func (s *Set) ApplyBlock(b *block.Block) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, tx := range b.Transactions {
		if tx.IsCoinbase() || tx.IsGenesis() {
			// Coinbase / genesis transaction: create outputs only.
			for j, out := range tx.Outputs {
				op := OutPoint{TxID: tx.ID, Index: j}
				s.utxos[op.Key()] = &utxoEntry{
					OutPoint: op,
					Output: Output{
						Address: out.Address,
						Amount:  out.Amount,
					},
				}
			}
			continue
		}

		// Regular transaction: spend explicitly referenced inputs.
		for _, in_ := range tx.Inputs {
			op := OutPoint{TxID: in_.PrevTxID, Index: in_.PrevIndex}
			key := op.Key()
			if _, exists := s.utxos[key]; !exists {
				return fmt.Errorf("block %d, tx %d: utxo not found %s (double-spend or missing input)",
					b.Header.Height, i, key)
			}
			delete(s.utxos, key)
		}

		// Create outputs.
		for j, out := range tx.Outputs {
			op := OutPoint{TxID: tx.ID, Index: j}
			s.utxos[op.Key()] = &utxoEntry{
				OutPoint: op,
				Output: Output{
					Address: out.Address,
					Amount:  out.Amount,
				},
			}
		}
	}

	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Rollback — undo a block (CRITICAL-4: reorg support)
// ──────────────────────────────────────────────────────────────────────────────

// RollbackBlock reverses the effects of a block on the UTXO set.
// For each transaction (processed in reverse order):
//   - Regular tx: remove the outputs it created, re-add the outputs it spent.
//   - Coinbase/genesis tx: remove the outputs it created.
//
// The inputValues map provides the original Output for each spent input,
// keyed by "txID:index". This is needed because the UTXO set no longer has
// the spent outputs. If inputValues is nil, the caller must provide the block's
// input context some other way (e.g., from the block data itself with the UTXO
// that was present before the block was applied).
//
// NOTE: For simplicity this implementation requires that inputValues provides
// the Address and Amount for every input spent by the block's transactions.
func (s *Set) RollbackBlock(b *block.Block, inputValues map[string]Output) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Process transactions in reverse order.
	for i := len(b.Transactions) - 1; i >= 0; i-- {
		tx := b.Transactions[i]

		// Remove outputs created by this transaction.
		for j := range tx.Outputs {
			op := OutPoint{TxID: tx.ID, Index: j}
			key := op.Key()
			delete(s.utxos, key)
		}

		// Restore inputs consumed by this transaction (skip coinbase/genesis).
		if !tx.IsCoinbase() && !tx.IsGenesis() {
			for _, in_ := range tx.Inputs {
				op := OutPoint{TxID: in_.PrevTxID, Index: in_.PrevIndex}
				key := op.Key()
				prevOut, ok := inputValues[key]
				if !ok {
					return fmt.Errorf("rollback: missing input value for %s", key)
				}
				s.utxos[key] = &utxoEntry{
					OutPoint: op,
					Output:   prevOut,
				}
			}
		}
	}

	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Snapshot — for recording input values before applying a block (CRITICAL-4)
// ──────────────────────────────────────────────────────────────────────────────

// SnapshotInputs records the UTXO outputs that will be consumed by the given
// block's transactions. This must be called BEFORE ApplyBlock so the values
// can be used later for RollbackBlock.
func (s *Set) SnapshotInputs(b *block.Block) map[string]Output {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snap := make(map[string]Output)
	for _, tx := range b.Transactions {
		if tx.IsCoinbase() || tx.IsGenesis() {
			continue
		}
		for _, in_ := range tx.Inputs {
			op := OutPoint{TxID: in_.PrevTxID, Index: in_.PrevIndex}
			key := op.Key()
			if entry, exists := s.utxos[key]; exists {
				snap[key] = entry.Output
			}
		}
	}
	return snap
}

// Clone creates a deep copy of the UTXO set.
func (s *Set) Clone() *Set {
	s.mu.RLock()
	defer s.mu.RUnlock()

	clone := &Set{
		utxos: make(map[string]*utxoEntry, len(s.utxos)),
	}
	for k, v := range s.utxos {
		clone.utxos[k] = &utxoEntry{
			OutPoint: v.OutPoint,
			Output:   v.Output,
		}
	}
	return clone
}

// ──────────────────────────────────────────────────────────────────────────────
// Rebuild from blockchain
// ──────────────────────────────────────────────────────────────────────────────

// RebuildFromBlocks rebuilds the entire UTXO set by replaying all blocks.
func RebuildFromBlocks(blocks []*block.Block) (*Set, error) {
	s := NewSet()
	for _, b := range blocks {
		if err := s.ApplyBlock(b); err != nil {
			return nil, fmt.Errorf("rebuild failed at block %d: %w", b.Header.Height, err)
		}
	}
	log.Printf("[UTXO] Rebuilt from %d blocks — %d unspent outputs", len(blocks), s.Size())
	return s, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Serialization
// ──────────────────────────────────────────────────────────────────────────────

// MarshalJSON serializes the UTXO set to JSON.
func (s *Set) MarshalJSON() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries := make([]*utxoEntry, 0, len(s.utxos))
	for _, entry := range s.utxos {
		entries = append(entries, entry)
	}
	return json.Marshal(entries)
}

// UnmarshalJSON deserializes the UTXO set from JSON.
func (s *Set) UnmarshalJSON(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var entries []*utxoEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return err
	}

	s.utxos = make(map[string]*utxoEntry, len(entries))
	for _, entry := range entries {
		s.utxos[entry.OutPoint.Key()] = entry
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

func shortAddr(addr string) string {
	if len(addr) <= 16 {
		return addr
	}
	return addr[:8] + "..." + addr[len(addr)-4:]
}
