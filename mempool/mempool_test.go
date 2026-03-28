package mempool

import (
	"fmt"
	"testing"

	"github.com/Bihan293/Noda/block"
)

func makeTx(id string, inputs int, outputs int) block.Transaction {
	ins := make([]block.TxInput, inputs)
	for i := 0; i < inputs; i++ {
		ins[i] = block.TxInput{PrevTxID: fmt.Sprintf("prev_%s_%d", id, i), PrevIndex: 0, PubKey: "pk", Signature: "sig"}
	}
	outs := make([]block.TxOutput, outputs)
	for i := 0; i < outputs; i++ {
		outs[i] = block.TxOutput{Amount: float64(10 + i), Address: fmt.Sprintf("addr_%d", i)}
	}
	return block.Transaction{
		ID:      id,
		Version: block.TxVersion,
		Inputs:  ins,
		Outputs: outs,
	}
}

func TestNew(t *testing.T) {
	mp := New(100)
	if mp == nil {
		t.Fatal("New() returned nil")
	}
	if mp.Size() != 0 {
		t.Errorf("new mempool Size() = %d, want 0", mp.Size())
	}
}

func TestNew_DefaultSize(t *testing.T) {
	mp := New(0)
	if mp.maxSize != DefaultMaxSize {
		t.Errorf("New(0).maxSize = %d, want %d", mp.maxSize, DefaultMaxSize)
	}
}

func TestAdd(t *testing.T) {
	mp := New(100)
	tx := makeTx("tx1", 1, 1)

	err := mp.Add(tx)
	if err != nil {
		t.Fatalf("Add() error: %v", err)
	}
	if mp.Size() != 1 {
		t.Errorf("Size() = %d after Add, want 1", mp.Size())
	}
}

func TestAdd_Duplicate(t *testing.T) {
	mp := New(100)
	tx := makeTx("tx1", 1, 1)

	mp.Add(tx)
	err := mp.Add(tx)
	if err == nil {
		t.Error("Add() should fail for duplicate transaction")
	}
}

func TestAdd_NoID(t *testing.T) {
	mp := New(100)
	tx := block.Transaction{ID: "", Version: block.TxVersion}

	err := mp.Add(tx)
	if err == nil {
		t.Error("Add() should fail for transaction without ID")
	}
}

func TestAdd_PoolFull(t *testing.T) {
	mp := New(2)

	mp.Add(makeTx("tx1", 1, 1))
	mp.Add(makeTx("tx2", 1, 1))

	// Third should evict the oldest.
	err := mp.Add(makeTx("tx3", 1, 1))
	if err != nil {
		t.Fatalf("Add() should evict oldest when full, got error: %v", err)
	}
	if mp.Size() != 2 {
		t.Errorf("Size() = %d after eviction, want 2", mp.Size())
	}
	if mp.Has("tx1") {
		t.Error("tx1 should have been evicted")
	}
}

func TestAdd_DoubleSpendOutpoint(t *testing.T) {
	mp := New(100)

	// Two transactions spending the same outpoint.
	tx1 := block.Transaction{
		ID:      "tx1",
		Version: block.TxVersion,
		Inputs:  []block.TxInput{{PrevTxID: "prev1", PrevIndex: 0, PubKey: "pk", Signature: "sig"}},
		Outputs: []block.TxOutput{{Amount: 10, Address: "addr1"}},
	}
	tx2 := block.Transaction{
		ID:      "tx2",
		Version: block.TxVersion,
		Inputs:  []block.TxInput{{PrevTxID: "prev1", PrevIndex: 0, PubKey: "pk", Signature: "sig"}},
		Outputs: []block.TxOutput{{Amount: 10, Address: "addr2"}},
	}

	if err := mp.Add(tx1); err != nil {
		t.Fatalf("Add(tx1) error: %v", err)
	}
	if err := mp.Add(tx2); err == nil {
		t.Error("Add(tx2) should fail: double-spend on same outpoint")
	}
}

func TestRemove(t *testing.T) {
	mp := New(100)
	tx := makeTx("tx1", 1, 1)
	mp.Add(tx)

	mp.Remove("tx1")
	if mp.Size() != 0 {
		t.Errorf("Size() = %d after Remove, want 0", mp.Size())
	}
	if mp.Has("tx1") {
		t.Error("Has(tx1) should be false after Remove")
	}
}

