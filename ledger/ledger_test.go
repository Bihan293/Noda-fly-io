package ledger

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/Bihan293/Noda/block"
	"github.com/Bihan293/Noda/crypto"
)

func tmpFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "test_ledger.json")
}

func TestNewLedger(t *testing.T) {
	l := NewLedger(tmpFile(t))
	if l == nil {
		t.Fatal("NewLedger() returned nil")
	}
	if l.Chain == nil {
		t.Error("Chain is nil")
	}
	if l.UTXOSet == nil {
		t.Error("UTXOSet is nil")
	}
	if l.Mempool == nil {
		t.Error("Mempool is nil")
	}
	if l.GetChainHeight() != 0 {
		t.Errorf("GetChainHeight() = %d, want 0", l.GetChainHeight())
	}
}

func TestGetBalance_Genesis(t *testing.T) {
	l := NewLedger(tmpFile(t))
	balance := l.GetBalance(block.LegacyGenesisAddress)
	if balance != block.GenesisSupply {
		t.Errorf("genesis balance = %f, want %f", balance, block.GenesisSupply)
	}
}

func TestGetBalance_Unknown(t *testing.T) {
	l := NewLedger(tmpFile(t))
	balance := l.GetBalance("unknown_address")
	if balance != 0 {
		t.Errorf("unknown balance = %f, want 0", balance)
	}
}

func TestGetAllBalances(t *testing.T) {
	l := NewLedger(tmpFile(t))
	balances := l.GetAllBalances()
	if balances[block.LegacyGenesisAddress] != block.GenesisSupply {
		t.Errorf("genesis balance = %f, want %f", balances[block.LegacyGenesisAddress], block.GenesisSupply)
	}
}

func TestGetBlockReward(t *testing.T) {
	l := NewLedger(tmpFile(t))
	reward := l.GetBlockReward()
	if reward != 50.0 {
		t.Errorf("GetBlockReward() = %f, want 50", reward)
	}
}

