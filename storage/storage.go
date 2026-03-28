// Package storage provides crash-safe persistent storage for the Noda blockchain.
//
// HIGH-1: Replaces the monolithic JSON file (node_data.json) with a structured
// storage layout that separates:
//   - blockstore: individual block files keyed by hash (and height index)
//   - chainstate: serialised UTXO set snapshot + metadata
//   - node metadata: best tip, target, total mined, total faucet, genesis owner
//
// All writes follow the atomic write pattern:
//  1. Write to a temporary file in the same directory
//  2. Sync (fsync) the temporary file
//  3. Rename the temporary file over the target file
//
// On startup the node performs recovery:
//   - If a temp/journal file is found it is removed (incomplete write).
//   - If chainstate is corrupted, UTXO is rebuilt from the blockstore.
//
// Legacy migration:
//   - If the old node_data.json exists and the new storage directory does not,
//     a one-time migration is performed.
package storage

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/Bihan293/Noda/block"
)

// ──────────────────────────────────────────────────────────────────────────────
// Directory layout
// ──────────────────────────────────────────────────────────────────────────────
//
//	<dataDir>/
//	  metadata.json          — node metadata (best tip, target, counters, genesis owner)
//	  blocks/
//	    <height>_<hash>.json — individual block files
//	  chainstate/
//	    utxo.json            — UTXO set snapshot

const (
	blocksSubdir     = "blocks"
	chainstateSubdir = "chainstate"
	metadataFile     = "metadata.json"
	utxoFile         = "utxo.json"
	tmpSuffix        = ".tmp"
	migrationMarker  = ".migrated"
)

// ──────────────────────────────────────────────────────────────────────────────
// NodeMetadata — small JSON with chain tip and counters
// ──────────────────────────────────────────────────────────────────────────────

// NodeMetadata stores lightweight node state that is updated on every block.
type NodeMetadata struct {
	BestTipHash  string  `json:"best_tip_hash"`
	BestHeight   uint64  `json:"best_height"`
	TargetHex    string  `json:"target_hex"`
	TotalMined   float64 `json:"total_mined"`
	TotalFaucet  float64 `json:"total_faucet"`
	GenesisOwner string  `json:"genesis_owner"`
	Version      int     `json:"version"` // storage format version
}

const currentStorageVersion = 1

// ──────────────────────────────────────────────────────────────────────────────
// BlockEntry — per-block file
// ──────────────────────────────────────────────────────────────────────────────

// blockFileName returns the canonical file name for a block: "<height>_<hash>.json".
func blockFileName(height uint64, hash string) string {
	return fmt.Sprintf("%08d_%s.json", height, hash)
}

// parseBlockFileName extracts height and hash from a block file name.
func parseBlockFileName(name string) (uint64, string, bool) {
	// Remove .json extension.
	name = strings.TrimSuffix(name, ".json")
	parts := strings.SplitN(name, "_", 2)
	if len(parts) != 2 {
		return 0, "", false
	}
	height, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return 0, "", false
	}
	return height, parts[1], true
}

// ──────────────────────────────────────────────────────────────────────────────
// Store — main storage engine
// ──────────────────────────────────────────────────────────────────────────────

// Store manages crash-safe persistent storage for blocks, chainstate, and metadata.
type Store struct {
	dataDir       string
	blocksDir     string
	chainstateDir string
	mu            sync.RWMutex

	// In-memory block index for fast lookup by hash and height.
	hashIndex   map[string]uint64 // hash -> height
	heightIndex map[uint64]string // height -> hash (main chain only)
}