func TestRemove_CleansOutpointTracking(t *testing.T) {
	mp := New(100)
	tx1 := block.Transaction{
		ID:      "tx1",
		Version: block.TxVersion,
		Inputs:  []block.TxInput{{PrevTxID: "prev1", PrevIndex: 0, PubKey: "pk", Signature: "sig"}},
		Outputs: []block.TxOutput{{Amount: 10, Address: "addr1"}},
	}
	mp.Add(tx1)
	mp.Remove("tx1")

	// Now the outpoint should be free for another tx.
	tx2 := block.Transaction{
		ID:      "tx2",
		Version: block.TxVersion,
		Inputs:  []block.TxInput{{PrevTxID: "prev1", PrevIndex: 0, PubKey: "pk", Signature: "sig"}},
		Outputs: []block.TxOutput{{Amount: 10, Address: "addr2"}},
	}
	if err := mp.Add(tx2); err != nil {
		t.Errorf("Add(tx2) should succeed after tx1 removed: %v", err)
	}
}

func TestRemoveBatch(t *testing.T) {
	mp := New(100)
	mp.Add(makeTx("tx1", 1, 1))
	mp.Add(makeTx("tx2", 1, 1))
	mp.Add(makeTx("tx3", 1, 1))

	mp.RemoveBatch([]string{"tx1", "tx3"})
	if mp.Size() != 1 {
		t.Errorf("Size() = %d after RemoveBatch, want 1", mp.Size())
	}
	if !mp.Has("tx2") {
		t.Error("tx2 should still be in pool")
	}
}

func TestHas(t *testing.T) {
	mp := New(100)
	tx := makeTx("tx1", 1, 1)
	mp.Add(tx)

	if !mp.Has("tx1") {
		t.Error("Has(tx1) = false, want true")
	}
	if mp.Has("nonexistent") {
		t.Error("Has(nonexistent) = true, want false")
	}
}

func TestGet(t *testing.T) {
	mp := New(100)
	tx := makeTx("tx1", 1, 1)
	mp.Add(tx)

	got := mp.Get("tx1")
	if got == nil {
		t.Fatal("Get(tx1) returned nil")
	}
	if got.ID != "tx1" {
		t.Errorf("Get(tx1).ID = %s, want tx1", got.ID)
	}

	if mp.Get("nonexistent") != nil {
		t.Error("Get(nonexistent) should return nil")
	}
}

func TestGetPending(t *testing.T) {
	mp := New(100)
	for i := 0; i < 5; i++ {
		mp.Add(makeTx(fmt.Sprintf("tx%d", i), 1, 1))
	}

	// Get only 3.
	pending := mp.GetPending(3)
	if len(pending) != 3 {
		t.Errorf("GetPending(3) len = %d, want 3", len(pending))
	}

	// Get all.
	all := mp.GetAll()
	if len(all) != 5 {
		t.Errorf("GetAll() len = %d, want 5", len(all))
	}
}

func TestGetPending_FIFO(t *testing.T) {
	mp := New(100)
	mp.Add(makeTx("tx1", 1, 1))
	mp.Add(makeTx("tx2", 1, 1))
	mp.Add(makeTx("tx3", 1, 1))

	pending := mp.GetPending(2)
	if pending[0].ID != "tx1" {
		t.Errorf("first pending = %s, want tx1", pending[0].ID)
	}
	if pending[1].ID != "tx2" {
		t.Errorf("second pending = %s, want tx2", pending[1].ID)
	}
}

func TestIsOutpointSpent(t *testing.T) {
	mp := New(100)
	tx := block.Transaction{
		ID:      "tx1",
		Version: block.TxVersion,
		Inputs:  []block.TxInput{{PrevTxID: "prev1", PrevIndex: 0, PubKey: "pk", Signature: "sig"}},
		Outputs: []block.TxOutput{{Amount: 10, Address: "addr1"}},
	}
	mp.Add(tx)

	if !mp.IsOutpointSpent("prev1", 0) {
		t.Error("IsOutpointSpent(prev1:0) = false, want true")
	}
	if mp.IsOutpointSpent("prev1", 1) {
		t.Error("IsOutpointSpent(prev1:1) = true, want false")
	}
	if mp.IsOutpointSpent("other", 0) {
		t.Error("IsOutpointSpent(other:0) = true, want false")
	}
}

func TestMin(t *testing.T) {
	if min(3, 5) != 3 {
		t.Error("min(3,5) should be 3")
	}
	if min(5, 3) != 3 {
		t.Error("min(5,3) should be 3")
	}
	if min(3, 3) != 3 {
		t.Error("min(3,3) should be 3")
	}
}
