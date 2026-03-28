package utxo

import (
	"testing"

	"github.com/Bihan293/Noda/block"
)

// ──────────────────────────────────────────────────────────────────────────────
// OutPoint
// ──────────────────────────────────────────────────────────────────────────────

func TestOutPoint_Key(t *testing.T) {
	op := OutPoint{TxID: "abc", Index: 2}
	if op.Key() != "abc:2" {
		t.Errorf("Key() = %s, want abc:2", op.Key())
	}
}

func TestOutPoint_String(t *testing.T) {
	op := OutPoint{TxID: "abc", Index: 0}
	if op.String() != "abc:0" {
		t.Errorf("String() = %s, want abc:0", op.String())
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// UTXO Set Basic Operations
// ──────────────────────────────────────────────────────────────────────────────

func TestNewSet(t *testing.T) {
	s := NewSet()
	if s == nil {
		t.Fatal("NewSet() returned nil")
	}
	if s.Size() != 0 {
		t.Errorf("new set Size() = %d, want 0", s.Size())
	}
}

func TestAdd(t *testing.T) {
	s := NewSet()
	op := OutPoint{TxID: "tx1", Index: 0}
	out := Output{Address: "alice", Amount: 100}

	s.Add(op, out)

	if s.Size() != 1 {
		t.Errorf("Size() = %d after Add, want 1", s.Size())
	}
	if !s.Has(op) {
		t.Error("Has() = false after Add")
	}
}

func TestSpend(t *testing.T) {
	s := NewSet()
	op := OutPoint{TxID: "tx1", Index: 0}
	out := Output{Address: "alice", Amount: 100}
	s.Add(op, out)

	spent, err := s.Spend(op)
	if err != nil {
		t.Fatalf("Spend() error: %v", err)
	}
	if spent.Address != "alice" || spent.Amount != 100 {
		t.Errorf("Spend() returned wrong output: %+v", spent)
	}
	if s.Size() != 0 {
		t.Errorf("Size() = %d after Spend, want 0", s.Size())
	}
	if s.Has(op) {
		t.Error("Has() = true after Spend")
	}
}

func TestSpend_DoubleSpend(t *testing.T) {
	s := NewSet()
	op := OutPoint{TxID: "tx1", Index: 0}
	out := Output{Address: "alice", Amount: 100}
	s.Add(op, out)

	s.Spend(op) // First spend succeeds.
	_, err := s.Spend(op)
	if err == nil {
		t.Error("Spend() should fail on double-spend")
	}
}

func TestGet(t *testing.T) {
	s := NewSet()
	op := OutPoint{TxID: "tx1", Index: 0}
	out := Output{Address: "alice", Amount: 100}
	s.Add(op, out)

	got := s.Get(op)
	if got == nil {
		t.Fatal("Get() returned nil")
	}
	if got.Address != "alice" {
		t.Errorf("Get().Address = %s, want alice", got.Address)
	}

	// Non-existent.
	if s.Get(OutPoint{TxID: "xx", Index: 0}) != nil {
		t.Error("Get() should return nil for non-existent")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Balance Queries
// ──────────────────────────────────────────────────────────────────────────────

func TestBalance(t *testing.T) {
	s := NewSet()
	s.Add(OutPoint{TxID: "tx1", Index: 0}, Output{Address: "alice", Amount: 50})
	s.Add(OutPoint{TxID: "tx2", Index: 0}, Output{Address: "alice", Amount: 30})
	s.Add(OutPoint{TxID: "tx3", Index: 0}, Output{Address: "bob", Amount: 100})

	if b := s.Balance("alice"); b != 80 {
		t.Errorf("Balance(alice) = %f, want 80", b)
	}
	if b := s.Balance("bob"); b != 100 {
		t.Errorf("Balance(bob) = %f, want 100", b)
	}
	if b := s.Balance("charlie"); b != 0 {
		t.Errorf("Balance(charlie) = %f, want 0", b)
	}
}

func TestAllBalances(t *testing.T) {
	s := NewSet()
	s.Add(OutPoint{TxID: "tx1", Index: 0}, Output{Address: "alice", Amount: 50})
	s.Add(OutPoint{TxID: "tx2", Index: 0}, Output{Address: "bob", Amount: 100})

	balances := s.AllBalances()
	if balances["alice"] != 50 {
		t.Errorf("AllBalances[alice] = %f, want 50", balances["alice"])
	}
	if balances["bob"] != 100 {
		t.Errorf("AllBalances[bob] = %f, want 100", balances["bob"])
	}
}

func TestGetUTXOsForAddress(t *testing.T) {
	s := NewSet()
	s.Add(OutPoint{TxID: "tx1", Index: 0}, Output{Address: "alice", Amount: 50})
	s.Add(OutPoint{TxID: "tx2", Index: 0}, Output{Address: "alice", Amount: 30})
	s.Add(OutPoint{TxID: "tx3", Index: 0}, Output{Address: "bob", Amount: 100})

	aliceUTXOs := s.GetUTXOsForAddress("alice")
	if len(aliceUTXOs) != 2 {
		t.Errorf("GetUTXOsForAddress(alice) len = %d, want 2", len(aliceUTXOs))
	}

	bobUTXOs := s.GetUTXOsForAddress("bob")
	if len(bobUTXOs) != 1 {
		t.Errorf("GetUTXOsForAddress(bob) len = %d, want 1", len(bobUTXOs))
	}
}

func TestFindUTXOsForAmount(t *testing.T) {
	s := NewSet()
	s.Add(OutPoint{TxID: "tx1", Index: 0}, Output{Address: "alice", Amount: 50})
	s.Add(OutPoint{TxID: "tx2", Index: 0}, Output{Address: "alice", Amount: 30})

	selected, total, err := s.FindUTXOsForAmount("alice", 60)
	if err != nil {
		t.Fatalf("FindUTXOsForAmount() error: %v", err)
	}
	if total < 60 {
		t.Errorf("total = %f, want >= 60", total)
	}
	if len(selected) == 0 {
		t.Error("FindUTXOsForAmount() returned no UTXOs")
	}
}

func TestFindUTXOsForAmount_Insufficient(t *testing.T) {
	s := NewSet()
	s.Add(OutPoint{TxID: "tx1", Index: 0}, Output{Address: "alice", Amount: 50})

	_, _, err := s.FindUTXOsForAmount("alice", 100)
	if err == nil {
		t.Error("FindUTXOsForAmount() should fail with insufficient funds")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// ApplyBlock (CRITICAL-2: explicit inputs/outputs)
// ──────────────────────────────────────────────────────────────────────────────

func TestApplyBlock_Coinbase(t *testing.T) {
	s := NewSet()
	tx := block.NewCoinbaseTx("alice", 1000, 0)
	b := &block.Block{
		Header:       block.BlockHeader{Height: 0},
		Transactions: []block.Transaction{tx},
	}

	err := s.ApplyBlock(b)
	if err != nil {
		t.Fatalf("ApplyBlock() error: %v", err)
	}

	if s.Balance("alice") != 1000 {
		t.Errorf("Balance(alice) = %f, want 1000", s.Balance("alice"))
	}
}

func TestApplyBlock_Genesis(t *testing.T) {
	s := NewSet()
	genesis := block.NewGenesisBlock()
	err := s.ApplyBlock(genesis)
	if err != nil {
		t.Fatalf("ApplyBlock(genesis) error: %v", err)
	}
	if s.Balance(block.LegacyGenesisAddress) != block.GenesisSupply {
		t.Errorf("Balance = %f, want %f", s.Balance(block.LegacyGenesisAddress), block.GenesisSupply)
	}
}

func TestApplyBlock_Transfer(t *testing.T) {
	s := NewSet()

	// First: give alice 100 coins via a coinbase-like genesis tx.
	genTx := block.Transaction{
		Version:      block.TxVersion,
		Outputs:      []block.TxOutput{{Amount: 100, Address: "alice"}},
		CoinbaseData: "genesis",
	}
	genTx.ID = block.HashTransaction(&genTx)

	genBlock := &block.Block{
		Header:       block.BlockHeader{Height: 0},
		Transactions: []block.Transaction{genTx},
	}
	s.ApplyBlock(genBlock)

	// Now alice sends 60 to bob with explicit inputs/outputs.
	transferTx := block.Transaction{
		Version: block.TxVersion,
		Inputs: []block.TxInput{
			{PrevTxID: genTx.ID, PrevIndex: 0, PubKey: "alice", Signature: "sig"},
		},
		Outputs: []block.TxOutput{
			{Amount: 60, Address: "bob"},
			{Amount: 40, Address: "alice"}, // change
		},
	}
	transferTx.ID = block.HashTransaction(&transferTx)

	transferBlock := &block.Block{
		Header:       block.BlockHeader{Height: 1},
		Transactions: []block.Transaction{transferTx},
	}
	err := s.ApplyBlock(transferBlock)
	if err != nil {
		t.Fatalf("ApplyBlock(transfer) error: %v", err)
	}

	if s.Balance("bob") != 60 {
		t.Errorf("Balance(bob) = %f, want 60", s.Balance("bob"))
	}
	if s.Balance("alice") != 40 {
		t.Errorf("Balance(alice) = %f, want 40", s.Balance("alice"))
	}
}

func TestApplyBlock_MissingInput(t *testing.T) {
	s := NewSet()

	// Try to spend a non-existent UTXO.
	tx := block.Transaction{
		Version: block.TxVersion,
		Inputs: []block.TxInput{
			{PrevTxID: "nonexistent_tx", PrevIndex: 0, PubKey: "alice", Signature: "sig"},
		},
		Outputs: []block.TxOutput{
			{Amount: 100, Address: "bob"},
		},
	}
	tx.ID = block.HashTransaction(&tx)

	b := &block.Block{
		Header:       block.BlockHeader{Height: 0},
		Transactions: []block.Transaction{tx},
	}

	err := s.ApplyBlock(b)
	if err == nil {
		t.Error("ApplyBlock() should fail with missing input UTXO")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// RebuildFromBlocks
// ──────────────────────────────────────────────────────────────────────────────

func TestRebuildFromBlocks(t *testing.T) {
	genesis := block.NewGenesisBlock()
	blocks := []*block.Block{genesis}

	s, err := RebuildFromBlocks(blocks)
	if err != nil {
		t.Fatalf("RebuildFromBlocks() error: %v", err)
	}

	balance := s.Balance(block.LegacyGenesisAddress)
	if balance != block.GenesisSupply {
		t.Errorf("genesis balance = %f, want %f", balance, block.GenesisSupply)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Serialization
// ──────────────────────────────────────────────────────────────────────────────

func TestMarshalUnmarshalJSON(t *testing.T) {
	s := NewSet()
	s.Add(OutPoint{TxID: "tx1", Index: 0}, Output{Address: "alice", Amount: 50})
	s.Add(OutPoint{TxID: "tx2", Index: 0}, Output{Address: "bob", Amount: 100})

	data, err := s.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON() error: %v", err)
	}

	s2 := NewSet()
	err = s2.UnmarshalJSON(data)
	if err != nil {
		t.Fatalf("UnmarshalJSON() error: %v", err)
	}

	if s2.Size() != 2 {
		t.Errorf("UnmarshalJSON Size() = %d, want 2", s2.Size())
	}
	if s2.Balance("alice") != 50 {
		t.Errorf("UnmarshalJSON Balance(alice) = %f, want 50", s2.Balance("alice"))
	}
}