func TestGetMempoolSize(t *testing.T) {
	l := NewLedger(tmpFile(t))
	if l.GetMempoolSize() != 0 {
		t.Errorf("GetMempoolSize() = %d, want 0", l.GetMempoolSize())
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Faucet
// ──────────────────────────────────────────────────────────────────────────────

func TestSetFaucetKey(t *testing.T) {
	l := NewLedger(tmpFile(t))
	kp, _ := crypto.GenerateKeyPair()
	privHex := hex.EncodeToString(kp.PrivateKey)

	err := l.SetFaucetKey(privHex)
	if err != nil {
		t.Fatalf("SetFaucetKey() error: %v", err)
	}
	if l.FaucetAddress() != kp.Address {
		t.Errorf("FaucetAddress() = %s, want %s", l.FaucetAddress(), kp.Address)
	}
}

func TestSetFaucetKey_Invalid(t *testing.T) {
	l := NewLedger(tmpFile(t))
	err := l.SetFaucetKey("invalid")
	if err == nil {
		t.Error("SetFaucetKey() should fail for invalid key")
	}
}

func TestFaucetState_NotConfigured(t *testing.T) {
	l := NewLedger(tmpFile(t))

	if l.FaucetAddress() != "" {
		t.Error("FaucetAddress() should be empty when not configured")
	}
	if l.IsFaucetActive() {
		t.Error("IsFaucetActive() should be false when not configured")
	}
}

func TestFaucetTotalDistributed(t *testing.T) {
	l := NewLedger(tmpFile(t))
	if l.FaucetTotalDistributed() != 0 {
		t.Errorf("FaucetTotalDistributed() = %f, want 0", l.FaucetTotalDistributed())
	}
}

func TestFaucetRemaining(t *testing.T) {
	l := NewLedger(tmpFile(t))
	remaining := l.FaucetRemaining()
	if remaining != FaucetGlobalCap {
		t.Errorf("FaucetRemaining() = %f, want %f", remaining, FaucetGlobalCap)
	}
}

func TestProcessFaucet_NotConfigured(t *testing.T) {
	l := NewLedger(tmpFile(t))
	_, err := l.ProcessFaucet("some_address")
	if err == nil {
		t.Error("ProcessFaucet() should fail when faucet not configured")
	}
}

func TestProcessFaucet_EmptyAddress(t *testing.T) {
	l := NewLedger(tmpFile(t))
	kp, _ := crypto.GenerateKeyPair()
	l.SetFaucetKey(hex.EncodeToString(kp.PrivateKey))

	_, err := l.ProcessFaucet("")
	if err == nil {
		t.Error("ProcessFaucet() should fail for empty address")
	}
}

func TestProcessFaucet_SelfSend(t *testing.T) {
	l := NewLedger(tmpFile(t))
	kp, _ := crypto.GenerateKeyPair()
	l.SetFaucetKey(hex.EncodeToString(kp.PrivateKey))

	_, err := l.ProcessFaucet(kp.Address)
	if err == nil {
		t.Error("ProcessFaucet() should fail when sending to faucet itself")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Transaction Validation (CRITICAL-2: UTXO inputs/outputs)
// ──────────────────────────────────────────────────────────────────────────────

func TestValidateUserTx_NoInputs(t *testing.T) {
	l := NewLedger(tmpFile(t))
	tx := block.Transaction{
		Version: block.TxVersion,
		Inputs:  nil,
		Outputs: []block.TxOutput{{Amount: 10, Address: "addr"}},
	}
	err := l.ValidateUserTx(tx)
	if err == nil {
		t.Error("ValidateUserTx() should fail for no inputs")
	}
}

func TestValidateUserTx_NoOutputs(t *testing.T) {
	l := NewLedger(tmpFile(t))
	tx := block.Transaction{
		Version: block.TxVersion,
		Inputs:  []block.TxInput{{PrevTxID: "tx1", PrevIndex: 0}},
		Outputs: nil,
	}
	err := l.ValidateUserTx(tx)
	if err == nil {
		t.Error("ValidateUserTx() should fail for no outputs")
	}
}

func TestValidateUserTx_NegativeAmount(t *testing.T) {
	l := NewLedger(tmpFile(t))
	tx := block.Transaction{
		Version: block.TxVersion,
		Inputs:  []block.TxInput{{PrevTxID: "tx1", PrevIndex: 0}},
		Outputs: []block.TxOutput{{Amount: -5, Address: "addr"}},
	}
	err := l.ValidateUserTx(tx)
	if err == nil {
		t.Error("ValidateUserTx() should fail for negative amount")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Wallet Builder + Submit (CRITICAL-2)
// ──────────────────────────────────────────────────────────────────────────────

func TestBuildTransaction(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	privHex := hex.EncodeToString(kp.PrivateKey)

	l := NewLedgerWithOwner(tmpFile(t), kp.Address)

	recipient, _ := crypto.GenerateKeyPair()

	tx, err := l.BuildTransaction(privHex, kp.Address, recipient.Address, 100)
	if err != nil {
		t.Fatalf("BuildTransaction() error: %v", err)
	}
	if tx == nil {
		t.Fatal("BuildTransaction() returned nil")
	}
	if len(tx.Inputs) == 0 {
		t.Error("BuildTransaction() should have at least one input")
	}
	if len(tx.Outputs) < 1 {
		t.Error("BuildTransaction() should have at least one output")
	}
	if tx.ID == "" {
		t.Error("BuildTransaction() should set tx ID")
	}

	// Verify the transaction passes validation.
	err = l.ValidateUserTx(*tx)
	if err != nil {
		t.Fatalf("ValidateUserTx() failed for built tx: %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Persistence
// ──────────────────────────────────────────────────────────────────────────────

func TestSaveAndLoad(t *testing.T) {
	path := tmpFile(t)

	l := NewLedger(path)
	if err := l.Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// HIGH-1: storage now uses a directory (blockstore), not a single JSON file.
	// Verify the store directory was created.
	storeDir := storeDirFromPath(path)
	if _, err := os.Stat(storeDir); os.IsNotExist(err) {
		t.Fatal("Save() did not create store directory")
	}

	l2 := LoadLedger(path)
	if l2 == nil {
		t.Fatal("LoadLedger() returned nil")
	}
	if l2.GetChainHeight() != l.GetChainHeight() {
		t.Errorf("loaded height = %d, want %d", l2.GetChainHeight(), l.GetChainHeight())
	}
	if l2.UTXOSet == nil {
		t.Error("loaded UTXOSet is nil")
	}
	if l2.Mempool == nil {
		t.Error("loaded Mempool is nil")
	}
	if l2.GetStore() == nil {
		t.Error("loaded Store is nil")
	}
}

func TestLoadLedger_FileNotFound(t *testing.T) {
	l := LoadLedger("/tmp/nonexistent_ledger_test.json")
	if l == nil {
		t.Fatal("LoadLedger() returned nil for missing file")
	}
	if l.GetChainHeight() != 0 {
		t.Errorf("height = %d, want 0", l.GetChainHeight())
	}
}

func TestLoadLedger_InvalidJSON(t *testing.T) {
	path := tmpFile(t)
	os.WriteFile(path, []byte("not valid json"), 0644)

	l := LoadLedger(path)
	if l == nil {
		t.Fatal("LoadLedger() returned nil for invalid JSON")
	}
	if l.GetChainHeight() != 0 {
		t.Errorf("height = %d, want 0", l.GetChainHeight())
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Chain Replacement
// ──────────────────────────────────────────────────────────────────────────────

func TestReplaceChain_Shorter(t *testing.T) {
	l := NewLedger(tmpFile(t))

	shorter := l.GetChain()
	replaced := l.ReplaceChain(shorter)
	if replaced {
		t.Error("ReplaceChain() should not accept a same-length chain")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Accessors
// ──────────────────────────────────────────────────────────────────────────────

func TestGetChain(t *testing.T) {
	l := NewLedger(tmpFile(t))
	c := l.GetChain()
	if c == nil {
		t.Error("GetChain() returned nil")
	}
}

func TestGetPendingTransactions(t *testing.T) {
	l := NewLedger(tmpFile(t))
	pending := l.GetPendingTransactions(10)
	if len(pending) != 0 {
		t.Errorf("GetPendingTransactions() len = %d, want 0", len(pending))
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// [CRITICAL-1] Genesis/Faucet Ownership Tests
// ──────────────────────────────────────────────────────────────────────────────

func TestNewKeyGetsGenesisControl(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privHex := hex.EncodeToString(kp.PrivateKey)

	l := NewLedgerWithOwner(tmpFile(t), kp.Address)

	if l.GenesisOwner() != kp.Address {
		t.Errorf("GenesisOwner() = %s, want %s", l.GenesisOwner(), kp.Address)
	}

	balance := l.GetBalance(kp.Address)
	if balance != block.GenesisSupply {
		t.Errorf("genesis balance = %f, want %f", balance, block.GenesisSupply)
	}

	if err := l.SetFaucetKeyAndValidateGenesis(privHex); err != nil {
		t.Fatalf("SetFaucetKeyAndValidateGenesis() error: %v", err)
	}

	if !l.FaucetOwnerMatch() {
		t.Error("FaucetOwnerMatch() should be true")
	}

	usable := l.UsableFaucetBalance()
	if usable != block.GenesisSupply {
		t.Errorf("UsableFaucetBalance() = %f, want %f", usable, block.GenesisSupply)
	}

	if !l.IsFaucetActive() {
		t.Error("IsFaucetActive() should be true")
	}
}

func TestFaucetWorksWithMatchingKey(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privHex := hex.EncodeToString(kp.PrivateKey)

	recipient, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	l := NewLedgerWithOwner(tmpFile(t), kp.Address)
	if err := l.SetFaucetKeyAndValidateGenesis(privHex); err != nil {
		t.Fatalf("SetFaucetKeyAndValidateGenesis() error: %v", err)
	}

	tx, err := l.ProcessFaucet(recipient.Address)
	if err != nil {
		t.Fatalf("ProcessFaucet() error: %v", err)
	}
	if tx == nil {
		t.Fatal("ProcessFaucet() returned nil tx")
	}
	if tx.TotalOutputValue() < FaucetAmount {
		// Total output includes change back to faucet + amount to recipient.
		// The recipient output should be FaucetAmount.
		found := false
		for _, out := range tx.Outputs {
			if out.Address == recipient.Address && out.Amount == FaucetAmount {
				found = true
			}
		}
		if !found {
			t.Errorf("faucet tx should have output of %f to recipient", FaucetAmount)
		}
	}

	// CRITICAL-3: tx is in mempool, not yet confirmed. Balance changes happen after mining.
	if l.GetMempoolSize() != 1 {
		t.Errorf("mempool size = %d, want 1 (faucet tx pending)", l.GetMempoolSize())
	}

	// Faucet tracking should already be updated.
	if l.FaucetTotalDistributed() != FaucetAmount {
		t.Errorf("FaucetTotalDistributed() = %f, want %f", l.FaucetTotalDistributed(), FaucetAmount)
	}
}

func TestIncompatibleKeyFailsFast(t *testing.T) {
	kp1, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	kp2, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	priv1Hex := hex.EncodeToString(kp1.PrivateKey)
	priv2Hex := hex.EncodeToString(kp2.PrivateKey)

	path := tmpFile(t)

	l := NewLedgerWithOwner(path, kp1.Address)
	if err := l.SetFaucetKeyAndValidateGenesis(priv1Hex); err != nil {
		t.Fatalf("first SetFaucetKeyAndValidateGenesis() error: %v", err)
	}

	recipient, _ := crypto.GenerateKeyPair()
	_, err = l.ProcessFaucet(recipient.Address)
	if err != nil {
		t.Fatalf("ProcessFaucet() error: %v", err)
	}

	if err := l.Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	l2 := LoadLedger(path)

	err = l2.SetFaucetKeyAndValidateGenesis(priv2Hex)
	if err == nil {
		t.Fatal("SetFaucetKeyAndValidateGenesis() should fail for incompatible key")
	}
	if !isGenesisOwnerMismatch(err) {
		t.Errorf("expected genesis owner mismatch error, got: %v", err)
	}
}

func TestLegacyGenesisMigration(t *testing.T) {
	path := tmpFile(t)

	l := NewLedger(path)

	if l.GenesisOwner() != block.LegacyGenesisAddress {
		t.Fatalf("expected legacy genesis owner %s, got %s",
			block.LegacyGenesisAddress, l.GenesisOwner())
	}

	legacyBalance := l.GetBalance(block.LegacyGenesisAddress)
	if legacyBalance != block.GenesisSupply {
		t.Fatalf("legacy genesis balance = %f, want %f", legacyBalance, block.GenesisSupply)
	}

	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privHex := hex.EncodeToString(kp.PrivateKey)

	if err := l.SetFaucetKeyAndValidateGenesis(privHex); err != nil {
		t.Fatalf("SetFaucetKeyAndValidateGenesis() for legacy migration error: %v", err)
	}

	if l.GenesisOwner() != kp.Address {
		t.Errorf("after migration GenesisOwner() = %s, want %s", l.GenesisOwner(), kp.Address)
	}

	newBalance := l.GetBalance(kp.Address)
	if newBalance != block.GenesisSupply {
		t.Errorf("after migration balance = %f, want %f", newBalance, block.GenesisSupply)
	}

	legacyBalance = l.GetBalance(block.LegacyGenesisAddress)
	if legacyBalance != 0 {
		t.Errorf("legacy address still has balance %f after migration", legacyBalance)
	}
}

func TestLegacyMigration_BlockedAfterActivity(t *testing.T) {
	path := tmpFile(t)

	l := NewLedger(path)

	l.Chain.TotalFaucet = 5000

	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privHex := hex.EncodeToString(kp.PrivateKey)

	err = l.SetFaucetKeyAndValidateGenesis(privHex)
	if err == nil {
		t.Fatal("SetFaucetKeyAndValidateGenesis() should fail — migration blocked after faucet activity")
	}
}

func TestGenesisOwnerPersistence(t *testing.T) {
	path := tmpFile(t)

	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	l := NewLedgerWithOwner(path, kp.Address)
	if err := l.Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	l2 := LoadLedger(path)
	if l2.GenesisOwner() != kp.Address {
		t.Errorf("loaded GenesisOwner() = %s, want %s", l2.GenesisOwner(), kp.Address)
	}
}

func TestStatusShowsGenesisOwner(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	l := NewLedgerWithOwner(tmpFile(t), kp.Address)

	owner := l.GenesisOwner()
	if owner != kp.Address {
		t.Errorf("GenesisOwner() = %s, want %s", owner, kp.Address)
	}

	if l.FaucetOwnerMatch() {
		t.Error("FaucetOwnerMatch() should be false without faucet configured")
	}

	if l.UsableFaucetBalance() != 0 {
		t.Errorf("UsableFaucetBalance() = %f, want 0", l.UsableFaucetBalance())
	}
}

func TestLoadLedgerWithOwner_NewFile(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	path := tmpFile(t)
	l := LoadLedgerWithOwner(path, kp.Address)

	if l.GenesisOwner() != kp.Address {
		t.Errorf("GenesisOwner() = %s, want %s", l.GenesisOwner(), kp.Address)
	}
	if l.GetBalance(kp.Address) != block.GenesisSupply {
		t.Errorf("balance = %f, want %f", l.GetBalance(kp.Address), block.GenesisSupply)
	}
}

func isGenesisOwnerMismatch(err error) bool {
	if err == nil {
		return false
	}
	return err.Error() != "" && (err == ErrGenesisOwnerMismatch ||
		len(err.Error()) > len("genesis owner mismatch") &&
			err.Error()[:len("genesis owner mismatch")] == "genesis owner mismatch")
}
