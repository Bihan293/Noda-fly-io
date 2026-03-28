package storage

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/Bihan293/Noda/block"
)

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

func tmpStore(t *testing.T) *Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "store")
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func easyTarget() *big.Int {
	t := new(big.Int)
	t.SetString("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", 16)
	return t
}

func mineTestBlock(prev *block.Block, height uint64) *block.Block {
	tx := block.NewCoinbaseTx("miner", 50, height)
	merkle := block.ComputeMerkleRoot([]string{tx.ID})
	b := &block.Block{
		Header: block.BlockHeader{
			Version:       block.BlockVersion,
			Height:        height,
			PrevBlockHash: prev.Hash,
			MerkleRoot:    merkle,
			Timestamp:     prev.Header.Timestamp + 600,
		},
		Transactions: []block.Transaction{tx},
	}
	block.MineBlock(b, easyTarget(), 10000)
	return b
}

// ──────────────────────────────────────────────────────────────────────────────
// Block Store Tests
// ──────────────────────────────────────────────────────────────────────────────

func TestSaveAndLoadBlock(t *testing.T) {
	s := tmpStore(t)
	genesis := block.NewGenesisBlock()

	if err := s.SaveBlock(genesis); err != nil {
		t.Fatalf("SaveBlock: %v", err)
	}

	loaded, err := s.LoadBlock(genesis.Hash)
	if err != nil {
		t.Fatalf("LoadBlock: %v", err)
	}

	if loaded.Hash != genesis.Hash {
		t.Errorf("loaded hash = %s, want %s", loaded.Hash, genesis.Hash)
	}
	if loaded.Header.Height != genesis.Header.Height {
		t.Errorf("loaded height = %d, want %d", loaded.Header.Height, genesis.Header.Height)
	}
}

func TestLoadBlockByHeight(t *testing.T) {
	s := tmpStore(t)
	genesis := block.NewGenesisBlock()
	s.SaveBlock(genesis)

	b1 := mineTestBlock(genesis, 1)
	s.SaveBlock(b1)

	loaded, err := s.LoadBlockByHeight(1)
	if err != nil {
		t.Fatalf("LoadBlockByHeight: %v", err)
	}
	if loaded.Hash != b1.Hash {
		t.Errorf("loaded hash = %s, want %s", loaded.Hash, b1.Hash)
	}
}