// NewStore creates a new Store rooted at dataDir.
// It creates the directory structure if it does not exist and runs recovery.
func NewStore(dataDir string) (*Store, error) {
	blocksDir := filepath.Join(dataDir, blocksSubdir)
	chainstateDir := filepath.Join(dataDir, chainstateSubdir)

	for _, dir := range []string{dataDir, blocksDir, chainstateDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("create directory %s: %w", dir, err)
		}
	}

	s := &Store{
		dataDir:       dataDir,
		blocksDir:     blocksDir,
		chainstateDir: chainstateDir,
		hashIndex:     make(map[string]uint64),
		heightIndex:   make(map[uint64]string),
	}

	// Recovery: remove any leftover temp files.
	s.cleanupTempFiles()

	// Build in-memory block index from blockstore.
	if err := s.rebuildBlockIndex(); err != nil {
		return nil, fmt.Errorf("rebuild block index: %w", err)
	}

	return s, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Atomic Write
// ──────────────────────────────────────────────────────────────────────────────

// atomicWrite writes data to targetPath using the temp+fsync+rename pattern.
func atomicWrite(targetPath string, data []byte) error {
	dir := filepath.Dir(targetPath)
	base := filepath.Base(targetPath)

	tmpPath := filepath.Join(dir, base+tmpSuffix)

	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp file: %w", err)
	}

	// fsync to ensure data reaches disk.
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("sync temp file: %w", err)
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
	}

	// Atomic rename.
	if err := os.Rename(tmpPath, targetPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp to target: %w", err)
	}

	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Recovery
// ──────────────────────────────────────────────────────────────────────────────

// cleanupTempFiles removes leftover .tmp files from an interrupted write.
func (s *Store) cleanupTempFiles() {
	for _, dir := range []string{s.dataDir, s.blocksDir, s.chainstateDir} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), tmpSuffix) {
				path := filepath.Join(dir, e.Name())
				slog.Info("Recovery: removing incomplete temp file", "path", path)
				os.Remove(path)
			}
		}
	}
}

// rebuildBlockIndex scans the blocks directory and populates the in-memory index.
func (s *Store) rebuildBlockIndex() error {
	entries, err := os.ReadDir(s.blocksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		height, hash, ok := parseBlockFileName(e.Name())
		if !ok {
			continue
		}
		s.hashIndex[hash] = height
		// For the height index, keep the entry (will be updated during chain loading).
		// Multiple blocks may share the same height (forks); the main chain block
		// is determined by metadata.
		s.heightIndex[height] = hash
	}

	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Block Store
// ──────────────────────────────────────────────────────────────────────────────

// SaveBlock persists a single block to the blockstore.
func (s *Store) SaveBlock(b *block.Block) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("marshal block %d: %w", b.Header.Height, err)
	}

	path := filepath.Join(s.blocksDir, blockFileName(b.Header.Height, b.Hash))
	if err := atomicWrite(path, data); err != nil {
		return fmt.Errorf("save block %d: %w", b.Header.Height, err)
	}

	s.hashIndex[b.Hash] = b.Header.Height
	s.heightIndex[b.Header.Height] = b.Hash

	return nil
}

// LoadBlock loads a block by its hash from the blockstore.
func (s *Store) LoadBlock(hash string) (*block.Block, error) {
	s.mu.RLock()
	height, ok := s.hashIndex[hash]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("block %s not found in index", shortHash(hash))
	}

	path := filepath.Join(s.blocksDir, blockFileName(height, hash))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read block file: %w", err)
	}

	var b block.Block
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("unmarshal block: %w", err)
	}
	return &b, nil
}

// LoadBlockByHeight loads a block by its height from the blockstore (main chain).
func (s *Store) LoadBlockByHeight(height uint64) (*block.Block, error) {
	s.mu.RLock()
	hash, ok := s.heightIndex[height]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no block at height %d", height)
	}
	return s.LoadBlock(hash)
}

