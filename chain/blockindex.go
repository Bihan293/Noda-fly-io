// Package chain — block index, cumulative work selection, orphan pool, and reorg.
//
// CRITICAL-4: Best chain is selected by cumulative work, not by length.
// The block index stores metadata for every known block (main, side, orphan).
// The reorg pipeline can rollback UTXO and re-apply a different branch.
package chain

import (
	"fmt"
	"log/slog"
	"math/big"
	"sync"

	"github.com/Bihan293/Noda/block"
)

// ──────────────────────────────────────────────────────────────────────────────
// Block Status
// ──────────────────────────────────────────────────────────────────────────────

// BlockStatus indicates the position of a block in the chain.
type BlockStatus int

const (
	StatusMain   BlockStatus = iota // part of the main (best) chain
	StatusSide                      // valid block on a side branch
	StatusOrphan                    // parent unknown
)

func (s BlockStatus) String() string {
	switch s {
	case StatusMain:
		return "main"
	case StatusSide:
		return "side"
	case StatusOrphan:
		return "orphan"
	default:
		return "unknown"
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// BlockNode — per-block metadata
// ──────────────────────────────────────────────────────────────────────────────

// BlockNode holds metadata for a single block in the index.
type BlockNode struct {
	Hash           string      `json:"hash"`
	ParentHash     string      `json:"parent_hash"`
	Height         uint64      `json:"height"`
	Work           *big.Int    `json:"work"`            // work for this block alone
	CumulativeWork *big.Int    `json:"cumulative_work"` // sum of work from genesis to here
	Status         BlockStatus `json:"status"`
	Block          *block.Block `json:"-"` // the full block (may be nil for header-only nodes)
}

// ──────────────────────────────────────────────────────────────────────────────
// BlockIndex
// ──────────────────────────────────────────────────────────────────────────────

// MaxOrphanBlocks limits the orphan pool size to prevent memory exhaustion.
const MaxOrphanBlocks = 200

// BlockIndex stores metadata for all known blocks and selects the best chain.
type BlockIndex struct {
	nodes      map[string]*BlockNode   // hash -> node
	byHeight   map[uint64][]string     // height -> list of block hashes at that height
	orphans    map[string]*block.Block // hash -> orphan block (parent unknown)
	orphanByParent map[string][]string // parent hash -> orphan hashes waiting for this parent
	bestTip    *BlockNode              // the tip of the best (most work) chain
	mu         sync.RWMutex
}

// NewBlockIndex creates an empty block index.
func NewBlockIndex() *BlockIndex {
	return &BlockIndex{
		nodes:          make(map[string]*BlockNode),
		byHeight:       make(map[uint64][]string),
		orphans:        make(map[string]*block.Block),
		orphanByParent: make(map[string][]string),
	}
}

// BuildFromBlocks initializes the block index from an ordered slice of main-chain blocks.
func (idx *BlockIndex) BuildFromBlocks(blocks []*block.Block) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	var prevNode *BlockNode
	for _, b := range blocks {
		work := block.WorkForBits(b.Header.Bits)
		cumulativeWork := new(big.Int).Set(work)
		if prevNode != nil {
			cumulativeWork.Add(cumulativeWork, prevNode.CumulativeWork)
		}

		node := &BlockNode{
			Hash:           b.Hash,
			ParentHash:     b.Header.PrevBlockHash,
			Height:         b.Header.Height,
			Work:           work,
			CumulativeWork: cumulativeWork,
			Status:         StatusMain,
			Block:          b,
		}
		idx.nodes[b.Hash] = node
		idx.byHeight[b.Header.Height] = append(idx.byHeight[b.Header.Height], b.Hash)
		prevNode = node
	}

	if prevNode != nil {
		idx.bestTip = prevNode
	}
}

// BestTip returns the tip of the best chain. Returns nil if index is empty.
func (idx *BlockIndex) BestTip() *BlockNode {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.bestTip
}

// BestHeight returns the height of the best chain tip.
func (idx *BlockIndex) BestHeight() uint64 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if idx.bestTip == nil {
		return 0
	}
	return idx.bestTip.Height
}

// BestCumulativeWork returns the cumulative work of the best chain tip.
func (idx *BlockIndex) BestCumulativeWork() *big.Int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if idx.bestTip == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(idx.bestTip.CumulativeWork)
}

// GetNode returns the block node for a hash, or nil.
func (idx *BlockIndex) GetNode(hash string) *BlockNode {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.nodes[hash]
}