func TestLoadAllBlocksOrdered(t *testing.T) {
	s := tmpStore(t)
	genesis := block.NewGenesisBlock()
	s.SaveBlock(genesis)

	b1 := mineTestBlock(genesis, 1)
	s.SaveBlock(b1)

	b2 := mineTestBlock(b1, 2)
	s.SaveBlock(b2)

	blocks, err := s.LoadAllBlocksOrdered()
	if err != nil {
		t.Fatalf("LoadAllBlocksOrdered: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("len = %d, want 3", len(blocks))
	}
	for i, b := range blocks {
		if b.Header.Height != uint64(i) {
			t.Errorf("block[%d] height = %d, want %d", i, b.Header.Height, i)
		}
	}
}

func TestHasBlock(t *testing.T) {
	s := tmpStore(t)
	genesis := block.NewGenesisBlock()

	if s.HasBlock(genesis.Hash) {
		t.Error("HasBlock should be false before save")
	}

	s.SaveBlock(genesis)

	if !s.HasBlock(genesis.Hash) {
		t.Error("HasBlock should be true after save")
	}
}

func TestBlockCount(t *testing.T) {
	s := tmpStore(t)
	if s.BlockCount() != 0 {
		t.Errorf("BlockCount = %d, want 0", s.BlockCount())
	}

	genesis := block.NewGenesisBlock()
	s.SaveBlock(genesis)

	if s.BlockCount() != 1 {
		t.Errorf("BlockCount = %d, want 1", s.BlockCount())
	}
}

func TestRemoveBlocksAbove(t *testing.T) {
	s := tmpStore(t)
	genesis := block.NewGenesisBlock()
	s.SaveBlock(genesis)

	b1 := mineTestBlock(genesis, 1)
	s.SaveBlock(b1)

	b2 := mineTestBlock(b1, 2)
	s.SaveBlock(b2)

	if s.BlockCount() != 3 {
		t.Fatalf("BlockCount = %d, want 3", s.BlockCount())
	}

	s.RemoveBlocksAbove(0)

	if s.BlockCount() != 1 {
		t.Errorf("BlockCount after remove = %d, want 1", s.BlockCount())
	}
	if !s.HasBlock(genesis.Hash) {
		t.Error("genesis should still exist")
	}
	if s.HasBlock(b1.Hash) {
		t.Error("b1 should be removed")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Metadata Tests
// ──────────────────────────────────────────────────────────────────────────────

func TestSaveAndLoadMetadata(t *testing.T) {
	s := tmpStore(t)
	meta := &NodeMetadata{
		BestTipHash:  "abc123",
		BestHeight:   42,
		TargetHex:    block.BitsFromTarget(block.InitialTarget),
		TotalMined:   150,
		TotalFaucet:  5000,
		GenesisOwner: "owner123",
	}

	if err := s.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata: %v", err)
	}

	loaded, err := s.LoadMetadata()
	if err != nil {
		t.Fatalf("LoadMetadata: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadMetadata returned nil")
	}
	if loaded.BestTipHash != meta.BestTipHash {
		t.Errorf("BestTipHash = %s, want %s", loaded.BestTipHash, meta.BestTipHash)
	}
	if loaded.BestHeight != meta.BestHeight {
		t.Errorf("BestHeight = %d, want %d", loaded.BestHeight, meta.BestHeight)
	}
	if loaded.TotalMined != meta.TotalMined {
		t.Errorf("TotalMined = %f, want %f", loaded.TotalMined, meta.TotalMined)
	}
	if loaded.TotalFaucet != meta.TotalFaucet {
		t.Errorf("TotalFaucet = %f, want %f", loaded.TotalFaucet, meta.TotalFaucet)
	}
	if loaded.GenesisOwner != meta.GenesisOwner {
		t.Errorf("GenesisOwner = %s, want %s", loaded.GenesisOwner, meta.GenesisOwner)
	}
	if loaded.Version != currentStorageVersion {
		t.Errorf("Version = %d, want %d", loaded.Version, currentStorageVersion)
	}
}

func TestLoadMetadata_NotExists(t *testing.T) {
	s := tmpStore(t)
	loaded, err := s.LoadMetadata()
	if err != nil {
		t.Fatalf("LoadMetadata: %v", err)
	}
	if loaded != nil {
		t.Error("LoadMetadata should return nil for missing file")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Chainstate Tests
// ──────────────────────────────────────────────────────────────────────────────

func TestSaveAndLoadChainstate(t *testing.T) {
	s := tmpStore(t)
	snap := &ChainstateSnapshot{
		Height: 5,
		Hash:   "tipHash",
		UTXOs: []UTXOEntry{
			{TxID: "tx1", Index: 0, Address: "addr1", Amount: 100},
			{TxID: "tx2", Index: 1, Address: "addr2", Amount: 200},
		},
	}

	if err := s.SaveChainstate(snap); err != nil {
		t.Fatalf("SaveChainstate: %v", err)
	}

	loaded, err := s.LoadChainstate()
	if err != nil {
		t.Fatalf("LoadChainstate: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadChainstate returned nil")
	}
	if loaded.Height != snap.Height {
		t.Errorf("Height = %d, want %d", loaded.Height, snap.Height)
	}
	if len(loaded.UTXOs) != len(snap.UTXOs) {
		t.Errorf("UTXO count = %d, want %d", len(loaded.UTXOs), len(snap.UTXOs))
	}
}

func TestLoadChainstate_NotExists(t *testing.T) {
	s := tmpStore(t)
	loaded, err := s.LoadChainstate()
	if err != nil {
		t.Fatalf("LoadChainstate: %v", err)
	}
	if loaded != nil {
		t.Error("LoadChainstate should return nil for missing file")
	}
}

func TestChainstateExists(t *testing.T) {
	s := tmpStore(t)
	if s.ChainstateExists() {
		t.Error("ChainstateExists should be false initially")
	}

	s.SaveChainstate(&ChainstateSnapshot{Height: 0, Hash: "h", UTXOs: nil})

	if !s.ChainstateExists() {
		t.Error("ChainstateExists should be true after save")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Atomic Write Tests
// ──────────────────────────────────────────────────────────────────────────────

func TestAtomicWrite_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	if err := atomicWrite(path, []byte(`{"test":true}`)); err != nil {
		t.Fatalf("atomicWrite: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != `{"test":true}` {
		t.Errorf("content = %s, want {\"test\":true}", string(data))
	}

	// No temp file should remain.
	tmpPath := path + tmpSuffix
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Error("temp file should not exist after atomic write")
	}
}

func TestAtomicWrite_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	atomicWrite(path, []byte(`old`))
	atomicWrite(path, []byte(`new`))

	data, _ := os.ReadFile(path)
	if string(data) != "new" {
		t.Errorf("content = %s, want new", string(data))
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Recovery Tests
// ──────────────────────────────────────────────────────────────────────────────

func TestRecovery_CleansUpTempFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	os.MkdirAll(filepath.Join(dir, blocksSubdir), 0755)
	os.MkdirAll(filepath.Join(dir, chainstateSubdir), 0755)

	// Create a leftover temp file.
	tmpPath := filepath.Join(dir, "metadata.json.tmp")
	os.WriteFile(tmpPath, []byte("incomplete"), 0644)

	tmpBlock := filepath.Join(dir, blocksSubdir, "00000001_abc.json.tmp")
	os.WriteFile(tmpBlock, []byte("incomplete"), 0644)

	// Create store — should clean up temp files.
	_, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Error("temp metadata file should have been cleaned up")
	}
	if _, err := os.Stat(tmpBlock); !os.IsNotExist(err) {
		t.Error("temp block file should have been cleaned up")
	}
}

func TestRecovery_RebuildIndexFromBlockstore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")

	// Create store and save some blocks.
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	genesis := block.NewGenesisBlock()
	s.SaveBlock(genesis)

	b1 := mineTestBlock(genesis, 1)
	s.SaveBlock(b1)

	// Now create a NEW store from the same directory — simulates restart.
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(restart): %v", err)
	}

	if s2.BlockCount() != 2 {
		t.Errorf("BlockCount after restart = %d, want 2", s2.BlockCount())
	}
	if !s2.HasBlock(genesis.Hash) {
		t.Error("genesis block should exist after restart")
	}
	if !s2.HasBlock(b1.Hash) {
		t.Error("block 1 should exist after restart")
	}

	// Should be able to load blocks.
	loaded, err := s2.LoadBlock(b1.Hash)
	if err != nil {
		t.Fatalf("LoadBlock after restart: %v", err)
	}
	if loaded.Hash != b1.Hash {
		t.Errorf("loaded hash = %s, want %s", loaded.Hash, b1.Hash)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Legacy Migration Tests
// ──────────────────────────────────────────────────────────────────────────────

func TestMigrateFromLegacy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// Create a fake legacy file.
	genesis := block.NewGenesisBlock()
	b1 := mineTestBlock(genesis, 1)

	legacyData := LegacyData{
		Chain: &LegacyChain{
			Blocks:       []*block.Block{genesis, b1},
			TotalMined:   50,
			TotalFaucet:  0,
			GenesisOwner: block.LegacyGenesisAddress,
			TargetHex:    block.BitsFromTarget(block.InitialTarget),
		},
	}
	legacyJSON, _ := json.MarshalIndent(legacyData, "", "  ")
	legacyPath := filepath.Join(t.TempDir(), "node_data.json")
	os.WriteFile(legacyPath, legacyJSON, 0644)

	// Migrate.
	migrated, err := s.MigrateFromLegacy(legacyPath)
	if err != nil {
		t.Fatalf("MigrateFromLegacy: %v", err)
	}
	if !migrated {
		t.Fatal("MigrateFromLegacy should return true")
	}

	// Verify blocks were migrated.
	if s.BlockCount() != 2 {
		t.Errorf("BlockCount = %d, want 2", s.BlockCount())
	}
	if !s.HasBlock(genesis.Hash) {
		t.Error("genesis should exist")
	}
	if !s.HasBlock(b1.Hash) {
		t.Error("block 1 should exist")
	}

	// Verify metadata was created.
	meta, err := s.LoadMetadata()
	if err != nil {
		t.Fatalf("LoadMetadata: %v", err)
	}
	if meta == nil {
		t.Fatal("metadata should exist")
	}
	if meta.BestHeight != 1 {
		t.Errorf("BestHeight = %d, want 1", meta.BestHeight)
	}
	if meta.TotalMined != 50 {
		t.Errorf("TotalMined = %f, want 50", meta.TotalMined)
	}

	// Second call should be a no-op.
	migrated2, err := s.MigrateFromLegacy(legacyPath)
	if err != nil {
		t.Fatalf("second MigrateFromLegacy: %v", err)
	}
	if migrated2 {
		t.Error("second migration should be no-op")
	}
}

func TestMigrateFromLegacy_NoFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	migrated, err := s.MigrateFromLegacy("/nonexistent/path.json")
	if err != nil {
		t.Fatalf("MigrateFromLegacy: %v", err)
	}
	if migrated {
		t.Error("should not migrate when legacy file doesn't exist")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Restart Persistence Test
// ──────────────────────────────────────────────────────────────────────────────

func TestFullRestartPersistence(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")

	// Session 1: create store, save genesis + 2 blocks + metadata + chainstate.
	s1, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	genesis := block.NewGenesisBlock()
	s1.SaveBlock(genesis)

	b1 := mineTestBlock(genesis, 1)
	s1.SaveBlock(b1)

	b2 := mineTestBlock(b1, 2)
	s1.SaveBlock(b2)

	meta := &NodeMetadata{
		BestTipHash:  b2.Hash,
		BestHeight:   2,
		TargetHex:    block.BitsFromTarget(block.InitialTarget),
		TotalMined:   100,
		TotalFaucet:  5000,
		GenesisOwner: "test_owner",
	}
	s1.SaveMetadata(meta)

	snap := &ChainstateSnapshot{
		Height: 2,
		Hash:   b2.Hash,
		UTXOs: []UTXOEntry{
			{TxID: "tx1", Index: 0, Address: "addr1", Amount: 100},
		},
	}
	s1.SaveChainstate(snap)

	// Session 2: reopen from same directory — simulate node restart.
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(restart): %v", err)
	}

	// Blocks should all be present.
	if s2.BlockCount() != 3 {
		t.Errorf("BlockCount = %d, want 3", s2.BlockCount())
	}

	// All blocks should be loadable.
	blocks, err := s2.LoadAllBlocksOrdered()
	if err != nil {
		t.Fatalf("LoadAllBlocksOrdered: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("len = %d, want 3", len(blocks))
	}
	if blocks[0].Header.Height != 0 || blocks[1].Header.Height != 1 || blocks[2].Header.Height != 2 {
		t.Error("blocks not in correct order")
	}

	// Metadata should be preserved.
	meta2, err := s2.LoadMetadata()
	if err != nil {
		t.Fatalf("LoadMetadata: %v", err)
	}
	if meta2.BestHeight != 2 {
		t.Errorf("BestHeight = %d, want 2", meta2.BestHeight)
	}
	if meta2.TotalMined != 100 {
		t.Errorf("TotalMined = %f, want 100", meta2.TotalMined)
	}

	// Chainstate should be preserved.
	snap2, err := s2.LoadChainstate()
	if err != nil {
		t.Fatalf("LoadChainstate: %v", err)
	}
	if snap2.Height != 2 {
		t.Errorf("chainstate Height = %d, want 2", snap2.Height)
	}
	if len(snap2.UTXOs) != 1 {
		t.Errorf("UTXO count = %d, want 1", len(snap2.UTXOs))
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Block file name parsing
// ──────────────────────────────────────────────────────────────────────────────

func TestBlockFileName(t *testing.T) {
	name := blockFileName(42, "abc123")
	if name != "00000042_abc123.json" {
		t.Errorf("blockFileName = %s, want 00000042_abc123.json", name)
	}

	height, hash, ok := parseBlockFileName(name)
	if !ok {
		t.Fatal("parseBlockFileName failed")
	}
	if height != 42 {
		t.Errorf("height = %d, want 42", height)
	}
	if hash != "abc123" {
		t.Errorf("hash = %s, want abc123", hash)
	}
}