// LoadAllBlocksOrdered reads all blocks from the blockstore, sorted by height.
func (s *Store) LoadAllBlocksOrdered() ([]*block.Block, error) {
	s.mu.RLock()
	heights := make([]uint64, 0, len(s.heightIndex))
	for h := range s.heightIndex {
		heights = append(heights, h)
	}
	s.mu.RUnlock()

	sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })

	blocks := make([]*block.Block, 0, len(heights))
	for _, h := range heights {
		b, err := s.LoadBlockByHeight(h)
		if err != nil {
			return nil, fmt.Errorf("load block at height %d: %w", h, err)
		}
		blocks = append(blocks, b)
	}
	return blocks, nil
}

// HasBlock returns true if the blockstore has a block with the given hash.
func (s *Store) HasBlock(hash string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.hashIndex[hash]
	return ok
}

// BlockCount returns the number of blocks in the blockstore.
func (s *Store) BlockCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.hashIndex)
}

// BestStoredHeight returns the highest block height currently stored.
// Returns 0 if no blocks are stored.
func (s *Store) BestStoredHeight() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best uint64
	for h := range s.heightIndex {
		if h > best {
			best = h
		}
	}
	return best
}

// RemoveBlocksAbove removes blocks from the store that are above the given height.
// Used during reorgs to clean up stale blocks from a disconnected branch.
func (s *Store) RemoveBlocksAbove(height uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var toRemove []uint64
	for h := range s.heightIndex {
		if h > height {
			toRemove = append(toRemove, h)
		}
	}

	for _, h := range toRemove {
		hash := s.heightIndex[h]
		path := filepath.Join(s.blocksDir, blockFileName(h, hash))
		os.Remove(path) // best-effort
		delete(s.heightIndex, h)
		delete(s.hashIndex, hash)
	}

	return nil
}

// UpdateHeightIndex updates the main-chain height→hash mapping for reorgs.
func (s *Store) UpdateHeightIndex(height uint64, hash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heightIndex[height] = hash
}

// ──────────────────────────────────────────────────────────────────────────────
// Metadata
// ──────────────────────────────────────────────────────────────────────────────

// SaveMetadata writes node metadata atomically.
func (s *Store) SaveMetadata(meta *NodeMetadata) error {
	meta.Version = currentStorageVersion
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	return atomicWrite(filepath.Join(s.dataDir, metadataFile), data)
}

// LoadMetadata reads node metadata. Returns nil if the file does not exist.
func (s *Store) LoadMetadata() (*NodeMetadata, error) {
	path := filepath.Join(s.dataDir, metadataFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read metadata: %w", err)
	}

	var meta NodeMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("unmarshal metadata: %w", err)
	}
	return &meta, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Chainstate (UTXO snapshot)
// ──────────────────────────────────────────────────────────────────────────────

// UTXOEntry represents a single UTXO for serialization.
type UTXOEntry struct {
	TxID    string  `json:"tx_id"`
	Index   int     `json:"index"`
	Address string  `json:"address"`
	Amount  float64 `json:"amount"`
}

// ChainstateSnapshot is the serializable UTXO set.
type ChainstateSnapshot struct {
	Height uint64      `json:"height"`   // block height at which this snapshot was taken
	Hash   string      `json:"hash"`     // best tip hash at snapshot time
	UTXOs  []UTXOEntry `json:"utxos"`
}

// SaveChainstate writes the UTXO set snapshot atomically.
func (s *Store) SaveChainstate(snap *ChainstateSnapshot) error {
	data, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshal chainstate: %w", err)
	}
	return atomicWrite(filepath.Join(s.chainstateDir, utxoFile), data)
}

// LoadChainstate reads the UTXO set snapshot. Returns nil if the file does not exist.
func (s *Store) LoadChainstate() (*ChainstateSnapshot, error) {
	path := filepath.Join(s.chainstateDir, utxoFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read chainstate: %w", err)
	}

	var snap ChainstateSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("unmarshal chainstate: %w", err)
	}
	return &snap, nil
}

