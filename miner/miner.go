// Package miner implements a background mining worker that assembles block
// templates from the mempool and mines them using Proof of Work.
//
// CRITICAL-3: Mining is decoupled from transaction submission. Transactions
// are submitted to the mempool and mined asynchronously by this worker.
//
// The miner:
//   - Periodically assembles a block template from the mempool
//   - Includes a coinbase transaction that collects block reward + fees
//   - Mines the block (PoW)
//   - Applies the block to the chain and UTXO set
//   - Removes confirmed transactions from the mempool
//
// Configuration:
//   - MINING_ENABLED: enable/disable mining (default: true if MINER_ADDRESS set)
//   - MINER_ADDRESS: address to receive mining rewards
//   - BLOCK_MAX_TX: maximum transactions per block (default: 100)
//   - MINING_INTERVAL_MS: interval between mining attempts in ms (default: 5000)
//   - MINING_MAX_ATTEMPTS: maximum PoW nonce attempts (default: 10000000)
package miner

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Bihan293/Noda/block"
	"github.com/Bihan293/Noda/chain"
	"github.com/Bihan293/Noda/mempool"
	"github.com/Bihan293/Noda/utxo"
)

// ──────────────────────────────────────────────────────────────────────────────
// Constants
// ──────────────────────────────────────────────────────────────────────────────

const (
	// DefaultBlockMaxTx is the default maximum number of transactions per block
	// (excluding the coinbase transaction).
	DefaultBlockMaxTx = 100

	// DefaultMiningInterval is the default interval between mining attempts.
	DefaultMiningInterval = 5 * time.Second

	// DefaultMaxAttempts is the default maximum PoW nonce attempts per block.
	DefaultMaxAttempts = 10_000_000
)

// ──────────────────────────────────────────────────────────────────────────────
// Config
// ──────────────────────────────────────────────────────────────────────────────

// Config holds the miner configuration.
type Config struct {
	Enabled       bool          // whether mining is enabled
	MinerAddress  string        // address to receive mining rewards
	BlockMaxTx    int           // max transactions per block (excluding coinbase)
	Interval      time.Duration // interval between mining attempts
	MaxAttempts   uint64        // max PoW nonce attempts per block
}

