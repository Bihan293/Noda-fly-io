package chain

import (
	"testing"

	"github.com/Bihan293/Noda/block"
)

func TestNewBlockchain(t *testing.T) {
	bc := NewBlockchain()
	if bc == nil {
		t.Fatal("NewBlockchain() returned nil")
	}
	if bc.Len() != 1 {
		t.Errorf("new blockchain Len() = %d, want 1 (genesis)", bc.Len())
	}
	if bc.Height() != 0 {
		t.Errorf("new blockchain Height() = %d, want 0", bc.Height())
	}
	if bc.Target == nil {
		t.Error("new blockchain Target is nil")
	}
}

func TestBlockchain_LastBlock(t *testing.T) {
	bc := NewBlockchain()
	last := bc.LastBlock()
	if last == nil {
		t.Fatal("LastBlock() returned nil for new blockchain")
	}
	if last.Header.Height != 0 {
		t.Errorf("LastBlock().Height = %d, want 0", last.Header.Height)
	}
}

func TestBlockchain_LastHash(t *testing.T) {
	bc := NewBlockchain()
	hash := bc.LastHash()
	if hash == "" {
		t.Error("LastHash() is empty for new blockchain")
	}
}

func TestBlockchain_GetBlock(t *testing.T) {
	bc := NewBlockchain()

	// Genesis block at height 0.
	b := bc.GetBlock(0)
	if b == nil {
		t.Error("GetBlock(0) returned nil")
	}

	// Out of range.
	b = bc.GetBlock(99)
	if b != nil {
		t.Error("GetBlock(99) should return nil")
	}
}

func TestBlockchain_AddBlock(t *testing.T) {
	bc := NewBlockchain()

	// Create a valid block at height 1.
	tx := block.NewCoinbaseTx("miner", 50, 1)
	merkle := block.ComputeMerkleRoot([]string{tx.ID})
	target := bc.GetTarget()

	newBlock := &block.Block{
		Header: block.BlockHeader{
			Version:       block.BlockVersion,
			Height:        1,
			PrevBlockHash: bc.LastHash(),
			MerkleRoot:    merkle,
			Timestamp:     bc.LastBlock().Header.Timestamp + 600,
		},
		Transactions: []block.Transaction{tx},
	}

	if err := block.MineBlock(newBlock, target, 10_000_000); err != nil {
		t.Fatalf("MineBlock() error: %v", err)
	}

	if err := bc.AddBlock(newBlock); err != nil {
		t.Fatalf("AddBlock() error: %v", err)
	}

	if bc.Len() != 2 {
		t.Errorf("Len() = %d after AddBlock, want 2", bc.Len())
	}
	if bc.Height() != 1 {
		t.Errorf("Height() = %d after AddBlock, want 1", bc.Height())
	}
}

func TestBlockchain_AddBlock_WrongHeight(t *testing.T) {
	bc := NewBlockchain()

	newBlock := &block.Block{
		Header: block.BlockHeader{
			Version:       block.BlockVersion,
			Height:        5, // Wrong height.
			PrevBlockHash: bc.LastHash(),
		},
		Transactions: []block.Transaction{{ID: "tx"}},
	}
	newBlock.Hash = block.HashBlockHeader(newBlock.Header)

	err := bc.AddBlock(newBlock)
	if err == nil {
		t.Error("AddBlock() should fail with wrong height")
	}
}

func TestBlockchain_GetBlockReward(t *testing.T) {
	bc := NewBlockchain()
	reward := bc.GetBlockReward()
	if reward != 50.0 {
		t.Errorf("GetBlockReward() = %f, want 50", reward)
	}
}

func TestBlockchain_AllTransactions(t *testing.T) {
	bc := NewBlockchain()
	txs := bc.AllTransactions()
	if len(txs) != 1 {
		t.Errorf("AllTransactions() len = %d, want 1 (genesis tx)", len(txs))
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Serialization
// ──────────────────────────────────────────────────────────────────────────────

func TestBlockchain_ToJSON_FromJSON(t *testing.T) {
	bc := NewBlockchain()

	data, err := bc.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON() error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("ToJSON() returned empty data")
	}

	bc2, err := FromJSON(data)
	if err != nil {
		t.Fatalf("FromJSON() error: %v", err)
	}

	if bc2.Len() != bc.Len() {
		t.Errorf("FromJSON Len() = %d, want %d", bc2.Len(), bc.Len())
	}
	if bc2.Height() != bc.Height() {
		t.Errorf("FromJSON Height() = %d, want %d", bc2.Height(), bc.Height())
	}
	if bc2.Target == nil {
		t.Error("FromJSON Target is nil")
	}
}

func TestFromJSON_Invalid(t *testing.T) {
	_, err := FromJSON([]byte("invalid json"))
	if err == nil {
		t.Error("FromJSON() should fail with invalid JSON")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Chain Validation
// ──────────────────────────────────────────────────────────────────────────────

func TestValidateChain(t *testing.T) {
	bc := NewBlockchain()
	err := ValidateChain(bc)
	if err != nil {
		t.Errorf("ValidateChain() error: %v", err)
	}
}

func TestValidateChain_Empty(t *testing.T) {
	bc := &Blockchain{Blocks: nil}
	err := ValidateChain(bc)
	if err == nil {
		t.Error("ValidateChain() should fail with empty blockchain")
	}
}

func TestValidateChain_Tampered(t *testing.T) {
	bc := NewBlockchain()
	// Tamper with genesis hash (must be 64 chars to avoid slice panic in block validation).
	bc.Blocks[0].Hash = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	err := ValidateChain(bc)
	if err == nil {
		t.Error("ValidateChain() should fail with tampered chain")
	}
}