// HasBlock returns true if the hash is in the index (excluding orphans).
func (idx *BlockIndex) HasBlock(hash string) bool {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	_, ok := idx.nodes[hash]
	return ok
}

// HasOrphan returns true if the hash is in the orphan pool.
func (idx *BlockIndex) HasOrphan(hash string) bool {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	_, ok := idx.orphans[hash]
	return ok
}

// OrphanCount returns the number of orphan blocks.
func (idx *BlockIndex) OrphanCount() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.orphans)
}

// ──────────────────────────────────────────────────────────────────────────────
// AddBlock — process a new block through the index
// ──────────────────────────────────────────────────────────────────────────────

// AddBlockResult describes what happened when a block was processed.
type AddBlockResult struct {
	// Added is true if the block was accepted (not a duplicate).
	Added bool
	// IsOrphan is true if the block was added to the orphan pool.
	IsOrphan bool
	// ReorgOccurred is true if the best chain tip changed to a new branch.
	ReorgOccurred bool
	// OldTip is the previous best tip hash (only set if ReorgOccurred).
	OldTip string
	// NewTip is the new best tip hash (only set if ReorgOccurred).
	NewTip string
	// ConnectedOrphans contains orphan blocks that were connected after this block.
	ConnectedOrphans []*block.Block
}

// AddBlock inserts a validated block into the index and determines if it
// extends the best chain, creates a side chain, or is an orphan.
// The caller MUST have already validated the block's PoW and Merkle root.
// Returns the result and any orphan blocks that should now be connected.
func (idx *BlockIndex) AddBlock(b *block.Block) AddBlockResult {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Check for duplicate.
	if _, exists := idx.nodes[b.Hash]; exists {
		return AddBlockResult{Added: false}
	}

	// Check for duplicate orphan.
	if _, exists := idx.orphans[b.Hash]; exists {
		return AddBlockResult{Added: false}
	}

	// Check if parent is known.
	parentNode, parentKnown := idx.nodes[b.Header.PrevBlockHash]
	if !parentKnown && b.Header.Height != 0 {
		// Parent unknown — orphan this block.
		return idx.addOrphanLocked(b)
	}

	// Parent known (or genesis block): create the node.
	work := block.WorkForBits(b.Header.Bits)
	cumulativeWork := new(big.Int).Set(work)
	if parentNode != nil {
		cumulativeWork.Add(cumulativeWork, parentNode.CumulativeWork)
	}

	node := &BlockNode{
		Hash:           b.Hash,
		ParentHash:     b.Header.PrevBlockHash,
		Height:         b.Header.Height,
		Work:           work,
		CumulativeWork: cumulativeWork,
		Status:         StatusSide, // initially side, promoted to main if best
		Block:          b,
	}

	idx.nodes[b.Hash] = node
	idx.byHeight[b.Header.Height] = append(idx.byHeight[b.Header.Height], b.Hash)

	result := AddBlockResult{Added: true}

	// Determine if this is the new best tip.
	if idx.bestTip == nil || cumulativeWork.Cmp(idx.bestTip.CumulativeWork) > 0 {
		oldTip := ""
		if idx.bestTip != nil {
			oldTip = idx.bestTip.Hash
		}

		// Check if this is a reorg (new tip is not a direct child of old tip).
		if idx.bestTip != nil && b.Header.PrevBlockHash != idx.bestTip.Hash {
			result.ReorgOccurred = true
			result.OldTip = oldTip
			result.NewTip = b.Hash
			slog.Info("Chain reorg detected",
				"old_tip", shortHash(oldTip),
				"new_tip", shortHash(b.Hash),
				"old_work", idx.bestTip.CumulativeWork.String(),
				"new_work", cumulativeWork.String(),
			)
		}

		idx.bestTip = node
		node.Status = StatusMain
	}

	// Check if any orphans can now be connected.
	// Connected orphans are added to the index immediately.
	connectedOrphans := idx.processOrphansLocked(b.Hash)
	result.ConnectedOrphans = connectedOrphans

	return result
}