// DefaultConfig returns a Config with sensible defaults.
// Mining is disabled by default — callers must set Enabled and MinerAddress.
func DefaultConfig() Config {
	return Config{
		Enabled:      false,
		MinerAddress: "",
		BlockMaxTx:   DefaultBlockMaxTx,
		Interval:     DefaultMiningInterval,
		MaxAttempts:  DefaultMaxAttempts,
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Ledger interface — to avoid circular imports
// ──────────────────────────────────────────────────────────────────────────────

// ChainAccess provides the methods the miner needs from the ledger/chain.
type ChainAccess interface {
	// GetChain returns the blockchain.
	GetChain() *chain.Blockchain
	// GetChainHeight returns the current chain height.
	GetChainHeight() uint64
	// GetBlockReward returns the reward for the next block.
	GetBlockReward() float64
	// UTXOSetRef returns a pointer to the UTXO set.
	UTXOSetRef() *utxo.Set
	// MempoolRef returns a pointer to the mempool.
	MempoolRef() *mempool.Mempool
	// ApplyMinedBlock applies a mined block to chain + UTXO, removes mempool txs, persists.
	ApplyMinedBlock(b *block.Block, txIDs []string) error
}

// ──────────────────────────────────────────────────────────────────────────────
// Miner
// ──────────────────────────────────────────────────────────────────────────────

// Miner is a background mining worker.
type Miner struct {
	config  Config
	ledger  ChainAccess
	mu      sync.RWMutex

	// Stats
	lastMinedHash string
	blocksMined   uint64
}

// New creates a new Miner with the given config and ledger access.
func New(cfg Config, ledger ChainAccess) *Miner {
	return &Miner{
		config: cfg,
		ledger: ledger,
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Run — Background Mining Loop
// ──────────────────────────────────────────────────────────────────────────────

// Run starts the mining loop. It blocks until the context is cancelled.
// Call this in a goroutine.
func (m *Miner) Run(ctx context.Context) {
	if !m.config.Enabled {
		slog.Info("Miner disabled — set MINING_ENABLED=true and MINER_ADDRESS to enable")
		return
	}
	if m.config.MinerAddress == "" {
		slog.Warn("Miner enabled but MINER_ADDRESS is empty — mining disabled")
		return
	}

	slog.Info("Miner started",
		"address", shortAddr(m.config.MinerAddress),
		"block_max_tx", m.config.BlockMaxTx,
		"interval", m.config.Interval,
	)

	ticker := time.NewTicker(m.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Miner shutting down")
			return
		case <-ticker.C:
			m.tryMineBlock()
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Block Template Assembly & Mining
// ──────────────────────────────────────────────────────────────────────────────

// tryMineBlock attempts to assemble and mine a single block from the mempool.
func (m *Miner) tryMineBlock() {
	mp := m.ledger.MempoolRef()
	if mp.Size() == 0 {
		return // nothing to mine
	}

	// Get pending transactions sorted by fee (the mempool handles ordering).
	txs := mp.GetPending(m.config.BlockMaxTx)
	if len(txs) == 0 {
		return
	}

	// Validate each tx against current UTXO state and compute fees.
	utxoSet := m.ledger.UTXOSetRef()
	var validTxs []block.Transaction
	var totalFees float64
	var confirmedIDs []string

	// Track outpoints consumed in this template to avoid double-spend within one block.
	spentInTemplate := make(map[string]bool)

	for _, tx := range txs {
		// Skip if any input was already spent in this template.
		conflict := false
		for _, in_ := range tx.Inputs {
			outKey := fmt.Sprintf("%s:%d", in_.PrevTxID, in_.PrevIndex)
			if spentInTemplate[outKey] {
				conflict = true
				break
			}
		}
		if conflict {
			continue
		}

		// Verify that all inputs still exist in the UTXO set.
		var inputSum float64
		valid := true
		for _, in_ := range tx.Inputs {
			op := utxo.OutPoint{TxID: in_.PrevTxID, Index: in_.PrevIndex}
			utxoOut := utxoSet.Get(op)
			if utxoOut == nil {
				valid = false
				break
			}
			inputSum += utxoOut.Amount
		}
		if !valid {
			// Input is no longer available — skip this tx.
			// It will be cleaned up later or the sender can re-submit.
			continue
		}

		outputSum := tx.TotalOutputValue()
		fee := inputSum - outputSum
		if fee < 0 {
			// Negative fee — invalid, skip.
			continue
		}

		// Mark outpoints as spent in this template.
		for _, in_ := range tx.Inputs {
			outKey := fmt.Sprintf("%s:%d", in_.PrevTxID, in_.PrevIndex)
			spentInTemplate[outKey] = true
		}

		validTxs = append(validTxs, tx)
		totalFees += fee
		confirmedIDs = append(confirmedIDs, tx.ID)
	}

	if len(validTxs) == 0 {
		return
	}

	// Build the block.
	ch := m.ledger.GetChain()
	nextHeight := ch.Height() + 1
	prevHash := ch.LastHash()
	target := ch.GetTarget()

	// Block reward + fees for coinbase.
	reward := m.ledger.GetBlockReward()
	coinbaseAmount := reward + totalFees

	// Create coinbase transaction.
	coinbaseTx := block.NewCoinbaseTx(m.config.MinerAddress, coinbaseAmount, nextHeight)

	// Assemble all transactions: coinbase first, then user transactions.
	allTxs := make([]block.Transaction, 0, 1+len(validTxs))
	allTxs = append(allTxs, coinbaseTx)
	allTxs = append(allTxs, validTxs...)

	// Compute Merkle root.
	txIDs := make([]string, len(allTxs))
	for i, tx := range allTxs {
		txIDs[i] = tx.ID
	}
	merkleRoot := block.ComputeMerkleRoot(txIDs)

	// Build block.
	newBlock := &block.Block{
		Header: block.BlockHeader{
			Version:       block.BlockVersion,
			Height:        nextHeight,
			PrevBlockHash: prevHash,
			MerkleRoot:    merkleRoot,
			Timestamp:     time.Now().Unix(),
			Bits:          block.BitsFromTarget(target),
		},
		Transactions: allTxs,
	}

	// Mine the block (PoW).
	slog.Debug("Mining block",
		"height", nextHeight,
		"txs", len(validTxs),
		"fees", totalFees,
		"reward", reward,
	)

	if err := block.MineBlock(newBlock, target, m.config.MaxAttempts); err != nil {
		slog.Warn("Mining failed", "height", nextHeight, "error", err)
		return
	}

	// Apply the mined block.
	if err := m.ledger.ApplyMinedBlock(newBlock, confirmedIDs); err != nil {
		slog.Error("Failed to apply mined block", "height", nextHeight, "error", err)
		return
	}

	m.mu.Lock()
	m.lastMinedHash = newBlock.Hash
	m.blocksMined++
	m.mu.Unlock()

	slog.Info("Block mined",
		"height", newBlock.Header.Height,
		"hash", newBlock.Hash[:16],
		"txs", len(validTxs),
		"fees", totalFees,
		"reward", coinbaseAmount,
		"miner", shortAddr(m.config.MinerAddress),
	)
}

// ──────────────────────────────────────────────────────────────────────────────
// Accessors
// ──────────────────────────────────────────────────────────────────────────────

// IsEnabled returns whether mining is enabled.
func (m *Miner) IsEnabled() bool {
	return m.config.Enabled && m.config.MinerAddress != ""
}

// MinerAddress returns the configured miner address.
func (m *Miner) MinerAddress() string {
	return m.config.MinerAddress
}

// LastMinedHash returns the hash of the last block mined by this node.
func (m *Miner) LastMinedHash() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastMinedHash
}

// BlocksMined returns the total number of blocks mined by this node.
func (m *Miner) BlocksMined() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.blocksMined
}

// Config returns the miner configuration.
func (m *Miner) Config() Config {
	return m.config
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
