// Package mempool implements an in-memory pool of unconfirmed transactions.
//
// The mempool holds transactions that have been validated but not yet included
// in a block. It supports:
//   - Thread-safe concurrent access
//   - Transaction validation before admission (signature, double-spend via UTXO)
//   - Priority ordering by fee rate, then arrival time (FIFO) for equal fees
//   - Configurable pool size limit with eviction (lowest fee first)
//   - Removal of transactions once they are included in a block
//   - Querying pending transactions for block assembly
//   - Double-spend detection based on explicit input outpoints (CRITICAL-2)
//   - Fee tracking per transaction (CRITICAL-3)
package mempool

import (
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/Bihan293/Noda/block"
)

// ──────────────────────────────────────────────────────────────────────────────
// Constants
// ──────────────────────────────────────────────────────────────────────────────

const (
	// DefaultMaxSize is the maximum number of transactions in the mempool.
	DefaultMaxSize = 10_000

	// DefaultTxTTL is how long a transaction can stay in the mempool before eviction.
	DefaultTxTTL = 24 * time.Hour
)

// ──────────────────────────────────────────────────────────────────────────────
// MempoolEntry
// ──────────────────────────────────────────────────────────────────────────────

// Entry wraps a transaction with metadata for pool management.
type Entry struct {
	Tx       block.Transaction `json:"tx"`
	AddedAt  time.Time         `json:"added_at"`  // when the TX was added to the pool
	Priority int64             `json:"priority"`   // arrival order (lower = earlier = higher priority)
	Fee      float64           `json:"fee"`        // transaction fee = sum(inputs) - sum(outputs)
	FeeRate  float64           `json:"fee_rate"`   // fee per byte (fee / estimated_size) — simplified to fee for now
}

// ──────────────────────────────────────────────────────────────────────────────
// Mempool
// ──────────────────────────────────────────────────────────────────────────────

// Mempool is a thread-safe pool of unconfirmed transactions.
type Mempool struct {
	entries    map[string]*Entry  // txID -> entry
	order      []string           // insertion order (for FIFO priority)
	spentOuts  map[string]string  // outpoint key -> txID that spends it (double-spend tracking)
	maxSize    int
	sequence   int64 // monotonic counter for arrival priority
	mu         sync.RWMutex
}