// processOrphansLocked connects orphans whose parent just became known,
// adds them to the index as proper nodes, and recursively processes their children.
// Must hold lock.
func (idx *BlockIndex) processOrphansLocked(parentHash string) []*block.Block {
	waitingHashes, ok := idx.orphanByParent[parentHash]
	if !ok || len(waitingHashes) == 0 {
		return nil
	}

	var connected []*block.Block
	// Copy the slice since we'll modify the map.
	hashes := make([]string, len(waitingHashes))
	copy(hashes, waitingHashes)
	delete(idx.orphanByParent, parentHash)

	for _, orphanHash := range hashes {
		orphanBlock, exists := idx.orphans[orphanHash]
		if !exists {
			continue
		}
		delete(idx.orphans, orphanHash)

		// Now add the orphan as a proper node.
		parentNode, parentKnown := idx.nodes[orphanBlock.Header.PrevBlockHash]
		if !parentKnown {
			// Still no parent — shouldn't happen, but re-orphan.
			idx.orphans[orphanHash] = orphanBlock
			idx.orphanByParent[orphanBlock.Header.PrevBlockHash] = append(
				idx.orphanByParent[orphanBlock.Header.PrevBlockHash], orphanHash)
			continue
		}

		work := block.WorkForBits(orphanBlock.Header.Bits)
		cumulativeWork := new(big.Int).Set(work)
		cumulativeWork.Add(cumulativeWork, parentNode.CumulativeWork)

		node := &BlockNode{
			Hash:           orphanBlock.Hash,
			ParentHash:     orphanBlock.Header.PrevBlockHash,
			Height:         orphanBlock.Header.Height,
			Work:           work,
			CumulativeWork: cumulativeWork,
			Status:         StatusSide,
			Block:          orphanBlock,
		}

		idx.nodes[orphanBlock.Hash] = node
		idx.byHeight[orphanBlock.Header.Height] = append(
			idx.byHeight[orphanBlock.Header.Height], orphanBlock.Hash)

		// Check if this becomes the new best tip.
		if idx.bestTip == nil || cumulativeWork.Cmp(idx.bestTip.CumulativeWork) > 0 {
			idx.bestTip = node
			node.Status = StatusMain
		}

		connected = append(connected, orphanBlock)

		// Recursively process any orphans waiting for this block.
		deeper := idx.processOrphansLocked(orphanBlock.Hash)
		connected = append(connected, deeper...)
	}

	return connected
}

// addOrphanLocked adds a block to the orphan pool. Must hold lock.
func (idx *BlockIndex) addOrphanLocked(b *block.Block) AddBlockResult {
	// Evict oldest orphans if pool is full.
	for len(idx.orphans) >= MaxOrphanBlocks {
		// Remove a random orphan (first found in map iteration).
		for hash, orphan := range idx.orphans {
			parentHash := orphan.Header.PrevBlockHash
			delete(idx.orphans, hash)
			// Clean up parent index.
			if hashes, ok := idx.orphanByParent[parentHash]; ok {
				for i, h := range hashes {
					if h == hash {
						idx.orphanByParent[parentHash] = append(hashes[:i], hashes[i+1:]...)
						break
					}
				}
				if len(idx.orphanByParent[parentHash]) == 0 {
					delete(idx.orphanByParent, parentHash)
				}
			}
			break
		}
	}

	idx.orphans[b.Hash] = b
	parentHash := b.Header.PrevBlockHash
	idx.orphanByParent[parentHash] = append(idx.orphanByParent[parentHash], b.Hash)

	slog.Debug("Block added to orphan pool",
		"hash", shortHash(b.Hash),
		"height", b.Header.Height,
		"parent", shortHash(parentHash),
		"orphan_count", len(idx.orphans),
	)

	return AddBlockResult{Added: true, IsOrphan: true}
}

// ──────────────────────────────────────────────────────────────────────────────
// Fork Point & Chain Path
// ──────────────────────────────────────────────────────────────────────────────