// ChainstateExists returns true if a chainstate snapshot exists.
func (s *Store) ChainstateExists() bool {
	path := filepath.Join(s.chainstateDir, utxoFile)
	_, err := os.Stat(path)
	return err == nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Legacy Migration
// ──────────────────────────────────────────────────────────────────────────────

// LegacyData represents the old monolithic node_data.json format.
type LegacyData struct {
	Chain *LegacyChain `json:"chain"`
}

// LegacyChain is the chain structure from the old JSON format.
type LegacyChain struct {
	Blocks       []*block.Block `json:"blocks"`
	TotalMined   float64        `json:"total_mined"`
	TotalFaucet  float64        `json:"total_faucet"`
	GenesisOwner string         `json:"genesis_owner"`
	TargetHex    string         `json:"target_hex"`
}

// MigrateFromLegacy performs a one-time migration from the old monolithic
// node_data.json to the new storage layout.
// It returns true if a migration was performed, false if not needed.
func (s *Store) MigrateFromLegacy(legacyPath string) (bool, error) {
	// Check if migration marker already exists.
	markerPath := filepath.Join(s.dataDir, migrationMarker)
	if _, err := os.Stat(markerPath); err == nil {
		return false, nil // already migrated
	}

	// Check if there's already data in the new store.
	if s.BlockCount() > 0 {
		// Already have blocks — no migration needed; create marker.
		_ = atomicWrite(markerPath, []byte("migrated"))
		return false, nil
	}

	// Check if legacy file exists.
	data, err := os.ReadFile(legacyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // no legacy data
		}
		return false, fmt.Errorf("read legacy file: %w", err)
	}

	slog.Info("Legacy migration: reading old node_data.json", "path", legacyPath)

	var legacy LegacyData
	if err := json.Unmarshal(data, &legacy); err != nil {
		return false, fmt.Errorf("unmarshal legacy data: %w", err)
	}

	if legacy.Chain == nil || len(legacy.Chain.Blocks) == 0 {
		slog.Warn("Legacy file has no chain data, skipping migration")
		return false, nil
	}

	// Write all blocks to the new blockstore.
	for _, b := range legacy.Chain.Blocks {
		if err := s.SaveBlock(b); err != nil {
			return false, fmt.Errorf("save block %d during migration: %w", b.Header.Height, err)
		}
	}

	// Write metadata.
	lastBlock := legacy.Chain.Blocks[len(legacy.Chain.Blocks)-1]
	targetHex := legacy.Chain.TargetHex
	if targetHex == "" {
		targetHex = block.BitsFromTarget(block.InitialTarget)
	}

	meta := &NodeMetadata{
		BestTipHash:  lastBlock.Hash,
		BestHeight:   lastBlock.Header.Height,
		TargetHex:    targetHex,
		TotalMined:   legacy.Chain.TotalMined,
		TotalFaucet:  legacy.Chain.TotalFaucet,
		GenesisOwner: legacy.Chain.GenesisOwner,
	}
	if err := s.SaveMetadata(meta); err != nil {
		return false, fmt.Errorf("save metadata during migration: %w", err)
	}

	// Write migration marker.
	_ = atomicWrite(markerPath, []byte("migrated"))

	slog.Info("Legacy migration completed",
		"blocks", len(legacy.Chain.Blocks),
		"best_height", lastBlock.Header.Height,
		"genesis_owner", legacy.Chain.GenesisOwner,
	)

	return true, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

// DataDir returns the root data directory path.
func (s *Store) DataDir() string {
	return s.dataDir
}

// MetadataPath returns the full path to the metadata file.
func (s *Store) MetadataPath() string {
	return filepath.Join(s.dataDir, metadataFile)
}

// TargetFromMeta parses the target hex from metadata.
func TargetFromMeta(meta *NodeMetadata) *big.Int {
	if meta == nil || meta.TargetHex == "" {
		return new(big.Int).Set(block.InitialTarget)
	}
	return block.TargetFromBits(meta.TargetHex)
}

func shortHash(h string) string {
	if len(h) <= 16 {
		return h
	}
	return h[:16] + "..."
}
