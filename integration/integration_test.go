// Package integration provides end-to-end tests for the Noda cryptocurrency node.
//
// These tests exercise multiple packages together to validate:
//   - Mining + faucet + transfer flow
//   - Chain validation and serialization round-trips
//   - Tokenomics enforcement (faucet cap, mining cap)
//   - UTXO consistency across operations
//   - CRITICAL-2: explicit UTXO inputs/outputs in transactions
//   - CRITICAL-3: decoupled mempool → miner pipeline with fees
package integration

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Bihan293/Noda/block"
	"github.com/Bihan293/Noda/chain"
	"github.com/Bihan293/Noda/crypto"
	"github.com/Bihan293/Noda/ledger"
	"github.com/Bihan293/Noda/mempool"
	"github.com/Bihan293/Noda/miner"
	"github.com/Bihan293/Noda/utxo"
)

func tmpFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "test.json")
}

// ──────────────────────────────────────────────────────────────────────────────
// End-to-End: Mining + Block Addition
// ──────────────────────────────────────────────────────────────────────────────

func TestMiningEndToEnd(t *testing.T) {
	bc := chain.NewBlockchain()

	// Mine 3 blocks.
	for i := uint64(1); i <= 3; i++ {
		reward := bc.GetBlockReward()
		tx := block.NewCoinbaseTx("miner_addr", reward, i)
		merkle := block.ComputeMerkleRoot([]string{tx.ID})
		target := bc.GetTarget()

		b := &block.Block{
			Header: block.BlockHeader{
				Version:       block.BlockVersion,
				Height:        i,
				PrevBlockHash: bc.LastHash(),
				MerkleRoot:    merkle,
				Timestamp:     bc.LastBlock().Header.Timestamp + 600,
			},
			Transactions: []block.Transaction{tx},
		}

		if err := block.MineBlock(b, target, 10_000_000); err != nil {
			t.Fatalf("MineBlock(%d) error: %v", i, err)
		}
		if err := bc.AddBlock(b); err != nil {
			t.Fatalf("AddBlock(%d) error: %v", i, err)
		}
	}

	if bc.Height() != 3 {
		t.Errorf("Height() = %d, want 3", bc.Height())
	}
	if bc.TotalMined != 150 { // 3 * 50
		t.Errorf("TotalMined = %f, want 150", bc.TotalMined)
	}

	// Validate the chain.
	if err := chain.ValidateChain(bc); err != nil {
		t.Errorf("ValidateChain() error: %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// End-to-End: Chain Serialization Round-Trip
// ──────────────────────────────────────────────────────────────────────────────

func TestChainSerializationRoundTrip(t *testing.T) {
	bc := chain.NewBlockchain()

	// Mine a block.
	tx := block.NewCoinbaseTx("miner", 50, 1)
	merkle := block.ComputeMerkleRoot([]string{tx.ID})
	target := bc.GetTarget()

	b := &block.Block{
		Header: block.BlockHeader{
			Version:       block.BlockVersion,
			Height:        1,
			PrevBlockHash: bc.LastHash(),
			MerkleRoot:    merkle,
			Timestamp:     bc.LastBlock().Header.Timestamp + 600,
		},
		Transactions: []block.Transaction{tx},
	}
	block.MineBlock(b, target, 10_000_000)
	bc.AddBlock(b)

	// Serialize.
	data, err := bc.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON() error: %v", err)
	}

	// Deserialize.
	bc2, err := chain.FromJSON(data)
	if err != nil {
		t.Fatalf("FromJSON() error: %v", err)
	}

	if bc2.Len() != bc.Len() {
		t.Errorf("deserialized Len() = %d, want %d", bc2.Len(), bc.Len())
	}
	if bc2.Height() != bc.Height() {
		t.Errorf("deserialized Height() = %d, want %d", bc2.Height(), bc.Height())
	}
	if bc2.TotalMined != bc.TotalMined {
		t.Errorf("deserialized TotalMined = %f, want %f", bc2.TotalMined, bc.TotalMined)
	}

	if err := chain.ValidateChain(bc2); err != nil {
		t.Errorf("ValidateChain(deserialized) error: %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// End-to-End: UTXO Consistency
// ──────────────────────────────────────────────────────────────────────────────

func TestUTXOConsistency(t *testing.T) {
	genesis := block.NewGenesisBlock()
	blocks := []*block.Block{genesis}

	// Mine block 1: coinbase 50 to miner.
	tx1 := block.NewCoinbaseTx("miner", 50, 1)
	merkle1 := block.ComputeMerkleRoot([]string{tx1.ID})
	easyTarget := new(big.Int)
	easyTarget.SetString("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", 16)

	b1 := &block.Block{
		Header: block.BlockHeader{
			Version:       block.BlockVersion,
			Height:        1,
			PrevBlockHash: genesis.Hash,
			MerkleRoot:    merkle1,
			Timestamp:     genesis.Header.Timestamp + 600,
		},
		Transactions: []block.Transaction{tx1},
	}
	block.MineBlock(b1, easyTarget, 100)
	blocks = append(blocks, b1)

	// Rebuild UTXO set.
	utxoSet, err := utxo.RebuildFromBlocks(blocks)
	if err != nil {
		t.Fatalf("RebuildFromBlocks() error: %v", err)
	}

	genesisBalance := utxoSet.Balance(block.LegacyGenesisAddress)
	if genesisBalance != block.GenesisSupply {
		t.Errorf("genesis balance = %f, want %f", genesisBalance, block.GenesisSupply)
	}

	minerBalance := utxoSet.Balance("miner")
	if minerBalance != 50 {
		t.Errorf("miner balance = %f, want 50", minerBalance)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// End-to-End: Crypto Sign + Verify (CRITICAL-2: sighash model)
// ──────────────────────────────────────────────────────────────────────────────

func TestSignVerifyIntegration(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	privHex := hex.EncodeToString(kp.PrivateKey)
	to := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	amount := 42.5

	// Build a transaction to sign.
	tx := &block.Transaction{
		Version: block.TxVersion,
		Inputs: []block.TxInput{
			{PrevTxID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PrevIndex: 0, PubKey: kp.Address},
		},
		Outputs: []block.TxOutput{
			{Amount: amount, Address: to},
		},
	}

	// Compute sighash and sign.
	sighash := block.ComputeSighash(tx)
	sig, err := crypto.SignSighash(privHex, sighash)
	if err != nil {
		t.Fatal(err)
	}

	// Verify using sighash.
	if !crypto.VerifySighash(kp.Address, sighash, sig) {
		t.Error("sighash signature verification failed")
	}

	// Derive address from private key.
	addr, err := crypto.AddressFromPrivateKey(privHex)
	if err != nil {
		t.Fatal(err)
	}
	if addr != kp.Address {
		t.Errorf("derived address = %s, want %s", addr, kp.Address)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// End-to-End: Mempool + Block Confirmation (CRITICAL-2)
// ──────────────────────────────────────────────────────────────────────────────

func TestMempoolBlockConfirmation(t *testing.T) {
	mp := mempool.New(100)

	for i := 0; i < 3; i++ {
		tx := block.Transaction{
			ID:      fmt.Sprintf("tx%d", i),
			Version: block.TxVersion,
			Inputs: []block.TxInput{
				{PrevTxID: fmt.Sprintf("prev_%d", i), PrevIndex: 0, PubKey: "pk", Signature: "sig"},
			},
			Outputs: []block.TxOutput{
				{Amount: float64(10 + i), Address: "recipient"},
			},
		}
		mp.Add(tx)
	}

	if mp.Size() != 3 {
		t.Errorf("mempool size = %d, want 3", mp.Size())
	}

	mp.RemoveBatch([]string{"tx0", "tx2"})

	if mp.Size() != 1 {
		t.Errorf("mempool size after confirmation = %d, want 1", mp.Size())
	}
	if !mp.Has("tx1") {
		t.Error("tx1 should still be pending")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// End-to-End: Faucet Cap Enforcement
// ──────────────────────────────────────────────────────────────────────────────

func TestFaucetConstants(t *testing.T) {
	if ledger.FaucetAmount != 100 {
		t.Errorf("FaucetAmount = %f, want 100", ledger.FaucetAmount)
	}
	if ledger.FaucetGlobalCap != 1_000_000 {
		t.Errorf("FaucetGlobalCap = %f, want 1000000", ledger.FaucetGlobalCap)
	}
	if block.GenesisSupply != ledger.FaucetGlobalCap {
		t.Errorf("GenesisSupply(%f) != FaucetGlobalCap(%f)", block.GenesisSupply, ledger.FaucetGlobalCap)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// End-to-End: Ledger Save / Load
// ──────────────────────────────────────────────────────────────────────────────

func TestLedgerPersistence(t *testing.T) {
	path := tmpFile(t)

	l1 := ledger.NewLedger(path)
	if err := l1.Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	l2 := ledger.LoadLedger(path)
	if l2.GetChainHeight() != l1.GetChainHeight() {
		t.Errorf("loaded height = %d, want %d", l2.GetChainHeight(), l1.GetChainHeight())
	}

	b1 := l1.GetBalance(block.LegacyGenesisAddress)
	b2 := l2.GetBalance(block.LegacyGenesisAddress)
	if b1 != b2 {
		t.Errorf("genesis balance: saved=%f loaded=%f", b1, b2)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Tokenomics Verification
// ──────────────────────────────────────────────────────────────────────────────

func TestTokenomics(t *testing.T) {
	if block.GenesisSupply+block.MaxMiningSupply != block.MaxTotalSupply {
		t.Errorf("Genesis(%f) + Mining(%f) != Total(%f)",
			block.GenesisSupply, block.MaxMiningSupply, block.MaxTotalSupply)
	}

	if ledger.FaucetGlobalCap != block.GenesisSupply {
		t.Errorf("FaucetGlobalCap(%f) != GenesisSupply(%f)",
			ledger.FaucetGlobalCap, block.GenesisSupply)
	}

	if block.InitialBlockReward != 50 {
		t.Errorf("InitialBlockReward = %f, want 50", block.InitialBlockReward)
	}

	if block.HalvingInterval != 210_000 {
		t.Errorf("HalvingInterval = %d, want 210000", block.HalvingInterval)
	}

	totalMiningRewards := 0.0
	for h := uint64(0); h < 10*block.HalvingInterval; h++ {
		reward := block.BlockReward(h, totalMiningRewards)
		if reward == 0 {
			break
		}
		totalMiningRewards += reward
	}
	if totalMiningRewards > block.MaxMiningSupply {
		t.Errorf("total mining rewards = %f, exceeds %f", totalMiningRewards, block.MaxMiningSupply)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CRITICAL-3: Transaction goes to mempool, NOT confirmed instantly
// ──────────────────────────────────────────────────────────────────────────────

func TestFullTransactionFlow(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privHex := hex.EncodeToString(kp.PrivateKey)

	// Create a ledger with this key as genesis owner.
	l := ledger.NewLedgerWithOwner(tmpFile(t), kp.Address)

	// Create recipient.
	recvKP, _ := crypto.GenerateKeyPair()
	receiver := recvKP.Address

	// Build a transaction using the wallet builder.
	amount := 100.0
	tx, err := l.BuildTransaction(privHex, kp.Address, receiver, amount)
	if err != nil {
		t.Fatalf("BuildTransaction() error: %v", err)
	}

	// Verify the transaction is well-formed.
	if len(tx.Inputs) == 0 {
		t.Error("tx should have inputs")
	}
	if len(tx.Outputs) < 1 {
		t.Error("tx should have at least one output")
	}
	if tx.ID == "" {
		t.Error("tx ID should be set")
	}

	// Validate through ledger.
	err = l.ValidateUserTx(*tx)
	if err != nil {
		t.Fatalf("ValidateUserTx() error: %v", err)
	}

	// Submit — CRITICAL-3: tx goes to mempool, NOT mined instantly.
	err = l.SubmitTransaction(*tx)
	if err != nil {
		t.Fatalf("SubmitTransaction() error: %v", err)
	}

	// CRITICAL-3: tx should be in mempool, not yet confirmed.
	if l.GetMempoolSize() != 1 {
		t.Errorf("mempool size = %d, want 1 (tx pending)", l.GetMempoolSize())
	}

	// Balances should NOT have changed yet (tx is pending).
	receiverBalance := l.GetBalance(receiver)
	if receiverBalance != 0 {
		t.Errorf("receiver balance = %f, want 0 (tx still pending)", receiverBalance)
	}

	senderBalance := l.GetBalance(kp.Address)
	if senderBalance != block.GenesisSupply {
		t.Errorf("sender balance = %f, want %f (tx still pending)", senderBalance, block.GenesisSupply)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CRITICAL-3: Miner picks up multiple tx from mempool + fees in coinbase
// ──────────────────────────────────────────────────────────────────────────────

func TestMinerPicksUpMempoolTxs(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privHex := hex.EncodeToString(kp.PrivateKey)
	minerKP, _ := crypto.GenerateKeyPair()

	l := ledger.NewLedgerWithOwner(tmpFile(t), kp.Address)

	// Submit a transaction to a recipient.
	// Note: genesis has 1 big UTXO, so we can only build 1 tx at a time
	// from the same sender (the change UTXO is not available until mined).
	recvKP, _ := crypto.GenerateKeyPair()
	tx, err := l.BuildTransaction(privHex, kp.Address, recvKP.Address, 100)
	if err != nil {
		t.Fatalf("BuildTransaction() error: %v", err)
	}
	if err := l.SubmitTransaction(*tx); err != nil {
		t.Fatalf("SubmitTransaction() error: %v", err)
	}

	// Should be in mempool.
	if l.GetMempoolSize() != 1 {
		t.Fatalf("mempool size = %d, want 1", l.GetMempoolSize())
	}

	// Configure and run miner once.
	cfg := miner.Config{
		Enabled:      true,
		MinerAddress: minerKP.Address,
		BlockMaxTx:   100,
		Interval:     50 * time.Millisecond,
		MaxAttempts:  10_000_000,
	}
	m := miner.New(cfg, l)

	// Run miner in background, let it process.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go m.Run(ctx)

	// Wait for miner to process.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("miner did not process transactions within timeout")
		default:
			if l.GetMempoolSize() == 0 {
				goto mined1
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
mined1:
	cancel()

	// Mempool should be empty.
	if l.GetMempoolSize() != 0 {
		t.Errorf("mempool size = %d, want 0 after mining", l.GetMempoolSize())
	}

	// Chain should have grown (genesis + 1 block).
	if l.GetChainHeight() < 1 {
		t.Errorf("chain height = %d, want >= 1", l.GetChainHeight())
	}

	// Recipient should have received coins.
	if l.GetBalance(recvKP.Address) != 100 {
		t.Errorf("receiver balance = %f, want 100", l.GetBalance(recvKP.Address))
	}

	// Now submit 2 more txs from the sender (who now has change UTXO).
	recv2, _ := crypto.GenerateKeyPair()
	recv3, _ := crypto.GenerateKeyPair()
	tx2, err := l.BuildTransaction(privHex, kp.Address, recv2.Address, 200)
	if err != nil {
		t.Fatalf("BuildTransaction(2) error: %v", err)
	}
	if err := l.SubmitTransaction(*tx2); err != nil {
		t.Fatalf("SubmitTransaction(2) error: %v", err)
	}
	// tx3 must use a different UTXO, but the change from tx2 is not mined yet.
	// So we can only have 1 pending tx per sender at a time from the same UTXO.
	// Let's mine tx2 first, then submit tx3.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	m2 := miner.New(cfg, l)
	go m2.Run(ctx2)

	deadline2 := time.After(3 * time.Second)
	for {
		select {
		case <-deadline2:
			t.Fatal("miner did not process tx2 within timeout")
		default:
			if l.GetMempoolSize() == 0 {
				goto mined2
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
mined2:
	cancel2()

	tx3, err := l.BuildTransaction(privHex, kp.Address, recv3.Address, 300)
	if err != nil {
		t.Fatalf("BuildTransaction(3) error: %v", err)
	}
	if err := l.SubmitTransaction(*tx3); err != nil {
		t.Fatalf("SubmitTransaction(3) error: %v", err)
	}
	ctx3, cancel3 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel3()
	m3 := miner.New(cfg, l)
	go m3.Run(ctx3)
	deadline3 := time.After(3 * time.Second)
	for {
		select {
		case <-deadline3:
			t.Fatal("miner did not process tx3 within timeout")
		default:
			if l.GetMempoolSize() == 0 {
				goto mined3
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
mined3:
	cancel3()

	// All recipients should have received coins.
	if l.GetBalance(recv2.Address) != 200 {
		t.Errorf("recv2 balance = %f, want 200", l.GetBalance(recv2.Address))
	}
	if l.GetBalance(recv3.Address) != 300 {
		t.Errorf("recv3 balance = %f, want 300", l.GetBalance(recv3.Address))
	}

	// Miner should have received rewards.
	minerBalance := l.GetBalance(minerKP.Address)
	if minerBalance <= 0 {
		t.Error("miner should have received block rewards")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CRITICAL-3: Fees correctly go to coinbase
// ──────────────────────────────────────────────────────────────────────────────

func TestFeesGoToCoinbase(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privHex := hex.EncodeToString(kp.PrivateKey)
	minerKP, _ := crypto.GenerateKeyPair()

	l := ledger.NewLedgerWithOwner(tmpFile(t), kp.Address)

	// Build a tx that pays a fee (send amount < input total, difference is fee).
	// Genesis has 11M in one UTXO. Send 100, change = 11M - 100 - fee.
	// The wallet builder sends exact amounts with change, so fee = 0 by default.
	// Let's build manually to create a fee.

	// Find the genesis UTXO.
	utxos := l.UTXOSet.GetUTXOsForAddress(kp.Address)
	if len(utxos) == 0 {
		t.Fatal("no UTXOs for genesis owner")
	}

	recvKP, _ := crypto.GenerateKeyPair()
	// Input: 11M. Output: 100 to recv + 10,999,890 change = 10 fee.
	txFee := 10.0
	txAmount := 100.0
	changeAmount := block.GenesisSupply - txAmount - txFee

	tx := &block.Transaction{
		Version: block.TxVersion,
		Inputs: []block.TxInput{
			{PrevTxID: utxos[0].OutPoint.TxID, PrevIndex: utxos[0].OutPoint.Index, PubKey: kp.Address},
		},
		Outputs: []block.TxOutput{
			{Amount: txAmount, Address: recvKP.Address},
			{Amount: changeAmount, Address: kp.Address},
		},
	}

	sighash := block.ComputeSighash(tx)
	sig, err := crypto.SignSighash(privHex, sighash)
	if err != nil {
		t.Fatal(err)
	}
	tx.Inputs[0].Signature = sig
	tx.ID = block.HashTransaction(tx)

	// Submit.
	if err := l.SubmitTransaction(*tx); err != nil {
		t.Fatalf("SubmitTransaction() error: %v", err)
	}

	// Mine with the miner.
	cfg := miner.Config{
		Enabled:      true,
		MinerAddress: minerKP.Address,
		BlockMaxTx:   100,
		Interval:     50 * time.Millisecond,
		MaxAttempts:  10_000_000,
	}
	mn := miner.New(cfg, l)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go mn.Run(ctx)

	// Wait for miner.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("miner did not process transactions within timeout")
		default:
			if l.GetMempoolSize() == 0 {
				goto done
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
done:
	cancel()

	// Miner should have block reward + fee.
	minerBalance := l.GetBalance(minerKP.Address)
	expectedMinerReward := block.BlockReward(1, 0) + txFee // block reward at height 1 + fee
	if minerBalance != expectedMinerReward {
		t.Errorf("miner balance = %f, want %f (reward + fee)", minerBalance, expectedMinerReward)
	}

	// Verify the coinbase tx in the block has the correct amount.
	ch := l.GetChain()
	if ch.Height() < 1 {
		t.Fatal("chain should have at least 1 block after mining")
	}
	minedBlock := ch.GetBlock(1)
	if minedBlock == nil {
		t.Fatal("block at height 1 should exist")
	}
	if len(minedBlock.Transactions) < 1 {
		t.Fatal("block should have at least 1 transaction (coinbase)")
	}
	coinbase := minedBlock.Transactions[0]
	if !coinbase.IsCoinbase() {
		t.Error("first tx in block should be coinbase")
	}
	coinbaseAmount := coinbase.TotalOutputValue()
	if coinbaseAmount != expectedMinerReward {
		t.Errorf("coinbase amount = %f, want %f", coinbaseAmount, expectedMinerReward)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CRITICAL-2: Verify no From/To/Amount in consensus blocks
// ──────────────────────────────────────────────────────────────────────────────

func TestNoLegacyFieldsInBlocks(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privHex := hex.EncodeToString(kp.PrivateKey)
	minerKP, _ := crypto.GenerateKeyPair()

	l := ledger.NewLedgerWithOwner(tmpFile(t), kp.Address)
	if err := l.SetFaucetKeyAndValidateGenesis(privHex); err != nil {
		t.Fatal(err)
	}

	recvKP, _ := crypto.GenerateKeyPair()
	_, err = l.ProcessFaucet(recvKP.Address)
	if err != nil {
		t.Fatalf("ProcessFaucet() error: %v", err)
	}

	// Mine the faucet tx.
	cfg := miner.Config{
		Enabled:      true,
		MinerAddress: minerKP.Address,
		BlockMaxTx:   100,
		Interval:     50 * time.Millisecond,
		MaxAttempts:  10_000_000,
	}
	mn := miner.New(cfg, l)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go mn.Run(ctx)

	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("miner did not process faucet tx within timeout")
		default:
			if l.GetMempoolSize() == 0 {
				goto done
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
done:
	cancel()

	// Inspect all transactions in all blocks.
	bc := l.GetChain()
	for _, b := range bc.Blocks {
		for _, tx := range b.Transactions {
			// Every tx must have explicit outputs.
			if len(tx.Outputs) == 0 {
				t.Errorf("block %d: tx %s has no outputs", b.Header.Height, tx.ID[:8])
			}
			// Non-coinbase txs must have explicit inputs.
			if !tx.IsCoinbase() && !tx.IsGenesis() {
				if len(tx.Inputs) == 0 {
					t.Errorf("block %d: regular tx %s has no inputs", b.Header.Height, tx.ID[:8])
				}
			}
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CRITICAL-4: Cumulative work chain selection
// ──────────────────────────────────────────────────────────────────────────────

func TestCumulativeWorkChainSelection(t *testing.T) {
	// Create two chains from the same genesis.
	// Chain A: 3 blocks with easy target (low work).
	// Chain B: 2 blocks with harder target (more total work).
	// The shorter chain B should win because it has more cumulative work.

	// Easy target (very easy to mine — low work per block).
	easyTarget := new(big.Int)
	easyTarget.SetString("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", 16)

	// Harder target (more work per block).
	hardTarget := new(big.Int)
	hardTarget.SetString("00ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", 16)

	// Build Chain A: genesis + 3 blocks with easy target.
	bcA := chain.NewBlockchain()
	for i := uint64(1); i <= 3; i++ {
		tx := block.NewCoinbaseTx("minerA", 50, i)
		merkle := block.ComputeMerkleRoot([]string{tx.ID})
		b := &block.Block{
			Header: block.BlockHeader{
				Version:       block.BlockVersion,
				Height:        i,
				PrevBlockHash: bcA.LastHash(),
				MerkleRoot:    merkle,
				Timestamp:     bcA.LastBlock().Header.Timestamp + 600,
			},
			Transactions: []block.Transaction{tx},
		}
		if err := block.MineBlock(b, easyTarget, 10_000_000); err != nil {
			t.Fatalf("Chain A MineBlock(%d) error: %v", i, err)
		}
		if err := bcA.AddBlock(b); err != nil {
			t.Fatalf("Chain A AddBlock(%d) error: %v", i, err)
		}
	}

	// Build Chain B: genesis + 2 blocks with harder target.
	bcB := chain.NewBlockchain()
	for i := uint64(1); i <= 2; i++ {
		tx := block.NewCoinbaseTx("minerB", 50, i)
		merkle := block.ComputeMerkleRoot([]string{tx.ID})
		b := &block.Block{
			Header: block.BlockHeader{
				Version:       block.BlockVersion,
				Height:        i,
				PrevBlockHash: bcB.LastHash(),
				MerkleRoot:    merkle,
				Timestamp:     bcB.LastBlock().Header.Timestamp + 600,
			},
			Transactions: []block.Transaction{tx},
		}
		if err := block.MineBlock(b, hardTarget, 100_000_000); err != nil {
			t.Fatalf("Chain B MineBlock(%d) error: %v", i, err)
		}
		if err := bcB.AddBlock(b); err != nil {
			t.Fatalf("Chain B AddBlock(%d) error: %v", i, err)
		}
	}

	// Chain A is LONGER (3 blocks post-genesis) but has LESS cumulative work.
	// Chain B is SHORTER (2 blocks post-genesis) but has MORE cumulative work.
	if bcA.Len() <= bcB.Len() {
		t.Fatalf("Chain A should be longer: A=%d B=%d", bcA.Len(), bcB.Len())
	}

	workA := bcA.CumulativeWork()
	workB := bcB.CumulativeWork()
	if workB.Cmp(workA) <= 0 {
		t.Fatalf("Chain B should have more work: A=%s B=%s", workA.String(), workB.String())
	}

	// Create a ledger with chain A.
	l := ledger.NewLedger(tmpFile(t))
	// Manually build chain A into the ledger.
	for i := 1; i < bcA.Len(); i++ {
		b := bcA.GetBlock(uint64(i))
		l.GetChain().AddBlock(b)
		l.UTXOSet.ApplyBlock(b)
	}

	// Now try to replace with chain B (shorter but more work).
	replaced := l.ReplaceChain(bcB)
	if !replaced {
		t.Error("ReplaceChain should accept chain B (more cumulative work)")
	}
	if l.GetChainHeight() != bcB.Height() {
		t.Errorf("height after replace = %d, want %d", l.GetChainHeight(), bcB.Height())
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CRITICAL-4: Reorg rolls back UTXO correctly
// ──────────────────────────────────────────────────────────────────────────────

func TestReorgRollsBackUTXO(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privHex := hex.EncodeToString(kp.PrivateKey)
	minerKP, _ := crypto.GenerateKeyPair()

	l := ledger.NewLedgerWithOwner(tmpFile(t), kp.Address)

	// Send coins and mine them.
	recvKP, _ := crypto.GenerateKeyPair()
	tx, err := l.BuildTransaction(privHex, kp.Address, recvKP.Address, 100)
	if err != nil {
		t.Fatalf("BuildTransaction() error: %v", err)
	}
	if err := l.SubmitTransaction(*tx); err != nil {
		t.Fatalf("SubmitTransaction() error: %v", err)
	}

	// Mine the block.
	cfg := miner.Config{
		Enabled:      true,
		MinerAddress: minerKP.Address,
		BlockMaxTx:   100,
		Interval:     50 * time.Millisecond,
		MaxAttempts:  10_000_000,
	}
	m := miner.New(cfg, l)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go m.Run(ctx)

	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("miner did not process transactions within timeout")
		default:
			if l.GetMempoolSize() == 0 {
				goto mined
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
mined:
	cancel()

	// Verify the receiver got coins.
	if l.GetBalance(recvKP.Address) != 100 {
		t.Errorf("receiver balance = %f, want 100", l.GetBalance(recvKP.Address))
	}

	// Record the state before replacing.
	heightBefore := l.GetChainHeight()
	if heightBefore < 1 {
		t.Fatal("chain should have height >= 1 after mining")
	}

	// Now create an alternative chain without the transfer (just coinbase).
	// It needs more cumulative work.
	altBC := chain.NewBlockchainWithOwner(kp.Address)
	hardTarget := new(big.Int)
	hardTarget.SetString("00ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", 16)

	for i := uint64(1); i <= heightBefore+1; i++ {
		altTx := block.NewCoinbaseTx(minerKP.Address, 50, i)
		merkle := block.ComputeMerkleRoot([]string{altTx.ID})
		b := &block.Block{
			Header: block.BlockHeader{
				Version:       block.BlockVersion,
				Height:        i,
				PrevBlockHash: altBC.LastHash(),
				MerkleRoot:    merkle,
				Timestamp:     altBC.LastBlock().Header.Timestamp + 600,
			},
			Transactions: []block.Transaction{altTx},
		}
		if err := block.MineBlock(b, hardTarget, 100_000_000); err != nil {
			t.Fatalf("Alt chain MineBlock(%d) error: %v", i, err)
		}
		if err := altBC.AddBlock(b); err != nil {
			t.Fatalf("Alt chain AddBlock(%d) error: %v", i, err)
		}
	}

	// Replace with the alternative chain.
	replaced := l.ReplaceChain(altBC)
	if !replaced {
		t.Fatal("ReplaceChain should accept alternative chain with more work")
	}

	// After reorg, receiver should have 0 balance (the tx was on the old chain).
	if l.GetBalance(recvKP.Address) != 0 {
		t.Errorf("receiver balance after reorg = %f, want 0", l.GetBalance(recvKP.Address))
	}

	// Genesis owner should have the full genesis supply again.
	if l.GetBalance(kp.Address) != block.GenesisSupply {
		t.Errorf("genesis owner balance after reorg = %f, want %f", l.GetBalance(kp.Address), block.GenesisSupply)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CRITICAL-4: Orphan block connects after parent arrives
// ──────────────────────────────────────────────────────────────────────────────

func TestOrphanBlockConnectsAfterParent(t *testing.T) {
	idx := chain.NewBlockIndex()

	// Build a chain: genesis -> block1 -> block2
	genesis := block.NewGenesisBlock()
	idx.AddBlock(genesis)

	easyTarget := new(big.Int)
	easyTarget.SetString("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", 16)

	// Block 1
	tx1 := block.NewCoinbaseTx("miner", 50, 1)
	merkle1 := block.ComputeMerkleRoot([]string{tx1.ID})
	b1 := &block.Block{
		Header: block.BlockHeader{
			Version:       block.BlockVersion,
			Height:        1,
			PrevBlockHash: genesis.Hash,
			MerkleRoot:    merkle1,
			Timestamp:     genesis.Header.Timestamp + 600,
		},
		Transactions: []block.Transaction{tx1},
	}
	block.MineBlock(b1, easyTarget, 1000)

	// Block 2 (depends on block 1).
	tx2 := block.NewCoinbaseTx("miner", 50, 2)
	merkle2 := block.ComputeMerkleRoot([]string{tx2.ID})
	b2 := &block.Block{
		Header: block.BlockHeader{
			Version:       block.BlockVersion,
			Height:        2,
			PrevBlockHash: b1.Hash,
			MerkleRoot:    merkle2,
			Timestamp:     b1.Header.Timestamp + 600,
		},
		Transactions: []block.Transaction{tx2},
	}
	block.MineBlock(b2, easyTarget, 1000)

	// Submit block 2 first (orphan — parent b1 not yet known).
	result2 := idx.AddBlock(b2)
	if !result2.Added {
		t.Fatal("block 2 should be added")
	}
	if !result2.IsOrphan {
		t.Fatal("block 2 should be an orphan (parent not known)")
	}
	if idx.OrphanCount() != 1 {
		t.Errorf("orphan count = %d, want 1", idx.OrphanCount())
	}

	// Now submit block 1 (parent of orphan block 2).
	result1 := idx.AddBlock(b1)
	if !result1.Added {
		t.Fatal("block 1 should be added")
	}
	if result1.IsOrphan {
		t.Fatal("block 1 should NOT be an orphan (parent is genesis)")
	}

	// Block 2 should have been connected as an orphan.
	if len(result1.ConnectedOrphans) != 1 {
		t.Errorf("connected orphans = %d, want 1", len(result1.ConnectedOrphans))
	}
	if idx.OrphanCount() != 0 {
		t.Errorf("orphan count after connect = %d, want 0", idx.OrphanCount())
	}

	// Best tip should be block 2.
	bestTip := idx.BestTip()
	if bestTip == nil {
		t.Fatal("best tip should not be nil")
	}
	if bestTip.Hash != b2.Hash {
		t.Errorf("best tip = %s, want block 2 hash", bestTip.Hash[:16])
	}
	if bestTip.Height != 2 {
		t.Errorf("best tip height = %d, want 2", bestTip.Height)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CRITICAL-4: Block index fork point detection
// ──────────────────────────────────────────────────────────────────────────────

func TestBlockIndexForkPoint(t *testing.T) {
	idx := chain.NewBlockIndex()

	easyTarget := new(big.Int)
	easyTarget.SetString("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", 16)

	// Genesis
	genesis := block.NewGenesisBlock()
	idx.AddBlock(genesis)

	// Main chain: genesis -> b1 -> b2
	tx1 := block.NewCoinbaseTx("minerA", 50, 1)
	b1 := &block.Block{
		Header: block.BlockHeader{
			Version: block.BlockVersion, Height: 1,
			PrevBlockHash: genesis.Hash,
			MerkleRoot:    block.ComputeMerkleRoot([]string{tx1.ID}),
			Timestamp:     genesis.Header.Timestamp + 600,
		},
		Transactions: []block.Transaction{tx1},
	}
	block.MineBlock(b1, easyTarget, 1000)
	idx.AddBlock(b1)

	tx2 := block.NewCoinbaseTx("minerA", 50, 2)
	b2 := &block.Block{
		Header: block.BlockHeader{
			Version: block.BlockVersion, Height: 2,
			PrevBlockHash: b1.Hash,
			MerkleRoot:    block.ComputeMerkleRoot([]string{tx2.ID}),
			Timestamp:     b1.Header.Timestamp + 600,
		},
		Transactions: []block.Transaction{tx2},
	}
	block.MineBlock(b2, easyTarget, 1000)
	idx.AddBlock(b2)

	// Fork: genesis -> b1 -> b2alt
	tx2alt := block.NewCoinbaseTx("minerB", 50, 2)
	b2alt := &block.Block{
		Header: block.BlockHeader{
			Version: block.BlockVersion, Height: 2,
			PrevBlockHash: b1.Hash,
			MerkleRoot:    block.ComputeMerkleRoot([]string{tx2alt.ID}),
			Timestamp:     b1.Header.Timestamp + 601, // slightly different timestamp
		},
		Transactions: []block.Transaction{tx2alt},
	}
	block.MineBlock(b2alt, easyTarget, 1000)
	idx.AddBlock(b2alt)

	// Find fork point between b2 and b2alt.
	forkPoint, disconnect, connect, err := idx.FindForkPoint(b2.Hash, b2alt.Hash)
	if err != nil {
		t.Fatalf("FindForkPoint() error: %v", err)
	}

	if forkPoint == nil {
		t.Fatal("fork point should not be nil")
	}
	if forkPoint.Hash != b1.Hash {
		t.Errorf("fork point = %s, want b1 hash", forkPoint.Hash[:16])
	}
	if len(disconnect) != 1 {
		t.Errorf("disconnect count = %d, want 1", len(disconnect))
	}
	if len(connect) != 1 {
		t.Errorf("connect count = %d, want 1", len(connect))
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CRITICAL-4: WorkForTarget calculation
// ──────────────────────────────────────────────────────────────────────────────

func TestWorkForTarget(t *testing.T) {
	// Harder target (smaller number) should produce more work.
	hardTarget := new(big.Int)
	hardTarget.SetString("00ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", 16)

	easyTarget := new(big.Int)
	easyTarget.SetString("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", 16)

	hardWork := block.WorkForTarget(hardTarget)
	easyWork := block.WorkForTarget(easyTarget)

	if hardWork.Cmp(easyWork) <= 0 {
		t.Errorf("hard target work (%s) should be > easy target work (%s)",
			hardWork.String(), easyWork.String())
	}

	// Zero target should give work = 1 (not panic).
	zeroWork := block.WorkForTarget(big.NewInt(0))
	if zeroWork.Cmp(big.NewInt(1)) < 0 {
		t.Errorf("zero target work = %s, want >= 1", zeroWork.String())
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CRITICAL-4: UTXO Rollback
// ──────────────────────────────────────────────────────────────────────────────

func TestUTXORollback(t *testing.T) {
	genesis := block.NewGenesisBlock()
	blocks := []*block.Block{genesis}

	// Build UTXO from genesis.
	utxoSet, err := utxo.RebuildFromBlocks(blocks)
	if err != nil {
		t.Fatalf("RebuildFromBlocks() error: %v", err)
	}

	// Build a block with a coinbase.
	tx := block.NewCoinbaseTx("miner", 50, 1)
	easyTarget := new(big.Int)
	easyTarget.SetString("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", 16)
	b := &block.Block{
		Header: block.BlockHeader{
			Version: block.BlockVersion, Height: 1,
			PrevBlockHash: genesis.Hash,
			MerkleRoot:    block.ComputeMerkleRoot([]string{tx.ID}),
			Timestamp:     genesis.Header.Timestamp + 600,
		},
		Transactions: []block.Transaction{tx},
	}
	block.MineBlock(b, easyTarget, 1000)

	// Snapshot, apply, then rollback.
	inputSnap := utxoSet.SnapshotInputs(b)
	if err := utxoSet.ApplyBlock(b); err != nil {
		t.Fatalf("ApplyBlock() error: %v", err)
	}

	// After apply: miner should have 50.
	if utxoSet.Balance("miner") != 50 {
		t.Errorf("miner balance after apply = %f, want 50", utxoSet.Balance("miner"))
	}

	// Rollback.
	if err := utxoSet.RollbackBlock(b, inputSnap); err != nil {
		t.Fatalf("RollbackBlock() error: %v", err)
	}

	// After rollback: miner should have 0.
	if utxoSet.Balance("miner") != 0 {
		t.Errorf("miner balance after rollback = %f, want 0", utxoSet.Balance("miner"))
	}

	// Genesis balance should be restored.
	genesisBalance := utxoSet.Balance(block.LegacyGenesisAddress)
	if genesisBalance != block.GenesisSupply {
		t.Errorf("genesis balance after rollback = %f, want %f", genesisBalance, block.GenesisSupply)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// HIGH-1: Blockstore persistence — restart recovers full state
// ──────────────────────────────────────────────────────────────────────────────

func TestBlockstoreRestartPersistence(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privHex := hex.EncodeToString(kp.PrivateKey)
	minerKP, _ := crypto.GenerateKeyPair()

	// Use a persistent path so we can "restart".
	path := tmpFile(t)

	// Session 1: create ledger, send faucet, mine a block.
	l1 := ledger.NewLedgerWithOwner(path, kp.Address)
	if err := l1.SetFaucetKeyAndValidateGenesis(privHex); err != nil {
		t.Fatalf("SetFaucetKeyAndValidateGenesis: %v", err)
	}

	recvKP, _ := crypto.GenerateKeyPair()
	_, err = l1.ProcessFaucet(recvKP.Address)
	if err != nil {
		t.Fatalf("ProcessFaucet: %v", err)
	}

	// Mine faucet tx.
	cfg := miner.Config{
		Enabled:      true,
		MinerAddress: minerKP.Address,
		BlockMaxTx:   100,
		Interval:     50 * time.Millisecond,
		MaxAttempts:  10_000_000,
	}
	mn := miner.New(cfg, l1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	go mn.Run(ctx)
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			cancel()
			t.Fatal("miner did not process faucet tx")
		default:
			if l1.GetMempoolSize() == 0 && l1.GetChainHeight() > 0 {
				goto mined
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
mined:
	cancel()

	heightBefore := l1.GetChainHeight()
	recvBalanceBefore := l1.GetBalance(recvKP.Address)
	minerBalanceBefore := l1.GetBalance(minerKP.Address)
	totalMinedBefore := l1.GetChain().TotalMined
	totalFaucetBefore := l1.GetChain().TotalFaucet

	if heightBefore < 1 {
		t.Fatal("should have at least height 1 after mining")
	}
	if recvBalanceBefore == 0 {
		t.Fatal("receiver should have balance after mining")
	}

	// Save explicitly.
	if err := l1.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Session 2: "restart" — reload from same path.
	l2 := ledger.LoadLedgerWithOwner(path, kp.Address)

	if l2.GetChainHeight() != heightBefore {
		t.Errorf("height after restart: got %d, want %d", l2.GetChainHeight(), heightBefore)
	}
	if l2.GetBalance(recvKP.Address) != recvBalanceBefore {
		t.Errorf("receiver balance after restart: got %f, want %f",
			l2.GetBalance(recvKP.Address), recvBalanceBefore)
	}
	if l2.GetBalance(minerKP.Address) != minerBalanceBefore {
		t.Errorf("miner balance after restart: got %f, want %f",
			l2.GetBalance(minerKP.Address), minerBalanceBefore)
	}
	if l2.GetChain().TotalMined != totalMinedBefore {
		t.Errorf("TotalMined after restart: got %f, want %f",
			l2.GetChain().TotalMined, totalMinedBefore)
	}
	if l2.GetChain().TotalFaucet != totalFaucetBefore {
		t.Errorf("TotalFaucet after restart: got %f, want %f",
			l2.GetChain().TotalFaucet, totalFaucetBefore)
	}
	if l2.GenesisOwner() != kp.Address {
		t.Errorf("GenesisOwner after restart: got %s, want %s",
			l2.GenesisOwner(), kp.Address)
	}
	if l2.GetStore() == nil {
		t.Error("Store should not be nil after restart")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// HIGH-1: Rebuild UTXO from blockstore when chainstate is corrupted
// ──────────────────────────────────────────────────────────────────────────────

func TestBlockstoreRebuildUTXOFromBlocks(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privHex := hex.EncodeToString(kp.PrivateKey)
	minerKP, _ := crypto.GenerateKeyPair()

	path := tmpFile(t)

	// Session 1: create chain with some activity.
	l1 := ledger.NewLedgerWithOwner(path, kp.Address)
	if err := l1.SetFaucetKeyAndValidateGenesis(privHex); err != nil {
		t.Fatal(err)
	}

	recvKP, _ := crypto.GenerateKeyPair()
	_, err = l1.ProcessFaucet(recvKP.Address)
	if err != nil {
		t.Fatal(err)
	}

	cfg := miner.Config{
		Enabled: true, MinerAddress: minerKP.Address,
		BlockMaxTx: 100, Interval: 50 * time.Millisecond, MaxAttempts: 10_000_000,
	}
	mn := miner.New(cfg, l1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	go mn.Run(ctx)
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			cancel()
			t.Fatal("miner timeout")
		default:
			if l1.GetMempoolSize() == 0 && l1.GetChainHeight() > 0 {
				goto mined2
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
mined2:
	cancel()
	l1.Save()

	recvBalance := l1.GetBalance(recvKP.Address)
	minerBalance := l1.GetBalance(minerKP.Address)

	// Corrupt the chainstate by deleting the UTXO file.
	st := l1.GetStore()
	if st == nil {
		t.Fatal("store should not be nil")
	}
	chainstateDir := filepath.Join(st.DataDir(), "chainstate")
	os.Remove(filepath.Join(chainstateDir, "utxo.json"))

	// Session 2: reload — should rebuild UTXO from blockstore.
	l2 := ledger.LoadLedgerWithOwner(path, kp.Address)

	if l2.GetBalance(recvKP.Address) != recvBalance {
		t.Errorf("receiver balance after rebuild: got %f, want %f",
			l2.GetBalance(recvKP.Address), recvBalance)
	}
	if l2.GetBalance(minerKP.Address) != minerBalance {
		t.Errorf("miner balance after rebuild: got %f, want %f",
			l2.GetBalance(minerKP.Address), minerBalance)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// HIGH-1: Recovery after partial write (temp file cleanup)
// ──────────────────────────────────────────────────────────────────────────────

func TestBlockstoreRecoveryAfterPartialWrite(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	path := tmpFile(t)

	// Create ledger and save.
	l1 := ledger.NewLedgerWithOwner(path, kp.Address)
	l1.Save()

	// Simulate a partial write by creating a .tmp file in the store.
	st := l1.GetStore()
	if st == nil {
		t.Fatal("store should not be nil")
	}
	tmpPath := filepath.Join(st.DataDir(), "metadata.json.tmp")
	os.WriteFile(tmpPath, []byte("incomplete"), 0644)

	// Reload — the store should clean up the temp file.
	l2 := ledger.LoadLedgerWithOwner(path, kp.Address)
	if l2 == nil {
		t.Fatal("LoadLedgerWithOwner returned nil")
	}

	// Temp file should be gone.
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Error("temp file should have been cleaned up during recovery")
	}

	// Data should be intact.
	if l2.GetChainHeight() != 0 {
		t.Errorf("height = %d, want 0", l2.GetChainHeight())
	}
	if l2.GenesisOwner() != kp.Address {
		t.Errorf("genesis owner = %s, want %s", l2.GenesisOwner(), kp.Address)
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// HIGH-3: FINAL LAUNCH GATE — Integration, Invariant, and Property Tests
// ══════════════════════════════════════════════════════════════════════════════

// ──────────────────────────────────────────────────────────────────────────────
// Invariant: sum(UTXO) == genesis + mined
// ──────────────────────────────────────────────────────────────────────────────

// computeUTXOSum returns the total value of all unspent outputs in the UTXO set.
func computeUTXOSum(s *utxo.Set) float64 {
	all := s.GetAllUTXOs()
	var total float64
	for _, u := range all {
		total += u.Output.Amount
	}
	return total
}

func TestInvariant_UTXOSumEqualsSupply(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privHex := hex.EncodeToString(kp.PrivateKey)
	minerKP, _ := crypto.GenerateKeyPair()

	l := ledger.NewLedgerWithOwner(tmpFile(t), kp.Address)

	// After genesis, UTXO sum should equal genesis supply.
	sum := computeUTXOSum(l.UTXOSet)
	if sum != block.GenesisSupply {
		t.Fatalf("UTXO sum after genesis = %f, want %f", sum, block.GenesisSupply)
	}

	// Send a transaction and mine it.
	recvKP, _ := crypto.GenerateKeyPair()
	tx, err := l.BuildTransaction(privHex, kp.Address, recvKP.Address, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.SubmitTransaction(*tx); err != nil {
		t.Fatal(err)
	}

	// Mine the tx.
	cfg := miner.Config{
		Enabled: true, MinerAddress: minerKP.Address,
		BlockMaxTx: 100, Interval: 50 * time.Millisecond, MaxAttempts: 10_000_000,
	}
	mn := miner.New(cfg, l)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go mn.Run(ctx)
	waitMempoolEmpty(t, l, 5*time.Second)
	cancel()

	// After mining: UTXO sum should equal genesis + mined.
	sum = computeUTXOSum(l.UTXOSet)
	expectedTotal := block.GenesisSupply + l.GetChain().TotalMined
	if !almostEqual(sum, expectedTotal, 0.0001) {
		t.Errorf("UTXO sum = %f, expected (genesis+mined) = %f", sum, expectedTotal)
	}
}

func TestInvariant_TotalSupplyNeverExceeds21M(t *testing.T) {
	// Verify that genesis + max mining never exceeds 21M.
	if block.GenesisSupply+block.MaxMiningSupply > block.MaxTotalSupply {
		t.Fatalf("GenesisSupply(%f) + MaxMiningSupply(%f) > MaxTotalSupply(%f)",
			block.GenesisSupply, block.MaxMiningSupply, block.MaxTotalSupply)
	}

	// Simulate mining rewards up to the cap and verify total never exceeds 21M.
	totalMined := 0.0
	for h := uint64(1); h < 10*block.HalvingInterval; h++ {
		reward := block.BlockReward(h, totalMined)
		if reward == 0 {
			break
		}
		totalMined += reward
		total := block.GenesisSupply + totalMined
		if total > block.MaxTotalSupply+0.0001 {
			t.Fatalf("Total supply at height %d = %f, exceeds %f",
				h, total, block.MaxTotalSupply)
		}
	}

	// Final total mined should not exceed MaxMiningSupply.
	if totalMined > block.MaxMiningSupply+0.0001 {
		t.Errorf("Total mined = %f, exceeds MaxMiningSupply(%f)", totalMined, block.MaxMiningSupply)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Invariant: faucet never distributes above cap
// ──────────────────────────────────────────────────────────────────────────────

func TestInvariant_FaucetNeverExceedsCap(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privHex := hex.EncodeToString(kp.PrivateKey)
	minerKP, _ := crypto.GenerateKeyPair()

	l := ledger.NewLedgerWithOwner(tmpFile(t), kp.Address)
	if err := l.SetFaucetKeyAndValidateGenesis(privHex); err != nil {
		t.Fatal(err)
	}

	// Distribute faucet coins to many recipients.
	const numClaims = 5
	for i := 0; i < numClaims; i++ {
		recvKP, _ := crypto.GenerateKeyPair()
		_, err := l.ProcessFaucet(recvKP.Address)
		if err != nil {
			// May fail near the end if we run out — that's fine.
			break
		}

		// Mine the faucet tx.
		cfg := miner.Config{
			Enabled: true, MinerAddress: minerKP.Address,
			BlockMaxTx: 100, Interval: 50 * time.Millisecond, MaxAttempts: 10_000_000,
		}
		mn := miner.New(cfg, l)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		go mn.Run(ctx)
		waitMempoolEmpty(t, l, 5*time.Second)
		cancel()

		// Check invariant.
		if l.FaucetTotalDistributed() > ledger.FaucetGlobalCap {
			t.Fatalf("Faucet total distributed = %f, exceeds cap %f",
				l.FaucetTotalDistributed(), ledger.FaucetGlobalCap)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Invariant: no double-spend in main chain
// ──────────────────────────────────────────────────────────────────────────────

func TestInvariant_NoDoubleSpendAcrossMainChain(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privHex := hex.EncodeToString(kp.PrivateKey)
	minerKP, _ := crypto.GenerateKeyPair()

	l := ledger.NewLedgerWithOwner(tmpFile(t), kp.Address)

	// Make several transactions.
	for i := 0; i < 3; i++ {
		recvKP, _ := crypto.GenerateKeyPair()
		tx, err := l.BuildTransaction(privHex, kp.Address, recvKP.Address, 100)
		if err != nil {
			t.Fatalf("BuildTransaction(%d): %v", i, err)
		}
		if err := l.SubmitTransaction(*tx); err != nil {
			t.Fatalf("SubmitTransaction(%d): %v", i, err)
		}

		// Mine each tx.
		cfg := miner.Config{
			Enabled: true, MinerAddress: minerKP.Address,
			BlockMaxTx: 100, Interval: 50 * time.Millisecond, MaxAttempts: 10_000_000,
		}
		mn := miner.New(cfg, l)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		go mn.Run(ctx)
		waitMempoolEmpty(t, l, 5*time.Second)
		cancel()
	}

	// Scan entire chain for double-spends.
	spentOutpoints := make(map[string]uint64) // outpoint -> block height
	bc := l.GetChain()
	for _, b := range bc.Blocks {
		for _, tx := range b.Transactions {
			if tx.IsCoinbase() || tx.IsGenesis() {
				continue
			}
			for _, in_ := range tx.Inputs {
				key := fmt.Sprintf("%s:%d", in_.PrevTxID, in_.PrevIndex)
				if prevHeight, exists := spentOutpoints[key]; exists {
					t.Fatalf("Double-spend detected! Outpoint %s spent in block %d and block %d",
						key, prevHeight, b.Header.Height)
				}
				spentOutpoints[key] = b.Header.Height
			}
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// End-to-End: Full tx lifecycle: build → sign → mempool → mine → confirm
// ──────────────────────────────────────────────────────────────────────────────

func TestFullTxLifecycle(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privHex := hex.EncodeToString(kp.PrivateKey)
	minerKP, _ := crypto.GenerateKeyPair()
	recvKP, _ := crypto.GenerateKeyPair()

	l := ledger.NewLedgerWithOwner(tmpFile(t), kp.Address)

	// 1. Build transaction.
	amount := 500.0
	tx, err := l.BuildTransaction(privHex, kp.Address, recvKP.Address, amount)
	if err != nil {
		t.Fatalf("BuildTransaction: %v", err)
	}

	// 2. Verify it's signed.
	if tx.Inputs[0].Signature == "" {
		t.Fatal("transaction should be signed")
	}

	// 3. Validate.
	if err := l.ValidateUserTx(*tx); err != nil {
		t.Fatalf("ValidateUserTx: %v", err)
	}

	// 4. Submit to mempool.
	if err := l.SubmitTransaction(*tx); err != nil {
		t.Fatalf("SubmitTransaction: %v", err)
	}
	if l.GetMempoolSize() != 1 {
		t.Fatalf("mempool size = %d, want 1", l.GetMempoolSize())
	}

	// 5. Balance should NOT have changed yet.
	if l.GetBalance(recvKP.Address) != 0 {
		t.Errorf("receiver balance before mining = %f, want 0", l.GetBalance(recvKP.Address))
	}

	// 6. Mine.
	cfg := miner.Config{
		Enabled: true, MinerAddress: minerKP.Address,
		BlockMaxTx: 100, Interval: 50 * time.Millisecond, MaxAttempts: 10_000_000,
	}
	mn := miner.New(cfg, l)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go mn.Run(ctx)
	waitMempoolEmpty(t, l, 5*time.Second)
	cancel()

	// 7. Confirm: mempool empty, balance updated.
	if l.GetMempoolSize() != 0 {
		t.Errorf("mempool size after mining = %d, want 0", l.GetMempoolSize())
	}
	if l.GetBalance(recvKP.Address) != amount {
		t.Errorf("receiver balance after mining = %f, want %f", l.GetBalance(recvKP.Address), amount)
	}

	// 8. Verify UTXO sum invariant.
	sum := computeUTXOSum(l.UTXOSet)
	expectedTotal := block.GenesisSupply + l.GetChain().TotalMined
	if !almostEqual(sum, expectedTotal, 0.0001) {
		t.Errorf("UTXO sum = %f, expected = %f", sum, expectedTotal)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Mining rewards never exceed 10M cap
// ──────────────────────────────────────────────────────────────────────────────

func TestMiningRewardsDoNotExceedCap(t *testing.T) {
	// Simulate mining many blocks and verify total never exceeds 10M.
	totalMined := 0.0
	prevReward := block.InitialBlockReward

	for h := uint64(1); h < 5*block.HalvingInterval; h++ {
		reward := block.BlockReward(h, totalMined)
		if reward == 0 {
			break
		}
		totalMined += reward

		// Reward should never be negative.
		if reward < 0 {
			t.Fatalf("Negative reward %f at height %d", reward, h)
		}

		// Reward should be <= previous reward (halving).
		if reward > prevReward+0.0001 {
			t.Errorf("Reward at height %d (%f) > previous (%f)", h, reward, prevReward)
		}
		prevReward = reward

		// Total mined should never exceed cap.
		if totalMined > block.MaxMiningSupply+0.0001 {
			t.Fatalf("Total mined %f exceeds cap %f at height %d",
				totalMined, block.MaxMiningSupply, h)
		}
	}

	t.Logf("Mining simulation: total mined = %f (cap: %f)", totalMined, block.MaxMiningSupply)
}

// ──────────────────────────────────────────────────────────────────────────────
// Restart persistence: full round-trip with multiple blocks and transactions
// ──────────────────────────────────────────────────────────────────────────────

func TestRestartPersistenceFullRoundTrip(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privHex := hex.EncodeToString(kp.PrivateKey)
	minerKP, _ := crypto.GenerateKeyPair()
	path := tmpFile(t)

	// Session 1: Create ledger, send multiple txs, mine multiple blocks.
	l1 := ledger.NewLedgerWithOwner(path, kp.Address)
	if err := l1.SetFaucetKeyAndValidateGenesis(privHex); err != nil {
		t.Fatal(err)
	}

	// Faucet + manual send.
	recv1, _ := crypto.GenerateKeyPair()
	_, err = l1.ProcessFaucet(recv1.Address)
	if err != nil {
		t.Fatal(err)
	}

	// Mine faucet tx.
	cfg := miner.Config{
		Enabled: true, MinerAddress: minerKP.Address,
		BlockMaxTx: 100, Interval: 50 * time.Millisecond, MaxAttempts: 10_000_000,
	}
	mn := miner.New(cfg, l1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	go mn.Run(ctx)
	waitMempoolEmpty(t, l1, 5*time.Second)
	cancel()

	// Another tx.
	recv2, _ := crypto.GenerateKeyPair()
	tx, err := l1.BuildTransaction(privHex, kp.Address, recv2.Address, 200)
	if err != nil {
		t.Fatal(err)
	}
	if err := l1.SubmitTransaction(*tx); err != nil {
		t.Fatal(err)
	}

	mn2 := miner.New(cfg, l1)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	go mn2.Run(ctx2)
	waitMempoolEmpty(t, l1, 5*time.Second)
	cancel2()

	// Record state.
	heightBefore := l1.GetChainHeight()
	utxoSumBefore := computeUTXOSum(l1.UTXOSet)
	recv1BalBefore := l1.GetBalance(recv1.Address)
	recv2BalBefore := l1.GetBalance(recv2.Address)
	totalMinedBefore := l1.GetChain().TotalMined
	totalFaucetBefore := l1.GetChain().TotalFaucet

	l1.Save()

	// Session 2: reload.
	l2 := ledger.LoadLedgerWithOwner(path, kp.Address)

	if l2.GetChainHeight() != heightBefore {
		t.Errorf("height: got %d, want %d", l2.GetChainHeight(), heightBefore)
	}
	if l2.GetBalance(recv1.Address) != recv1BalBefore {
		t.Errorf("recv1 balance: got %f, want %f", l2.GetBalance(recv1.Address), recv1BalBefore)
	}
	if l2.GetBalance(recv2.Address) != recv2BalBefore {
		t.Errorf("recv2 balance: got %f, want %f", l2.GetBalance(recv2.Address), recv2BalBefore)
	}
	if l2.GetChain().TotalMined != totalMinedBefore {
		t.Errorf("TotalMined: got %f, want %f", l2.GetChain().TotalMined, totalMinedBefore)
	}
	if l2.GetChain().TotalFaucet != totalFaucetBefore {
		t.Errorf("TotalFaucet: got %f, want %f", l2.GetChain().TotalFaucet, totalFaucetBefore)
	}
	utxoSumAfter := computeUTXOSum(l2.UTXOSet)
	if !almostEqual(utxoSumAfter, utxoSumBefore, 0.0001) {
		t.Errorf("UTXO sum: got %f, want %f", utxoSumAfter, utxoSumBefore)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Competing branches + reorg — verify UTXO is consistent after reorg
// ──────────────────────────────────────────────────────────────────────────────

func TestCompetingBranchesReorgUTXOConsistency(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	minerKP, _ := crypto.GenerateKeyPair()

	l := ledger.NewLedgerWithOwner(tmpFile(t), kp.Address)

	// Build the current chain by mining a coinbase block.
	easyTarget := new(big.Int)
	easyTarget.SetString("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", 16)
	hardTarget := new(big.Int)
	hardTarget.SetString("00ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", 16)

	// Mine 2 easy blocks onto the ledger.
	for i := uint64(1); i <= 2; i++ {
		cb := block.NewCoinbaseTx(minerKP.Address, 50, i)
		merkle := block.ComputeMerkleRoot([]string{cb.ID})
		b := &block.Block{
			Header: block.BlockHeader{
				Version: block.BlockVersion, Height: i,
				PrevBlockHash: l.GetChain().LastHash(),
				MerkleRoot:    merkle,
				Timestamp:     l.GetChain().LastBlock().Header.Timestamp + 600,
			},
			Transactions: []block.Transaction{cb},
		}
		block.MineBlock(b, easyTarget, 1000)
		if err := l.ApplyMinedBlock(b, nil); err != nil {
			t.Fatal(err)
		}
	}

	// Record pre-reorg UTXO sum.
	preReorgSum := computeUTXOSum(l.UTXOSet)
	if l.GetChainHeight() != 2 {
		t.Fatalf("height = %d, want 2", l.GetChainHeight())
	}

	// Build alternative chain from genesis with MORE cumulative work.
	altBC := chain.NewBlockchainWithOwner(kp.Address)
	altMiner, _ := crypto.GenerateKeyPair()
	for i := uint64(1); i <= 3; i++ {
		cb := block.NewCoinbaseTx(altMiner.Address, 50, i)
		merkle := block.ComputeMerkleRoot([]string{cb.ID})
		b := &block.Block{
			Header: block.BlockHeader{
				Version: block.BlockVersion, Height: i,
				PrevBlockHash: altBC.LastHash(),
				MerkleRoot:    merkle,
				Timestamp:     altBC.LastBlock().Header.Timestamp + 600,
			},
			Transactions: []block.Transaction{cb},
		}
		if err := block.MineBlock(b, hardTarget, 100_000_000); err != nil {
			t.Fatalf("alt chain mine %d: %v", i, err)
		}
		if err := altBC.AddBlock(b); err != nil {
			t.Fatalf("alt chain add %d: %v", i, err)
		}
	}

	// Replace with alternative chain.
	replaced := l.ReplaceChain(altBC)
	if !replaced {
		t.Fatal("should accept alternative chain with more work")
	}

	// Verify UTXO sum after reorg.
	postReorgSum := computeUTXOSum(l.UTXOSet)
	expectedTotal := block.GenesisSupply + l.GetChain().TotalMined
	if !almostEqual(postReorgSum, expectedTotal, 0.0001) {
		t.Errorf("post-reorg UTXO sum = %f, expected = %f (genesis+mined=%f+%f)",
			postReorgSum, expectedTotal, block.GenesisSupply, l.GetChain().TotalMined)
	}

	// Pre-reorg miner should have 0 balance (their blocks were disconnected).
	if l.GetBalance(minerKP.Address) != 0 {
		t.Errorf("old miner balance after reorg = %f, want 0", l.GetBalance(minerKP.Address))
	}

	// Alt miner should have rewards.
	if l.GetBalance(altMiner.Address) <= 0 {
		t.Error("alt miner should have rewards after reorg")
	}

	_ = preReorgSum // used for reference
}

// ──────────────────────────────────────────────────────────────────────────────
// Multi-node sync after divergence (simulated)
// ──────────────────────────────────────────────────────────────────────────────

func TestMultiNodeSyncAfterDivergence(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	minerA, _ := crypto.GenerateKeyPair()
	minerB, _ := crypto.GenerateKeyPair()

	hardTarget := new(big.Int)
	hardTarget.SetString("00ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", 16)

	// Node A: genesis + 2 blocks.
	lA := ledger.NewLedgerWithOwner(tmpFile(t), kp.Address)
	for i := uint64(1); i <= 2; i++ {
		cb := block.NewCoinbaseTx(minerA.Address, 50, i)
		merkle := block.ComputeMerkleRoot([]string{cb.ID})
		b := &block.Block{
			Header: block.BlockHeader{
				Version: block.BlockVersion, Height: i,
				PrevBlockHash: lA.GetChain().LastHash(),
				MerkleRoot:    merkle,
				Timestamp:     lA.GetChain().LastBlock().Header.Timestamp + 600,
			},
			Transactions: []block.Transaction{cb},
		}
		if err := block.MineBlock(b, hardTarget, 100_000_000); err != nil {
			t.Fatal(err)
		}
		if err := lA.ApplyMinedBlock(b, nil); err != nil {
			t.Fatal(err)
		}
	}

	// Node B: genesis + 3 blocks (longer AND more work).
	bcB := chain.NewBlockchainWithOwner(kp.Address)
	for i := uint64(1); i <= 3; i++ {
		cb := block.NewCoinbaseTx(minerB.Address, 50, i)
		merkle := block.ComputeMerkleRoot([]string{cb.ID})
		b := &block.Block{
			Header: block.BlockHeader{
				Version: block.BlockVersion, Height: i,
				PrevBlockHash: bcB.LastHash(),
				MerkleRoot:    merkle,
				Timestamp:     bcB.LastBlock().Header.Timestamp + 600,
			},
			Transactions: []block.Transaction{cb},
		}
		if err := block.MineBlock(b, hardTarget, 100_000_000); err != nil {
			t.Fatal(err)
		}
		if err := bcB.AddBlock(b); err != nil {
			t.Fatal(err)
		}
	}

	// Simulate Node A syncing from Node B.
	workA := lA.GetChain().CumulativeWork()
	workB := bcB.CumulativeWork()
	if workB.Cmp(workA) <= 0 {
		t.Fatal("Node B should have more work")
	}

	replaced := lA.ReplaceChain(bcB)
	if !replaced {
		t.Fatal("Node A should accept Node B's chain")
	}

	// After sync, Node A should have Node B's state.
	if lA.GetChainHeight() != bcB.Height() {
		t.Errorf("height after sync: got %d, want %d", lA.GetChainHeight(), bcB.Height())
	}

	// Miner A should have 0 (their blocks were replaced).
	if lA.GetBalance(minerA.Address) != 0 {
		t.Errorf("miner A balance after sync = %f, want 0", lA.GetBalance(minerA.Address))
	}

	// Miner B should have rewards.
	if lA.GetBalance(minerB.Address) <= 0 {
		t.Error("miner B should have rewards after sync")
	}

	// UTXO invariant.
	sum := computeUTXOSum(lA.UTXOSet)
	expectedTotal := block.GenesisSupply + lA.GetChain().TotalMined
	if !almostEqual(sum, expectedTotal, 0.0001) {
		t.Errorf("UTXO sum after sync = %f, expected = %f", sum, expectedTotal)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Mining rewards cap when approaching 10M limit
// ──────────────────────────────────────────────────────────────────────────────

func TestMiningRewardClampedAtCap(t *testing.T) {
	// If totalMined is very close to MaxMiningSupply, reward should be clamped.
	almostFull := block.MaxMiningSupply - 10.0 // only 10 coins left
	reward := block.BlockReward(1, almostFull)
	if reward > 10.0+0.0001 {
		t.Errorf("reward near cap = %f, should be <= 10.0", reward)
	}

	// At exactly the cap, reward should be 0.
	atCap := block.MaxMiningSupply
	reward = block.BlockReward(1, atCap)
	if reward != 0 {
		t.Errorf("reward at cap = %f, want 0", reward)
	}

	// Past the cap, reward should be 0.
	pastCap := block.MaxMiningSupply + 1.0
	reward = block.BlockReward(1, pastCap)
	if reward != 0 {
		t.Errorf("reward past cap = %f, want 0", reward)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Faucet bootstrap and exhaust simulation
// ──────────────────────────────────────────────────────────────────────────────

func TestFaucetBootstrapAndExhaustSimulation(t *testing.T) {
	// We can't distribute all 1M in a test (10000 claims × mining), so we verify:
	// 1. FaucetGlobalCap == GenesisSupply == 1_000_000
	// 2. Each claim is exactly FaucetAmount (100)
	// 3. Faucet correctly rejects when exhausted

	if ledger.FaucetGlobalCap != block.GenesisSupply {
		t.Fatalf("FaucetGlobalCap(%f) != GenesisSupply(%f)", ledger.FaucetGlobalCap, block.GenesisSupply)
	}

	maxClaims := int(ledger.FaucetGlobalCap / ledger.FaucetAmount)
	expectedMaxClaims := 10000 // 1_000_000 / 100
	if maxClaims != expectedMaxClaims {
		t.Errorf("max faucet claims = %d, want %d", maxClaims, expectedMaxClaims)
	}

	// Verify the faucet function rejects when manually set to exhausted.
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privHex := hex.EncodeToString(kp.PrivateKey)

	l := ledger.NewLedgerWithOwner(tmpFile(t), kp.Address)
	if err := l.SetFaucetKeyAndValidateGenesis(privHex); err != nil {
		t.Fatal(err)
	}

	// Manually set TotalFaucet to the cap.
	l.GetChain().TotalFaucet = ledger.FaucetGlobalCap

	// Attempt to claim.
	recvKP, _ := crypto.GenerateKeyPair()
	_, err = l.ProcessFaucet(recvKP.Address)
	if err == nil {
		t.Fatal("faucet should reject when exhausted")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

func waitMempoolEmpty(t *testing.T, l *ledger.Ledger, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			t.Fatalf("mempool not empty after %v (size=%d)", timeout, l.GetMempoolSize())
		default:
			if l.GetMempoolSize() == 0 {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func almostEqual(a, b, epsilon float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < epsilon
}

// Suppress unused import warnings.
var _ = time.Now
var _ = os.Remove