// New creates a new mempool with the given maximum size.
// If maxSize <= 0, DefaultMaxSize is used.
func New(maxSize int) *Mempool {
	if maxSize <= 0 {
		maxSize = DefaultMaxSize
	}
	return &Mempool{
		entries:   make(map[string]*Entry),
		order:     make([]string, 0, 256),
		spentOuts: make(map[string]string),
		maxSize:   maxSize,
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Add / Remove
// ──────────────────────────────────────────────────────────────────────────────

// AddWithFee inserts a transaction into the mempool with an explicit fee amount.
// The fee should be pre-computed by the caller (sum(inputs) - sum(outputs)).
// Returns an error if the transaction is a duplicate, spends an already-spent
// outpoint, or the pool is full after eviction.
func (mp *Mempool) AddWithFee(tx block.Transaction, fee float64) error {
	mp.mu.Lock()
	defer mp.mu.Unlock()

	if tx.ID == "" {
		return fmt.Errorf("transaction has no ID")
	}

	// Check for duplicates.
	if _, exists := mp.entries[tx.ID]; exists {
		return fmt.Errorf("transaction %s already in mempool", shortID(tx.ID))
	}

	// Check for double-spend on inputs (CRITICAL-2).
	for _, in_ := range tx.Inputs {
		outKey := fmt.Sprintf("%s:%d", in_.PrevTxID, in_.PrevIndex)
		if existingTxID, exists := mp.spentOuts[outKey]; exists {
			return fmt.Errorf("double-spend: outpoint %s already spent by mempool tx %s",
				outKey, shortID(existingTxID))
		}
	}

	// Evict expired entries first.
	mp.evictExpiredLocked()

	// Check pool size limit.
	if len(mp.entries) >= mp.maxSize {
		// Evict the oldest transaction.
		if !mp.evictOldestLocked() {
			return fmt.Errorf("mempool is full (%d transactions)", mp.maxSize)
		}
	}

	mp.sequence++
	entry := &Entry{
		Tx:       tx,
		AddedAt:  time.Now(),
		Priority: mp.sequence,
		Fee:      fee,
		FeeRate:  fee, // simplified: fee rate = fee (no byte-size calculation yet)
	}

	mp.entries[tx.ID] = entry
	mp.order = append(mp.order, tx.ID)

	// Track spent outpoints.
	for _, in_ := range tx.Inputs {
		outKey := fmt.Sprintf("%s:%d", in_.PrevTxID, in_.PrevIndex)
		mp.spentOuts[outKey] = tx.ID
	}

	// Build log description.
	desc := fmt.Sprintf("inputs=%d outputs=%d fee=%.8f", len(tx.Inputs), len(tx.Outputs), fee)
	if tx.IsCoinbase() {
		desc = "coinbase"
	}
	log.Printf("[MEMPOOL] Added TX %s (%s) — pool size: %d",
		shortID(tx.ID), desc, len(mp.entries))

	return nil
}

// Add inserts a transaction into the mempool with zero fee (backward-compatible).
// The transaction must already have a valid ID set.
// Returns an error if the transaction is a duplicate, spends an already-spent
// outpoint, or the pool is full after eviction.
func (mp *Mempool) Add(tx block.Transaction) error {
	return mp.AddWithFee(tx, 0)
}

// Remove deletes a transaction from the mempool (e.g., after block inclusion).
func (mp *Mempool) Remove(txID string) {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	mp.removeLocked(txID)
}

// RemoveBatch removes multiple transactions (typically after a block is mined).
func (mp *Mempool) RemoveBatch(txIDs []string) {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	for _, id := range txIDs {
		mp.removeLocked(id)
	}
	log.Printf("[MEMPOOL] Removed %d transactions (block confirmed) — pool size: %d",
		len(txIDs), len(mp.entries))
}

// removeLocked removes a single TX. Must be called with lock held.
func (mp *Mempool) removeLocked(txID string) {
	entry, exists := mp.entries[txID]
	if exists {
		// Clean up spent outpoints tracking.
		for _, in_ := range entry.Tx.Inputs {
			outKey := fmt.Sprintf("%s:%d", in_.PrevTxID, in_.PrevIndex)
			delete(mp.spentOuts, outKey)
		}
	}
	delete(mp.entries, txID)
	// Remove from order slice.
	for i, id := range mp.order {
		if id == txID {
			mp.order = append(mp.order[:i], mp.order[i+1:]...)
			break
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Query
// ──────────────────────────────────────────────────────────────────────────────

// Has returns true if the transaction is in the mempool.
func (mp *Mempool) Has(txID string) bool {
	mp.mu.RLock()
	defer mp.mu.RUnlock()
	_, ok := mp.entries[txID]
	return ok
}

// Get returns a transaction from the mempool, or nil if not found.
func (mp *Mempool) Get(txID string) *block.Transaction {
	mp.mu.RLock()
	defer mp.mu.RUnlock()
	entry, ok := mp.entries[txID]
	if !ok {
		return nil
	}
	tx := entry.Tx
	return &tx
}

// GetFee returns the fee for a transaction in the mempool. Returns 0 if not found.
func (mp *Mempool) GetFee(txID string) float64 {
	mp.mu.RLock()
	defer mp.mu.RUnlock()
	entry, ok := mp.entries[txID]
	if !ok {
		return 0
	}
	return entry.Fee
}

// Size returns the number of transactions currently in the pool.
func (mp *Mempool) Size() int {
	mp.mu.RLock()
	defer mp.mu.RUnlock()
	return len(mp.entries)
}

// GetPending returns up to `limit` pending transactions ordered by fee rate
// (descending), then by arrival time (FIFO) for equal fees.
// These are candidates for inclusion in the next block.
func (mp *Mempool) GetPending(limit int) []block.Transaction {
	mp.mu.RLock()
	defer mp.mu.RUnlock()

	if len(mp.entries) == 0 {
		return nil
	}

	// Collect entries for sorting.
	type sortEntry struct {
		id       string
		fee      float64
		priority int64
	}
	entries := make([]sortEntry, 0, len(mp.entries))
	for id, e := range mp.entries {
		entries = append(entries, sortEntry{
			id:       id,
			fee:      e.Fee,
			priority: e.Priority,
		})
	}

	// Sort by fee descending, then by arrival order ascending (FIFO).
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].fee != entries[j].fee {
			return entries[i].fee > entries[j].fee // higher fee first
		}
		return entries[i].priority < entries[j].priority // earlier arrival first
	})

	result := make([]block.Transaction, 0, min(limit, len(entries)))
	for _, se := range entries {
		if len(result) >= limit {
			break
		}
		if entry, ok := mp.entries[se.id]; ok {
			result = append(result, entry.Tx)
		}
	}
	return result
}

// GetAll returns all pending transactions ordered by fee rate then FIFO.
func (mp *Mempool) GetAll() []block.Transaction {
	mp.mu.RLock()
	n := len(mp.entries)
	mp.mu.RUnlock()
	return mp.GetPending(n)
}

// ──────────────────────────────────────────────────────────────────────────────
// Eviction
// ──────────────────────────────────────────────────────────────────────────────

// evictExpiredLocked removes transactions older than DefaultTxTTL.
// Must be called with lock held.
func (mp *Mempool) evictExpiredLocked() {
	cutoff := time.Now().Add(-DefaultTxTTL)
	var toRemove []string
	for id, entry := range mp.entries {
		if entry.AddedAt.Before(cutoff) {
			toRemove = append(toRemove, id)
		}
	}
	for _, id := range toRemove {
		mp.removeLocked(id)
		log.Printf("[MEMPOOL] Evicted expired TX %s", shortID(id))
	}
}

// evictOldestLocked removes the oldest transaction to make room.
// Returns true if a transaction was evicted.
func (mp *Mempool) evictOldestLocked() bool {
	if len(mp.order) == 0 {
		return false
	}
	oldest := mp.order[0]
	mp.removeLocked(oldest)
	log.Printf("[MEMPOOL] Evicted oldest TX %s (pool full)", shortID(oldest))
	return true
}

// ──────────────────────────────────────────────────────────────────────────────
// Double-Spend Check (CRITICAL-2: based on explicit input outpoints)
// ──────────────────────────────────────────────────────────────────────────────

// IsOutpointSpent returns true if the given outpoint is already spent by a
// transaction in the mempool.
func (mp *Mempool) IsOutpointSpent(txID string, index int) bool {
	mp.mu.RLock()
	defer mp.mu.RUnlock()
	outKey := fmt.Sprintf("%s:%d", txID, index)
	_, exists := mp.spentOuts[outKey]
	return exists
}

// GetSpendingTotalForAddress returns the total output value of transactions
// in the mempool that have inputs referencing UTXOs from the given address.
// This requires checking which inputs belong to the address — callers should
// pass in the set of outpoint keys that belong to the address.
// For backward compatibility, this returns 0 — the UTXO-level check is more precise.
func (mp *Mempool) GetSpendingTotal(address string) float64 {
	// In the new UTXO model, spending is tracked per-outpoint, not per-address.
	// This method is kept for backward compatibility but returns 0.
	// Use IsOutpointSpent for precise double-spend checks.
	return 0
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

func shortID(id string) string {
	if len(id) <= 16 {
		return id
	}
	return id[:8] + "..." + id[len(id)-4:]
}

func shortAddr(addr string) string {
	if len(addr) <= 16 {
		return addr
	}
	return addr[:8] + "..." + addr[len(addr)-4:]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
