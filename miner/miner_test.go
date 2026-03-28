package miner

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/Bihan293/Noda/block"
	"github.com/Bihan293/Noda/crypto"
	"github.com/Bihan293/Noda/ledger"
)

func tmpFile(t *testing.T) string {
	t.Helper()
	return t.TempDir() + "/test.json"
}

func TestNew(t *testing.T) {
	cfg := DefaultConfig()
	l := ledger.NewLedger(tmpFile(t))
	m := New(cfg, l)

	if m == nil {
		t.Fatal("New() returned nil")
	}
	if m.IsEnabled() {
		t.Error("miner should be disabled by default")
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Enabled {
		t.Error("default config should have Enabled=false")
	}
	if cfg.MinerAddress != "" {
		t.Error("default config should have empty MinerAddress")
	}
	if cfg.BlockMaxTx != DefaultBlockMaxTx {
		t.Errorf("BlockMaxTx = %d, want %d", cfg.BlockMaxTx, DefaultBlockMaxTx)
	}
	if cfg.Interval != DefaultMiningInterval {
		t.Errorf("Interval = %v, want %v", cfg.Interval, DefaultMiningInterval)
	}
}

func TestMiner_DisabledDoesNotRun(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = false
	l := ledger.NewLedger(tmpFile(t))
	m := New(cfg, l)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Run should return quickly since mining is disabled.
	m.Run(ctx)

	if m.BlocksMined() != 0 {
		t.Errorf("BlocksMined() = %d, want 0", m.BlocksMined())
	}
}

func TestMiner_EnabledEmptyMempool(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	cfg := Config{
		Enabled:      true,
		MinerAddress: kp.Address,
		BlockMaxTx:   100,
		Interval:     50 * time.Millisecond,
		MaxAttempts:  10_000_000,
	}
	l := ledger.NewLedger(tmpFile(t))
	m := New(cfg, l)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	go m.Run(ctx)
	<-ctx.Done()

	// No blocks should be mined (empty mempool).
	if m.BlocksMined() != 0 {
		t.Errorf("BlocksMined() = %d, want 0 (empty mempool)", m.BlocksMined())
	}
}

func TestMiner_MinesBlockWithTx(t *testing.T) {
	senderKP, _ := crypto.GenerateKeyPair()
	minerKP, _ := crypto.GenerateKeyPair()
	recvKP, _ := crypto.GenerateKeyPair()

	l := ledger.NewLedgerWithOwner(tmpFile(t), senderKP.Address)

	// Build and submit a transaction.
	privHex := keyHex(senderKP)
	tx, err := l.BuildTransaction(privHex, senderKP.Address, recvKP.Address, 100)
	if err != nil {
		t.Fatalf("BuildTransaction() error: %v", err)
	}
	if err := l.SubmitTransaction(*tx); err != nil {
		t.Fatalf("SubmitTransaction() error: %v", err)
	}

	if l.GetMempoolSize() != 1 {
		t.Fatalf("mempool size = %d, want 1", l.GetMempoolSize())
	}

	cfg := Config{
		Enabled:      true,
		MinerAddress: minerKP.Address,
		BlockMaxTx:   100,
		Interval:     50 * time.Millisecond,
		MaxAttempts:  10_000_000,
	}
	m := New(cfg, l)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go m.Run(ctx)

	// Wait for miner.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("miner timeout")
		default:
			if l.GetMempoolSize() == 0 {
				goto done
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
done:
	// Give the miner goroutine a moment to update stats after applying the block.
	time.Sleep(100 * time.Millisecond)
	cancel()

	if l.GetMempoolSize() != 0 {
		t.Errorf("mempool should be empty after mining")
	}
	if m.BlocksMined() == 0 {
		t.Error("BlocksMined() should be > 0")
	}
	if m.LastMinedHash() == "" {
		t.Error("LastMinedHash() should not be empty")
	}

	// Receiver should have balance.
	if l.GetBalance(recvKP.Address) != 100 {
		t.Errorf("receiver balance = %f, want 100", l.GetBalance(recvKP.Address))
	}

	// Miner should have reward.
	minerBal := l.GetBalance(minerKP.Address)
	expectedReward := block.BlockReward(1, 0)
	if minerBal != expectedReward {
		t.Errorf("miner balance = %f, want %f", minerBal, expectedReward)
	}
}

func TestMiner_Accessors(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	cfg := Config{
		Enabled:      true,
		MinerAddress: kp.Address,
		BlockMaxTx:   50,
		Interval:     1 * time.Second,
		MaxAttempts:  5000,
	}
	l := ledger.NewLedger(tmpFile(t))
	m := New(cfg, l)

	if !m.IsEnabled() {
		t.Error("IsEnabled() should be true")
	}
	if m.MinerAddress() != kp.Address {
		t.Errorf("MinerAddress() = %s, want %s", m.MinerAddress(), kp.Address)
	}
	if m.LastMinedHash() != "" {
		t.Error("LastMinedHash() should be empty initially")
	}
	if m.BlocksMined() != 0 {
		t.Errorf("BlocksMined() = %d, want 0", m.BlocksMined())
	}
	c := m.Config()
	if c.BlockMaxTx != 50 {
		t.Errorf("Config().BlockMaxTx = %d, want 50", c.BlockMaxTx)
	}
}

func keyHex(kp *crypto.KeyPair) string {
	return hex.EncodeToString(kp.PrivateKey)
}