// FindForkPoint finds the common ancestor between two chain tips.
// Returns the fork point node, the blocks to disconnect (from oldTip to fork),
// and the blocks to connect (from fork to newTip), both in order.
func (idx *BlockIndex) FindForkPoint(oldTipHash, newTipHash string) (
	forkPoint *BlockNode,
	disconnect []*BlockNode,
	connect []*BlockNode,
	err error,
) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	oldNode := idx.nodes[oldTipHash]
	newNode := idx.nodes[newTipHash]

	if oldNode == nil {
		return nil, nil, nil, fmt.Errorf("old tip %s not found in index", shortHash(oldTipHash))
	}
	if newNode == nil {
		return nil, nil, nil, fmt.Errorf("new tip %s not found in index", shortHash(newTipHash))
	}

	// Walk both chains back to the same height.
	oldWalk := oldNode
	newWalk := newNode

	// Bring both to the same height.
	for oldWalk.Height > newWalk.Height {
		disconnect = append(disconnect, oldWalk)
		parent, ok := idx.nodes[oldWalk.ParentHash]
		if !ok {
			return nil, nil, nil, fmt.Errorf("broken chain at %s", shortHash(oldWalk.Hash))
		}
		oldWalk = parent
	}
	for newWalk.Height > oldWalk.Height {
		connect = append([]*BlockNode{newWalk}, connect...)
		parent, ok := idx.nodes[newWalk.ParentHash]
		if !ok {
			return nil, nil, nil, fmt.Errorf("broken chain at %s", shortHash(newWalk.Hash))
		}
		newWalk = parent
	}

	// Walk both back until they meet.
	for oldWalk.Hash != newWalk.Hash {
		disconnect = append(disconnect, oldWalk)
		connect = append([]*BlockNode{newWalk}, connect...)

		oldParent, ok1 := idx.nodes[oldWalk.ParentHash]
		newParent, ok2 := idx.nodes[newWalk.ParentHash]
		if !ok1 || !ok2 {
			return nil, nil, nil, fmt.Errorf("broken chain while finding fork point")
		}
		oldWalk = oldParent
		newWalk = newParent
	}

	return oldWalk, disconnect, connect, nil
}

// GetMainChainBlocks returns all blocks on the main chain from genesis to tip, in order.
func (idx *BlockIndex) GetMainChainBlocks() []*block.Block {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if idx.bestTip == nil {
		return nil
	}

	// Walk backwards from tip to genesis.
	var nodes []*BlockNode
	current := idx.bestTip
	for current != nil {
		nodes = append(nodes, current)
		parent, ok := idx.nodes[current.ParentHash]
		if !ok {
			break // reached genesis or broken chain
		}
		current = parent
	}

	// Reverse to get genesis-first order.
	blocks := make([]*block.Block, 0, len(nodes))
	for i := len(nodes) - 1; i >= 0; i-- {
		if nodes[i].Block != nil {
			blocks = append(blocks, nodes[i].Block)
		}
	}
	return blocks
}

// UpdateMainChainStatus updates the Status field for all blocks after a reorg.
// Blocks on the new best chain are marked StatusMain; others are StatusSide.
func (idx *BlockIndex) UpdateMainChainStatus() {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Mark everything as side first.
	for _, node := range idx.nodes {
		node.Status = StatusSide
	}

	// Walk back from best tip and mark as main.
	current := idx.bestTip
	for current != nil {
		current.Status = StatusMain
		parent, ok := idx.nodes[current.ParentHash]
		if !ok {
			break
		}
		current = parent
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Headers-First Sync Support (CRITICAL-4)
// ──────────────────────────────────────────────────────────────────────────────

// GetBlockHashesAfter returns up to `limit` block hashes on the main chain
// after the given hash. Used for headers-first sync.
func (idx *BlockIndex) GetBlockHashesAfter(afterHash string, limit int) []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	startNode, ok := idx.nodes[afterHash]
	if !ok {
		return nil
	}

	// Collect main chain blocks.
	mainBlocks := idx.getMainChainBlocksLocked()
	var result []string
	found := false
	for _, b := range mainBlocks {
		if found {
			result = append(result, b.Hash)
			if len(result) >= limit {
				break
			}
		}
		if b.Hash == afterHash || b.Header.Height == startNode.Height {
			if b.Hash == afterHash {
				found = true
			}
		}
	}
	return result
}

// getMainChainBlocksLocked returns main chain blocks in order. Must hold read lock.
func (idx *BlockIndex) getMainChainBlocksLocked() []*block.Block {
	if idx.bestTip == nil {
		return nil
	}

	var nodes []*BlockNode
	current := idx.bestTip
	for current != nil {
		nodes = append(nodes, current)
		parent, ok := idx.nodes[current.ParentHash]
		if !ok {
			break
		}
		current = parent
	}

	blocks := make([]*block.Block, 0, len(nodes))
	for i := len(nodes) - 1; i >= 0; i-- {
		if nodes[i].Block != nil {
			blocks = append(blocks, nodes[i].Block)
		}
	}
	return blocks
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

func shortHash(hash string) string {
	if len(hash) <= 16 {
		return hash
	}
	return hash[:16] + "..."
}
