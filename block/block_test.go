package block

import (
	"fmt"
	"math/big"
	"testing"
	"time"
)

// ──────────────────────────────────────────────────────────────────────────────
// Constants
// ──────────────────────────────────────────────────────────────────────────────

func TestConstants(t *testing.T) {
	if GenesisSupply != 1_000_000 {
		t.Errorf("GenesisSupply = %f, want 1000000", GenesisSupply)
	}
	if MaxTotalSupply != 21_000_000 {
		t.Errorf("MaxTotalSupply = %f, want 21000000", MaxTotalSupply)
	}
	if MaxMiningSupply != 20_000_000 {
		t.Errorf("MaxMiningSupply = %f, want 20000000", MaxMiningSupply)
	}
	if InitialBlockReward != 50.0 {
		t.Errorf("InitialBlockReward = %f, want 50", InitialBlockReward)
	}
	if HalvingInterval != 210_000 {
		t.Errorf("HalvingInterval = %d, want 210000", HalvingInterval)
	}
	if DifficultyAdjustmentInterval != 2016 {
		t.Errorf("DifficultyAdjustmentInterval = %d, want 2016", DifficultyAdjustmentInterval)
	}
	if TargetBlockTime != 10*time.Minute {
		t.Errorf("TargetBlockTime = %v, want 10m", TargetBlockTime)
	}
	// Verify tokenomics: genesis + mining = total.
	if GenesisSupply+MaxMiningSupply != MaxTotalSupply {
		t.Errorf("GenesisSupply + MaxMiningSupply = %f, want %f", GenesisSupply+MaxMiningSupply, MaxTotalSupply)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// HashTransaction (CRITICAL-2: deterministic binary serialization)
// ──────────────────────────────────────────────────────────────────────────────

func TestHashTransaction(t *testing.T) {
	tx := &Transaction{
		Version: TxVersion,
		Inputs: []TxInput{
			{PrevTxID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PrevIndex: 0, PubKey: "pk1", Signature: "sig1"},
		},
		Outputs: []TxOutput{
			{Amount: 100, Address: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
	}
	hash1 := HashTransaction(tx)
	if hash1 == "" {
		t.Fatal("HashTransaction() returned empty string")
	}
	if len(hash1) != 64 {
		t.Errorf("hash length = %d, want 64", len(hash1))
	}

	// Same input must produce same hash.
	hash2 := HashTransaction(tx)
	if hash1 != hash2 {
		t.Error("HashTransaction() is not deterministic")
	}

	// Different output amount must produce different hash.
	tx2 := &Transaction{
		Version: TxVersion,
		Inputs: []TxInput{
			{PrevTxID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PrevIndex: 0, PubKey: "pk1", Signature: "sig1"},
		},
		Outputs: []TxOutput{
			{Amount: 200, Address: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
	}
	hash3 := HashTransaction(tx2)
	if hash1 == hash3 {
		t.Error("HashTransaction() returned same hash for different input")
	}
}

func TestHashTransaction_SignatureExcluded(t *testing.T) {
	// The hash should be the same regardless of signature value
	// (signatures are excluded from the hash computation).
	tx1 := &Transaction{
		Version: TxVersion,
		Inputs: []TxInput{
			{PrevTxID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PrevIndex: 0, PubKey: "pk1", Signature: "sig_a"},
		},
		Outputs: []TxOutput{
			{Amount: 100, Address: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
	}
	tx2 := &Transaction{
		Version: TxVersion,
		Inputs: []TxInput{
			{PrevTxID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PrevIndex: 0, PubKey: "pk1", Signature: "sig_b"},
		},
		Outputs: []TxOutput{
			{Amount: 100, Address: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
	}
	if HashTransaction(tx1) != HashTransaction(tx2) {
		t.Error("HashTransaction should be the same regardless of signature (signatures excluded)")
	}
}

func TestComputeSighash(t *testing.T) {
	tx := &Transaction{
		Version: TxVersion,
		Inputs: []TxInput{
			{PrevTxID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PrevIndex: 0},
		},
		Outputs: []TxOutput{
			{Amount: 50, Address: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
	}
	sighash := ComputeSighash(tx)
	if len(sighash) != 32 {
		t.Errorf("sighash length = %d, want 32", len(sighash))
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Transaction helpers
// ──────────────────────────────────────────────────────────────────────────────

func TestIsCoinbase(t *testing.T) {
	cb := Transaction{CoinbaseData: "coinbase:1"}
	if !cb.IsCoinbase() {
		t.Error("IsCoinbase() should be true for coinbase tx")
	}

	regular := Transaction{
		Inputs:  []TxInput{{PrevTxID: "tx1", PrevIndex: 0}},
		Outputs: []TxOutput{{Amount: 50, Address: "addr"}},
	}
	if regular.IsCoinbase() {
		t.Error("IsCoinbase() should be false for regular tx")
	}
}

func TestIsGenesis(t *testing.T) {
	gen := Transaction{CoinbaseData: "genesis"}
	if !gen.IsGenesis() {
		t.Error("IsGenesis() should be true")
	}

	cb := Transaction{CoinbaseData: "coinbase:1"}
	if cb.IsGenesis() {
		t.Error("IsGenesis() should be false for coinbase")
	}
}

func TestTotalOutputValue(t *testing.T) {
	tx := Transaction{
		Outputs: []TxOutput{
			{Amount: 30, Address: "a"},
			{Amount: 20, Address: "b"},
		},
	}
	if tx.TotalOutputValue() != 50 {
		t.Errorf("TotalOutputValue() = %f, want 50", tx.TotalOutputValue())
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Merkle Tree
// ──────────────────────────────────────────────────────────────────────────────

func TestComputeMerkleRoot_Empty(t *testing.T) {
	root := ComputeMerkleRoot(nil)
	if root == "" {
		t.Fatal("ComputeMerkleRoot(nil) returned empty string")
	}
	if len(root) != 64 {
		t.Errorf("root length = %d, want 64", len(root))
	}
}

func TestComputeMerkleRoot_SingleTx(t *testing.T) {
	txID := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	root := ComputeMerkleRoot([]string{txID})
	if root == "" {
		t.Fatal("ComputeMerkleRoot(1 tx) returned empty string")
	}
}

func TestComputeMerkleRoot_TwoTxs(t *testing.T) {
	ids := []string{
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	root := ComputeMerkleRoot(ids)
	if root == "" {
		t.Fatal("ComputeMerkleRoot(2 txs) returned empty string")
	}
}

func TestComputeMerkleRoot_OddTxs(t *testing.T) {
	ids := []string{
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
	root := ComputeMerkleRoot(ids)
	if root == "" {
		t.Fatal("ComputeMerkleRoot(3 txs) returned empty string")
	}
	if len(root) != 64 {
		t.Errorf("root length = %d, want 64", len(root))
	}
}

func TestComputeMerkleRoot_Deterministic(t *testing.T) {
	ids := []string{"aabb", "ccdd", "eeff"}
	r1 := ComputeMerkleRoot(ids)
	r2 := ComputeMerkleRoot(ids)
	if r1 != r2 {
		t.Error("ComputeMerkleRoot() is not deterministic")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Block Hashing & PoW
// ──────────────────────────────────────────────────────────────────────────────

func TestHashBlockHeader_Deterministic(t *testing.T) {
	h := BlockHeader{
		Version:       2,
		Height:        5,
		PrevBlockHash: "0000000000000000000000000000000000000000000000000000000000000000",
		MerkleRoot:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Timestamp:     1000,
		Bits:          "00ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		Nonce:         42,
	}
	hash1 := HashBlockHeader(h)
	hash2 := HashBlockHeader(h)
	if hash1 != hash2 {
		t.Error("HashBlockHeader() is not deterministic")
	}
	if len(hash1) != 64 {
		t.Errorf("hash length = %d, want 64", len(hash1))
	}
}

func TestBitsAndTarget_RoundTrip(t *testing.T) {
	target := new(big.Int)
	target.SetString("00ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", 16)

	bits := BitsFromTarget(target)
	recovered := TargetFromBits(bits)

	if target.Cmp(recovered) != 0 {
		t.Errorf("target round-trip failed: %s != %s", target, recovered)
	}
}

func TestMeetsTarget(t *testing.T) {
	target := new(big.Int)
	target.SetString("00ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", 16)

	if !MeetsTarget("0000000000000000000000000000000000000000000000000000000000000001", target) {
		t.Error("MeetsTarget() should return true for hash below target")
	}

	if MeetsTarget("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", target) {
		t.Error("MeetsTarget() should return false for hash above target")
	}
}

func TestMineBlock(t *testing.T) {
	easyTarget := new(big.Int)
	easyTarget.SetString("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", 16)

	tx := NewCoinbaseTx("miner", 50, 1)

	b := &Block{
		Header: BlockHeader{
			Version:       BlockVersion,
			Height:        1,
			PrevBlockHash: "0000000000000000000000000000000000000000000000000000000000000000",
			MerkleRoot:    ComputeMerkleRoot([]string{tx.ID}),
			Timestamp:     time.Now().Unix(),
		},
		Transactions: []Transaction{tx},
	}

	err := MineBlock(b, easyTarget, 100)
	if err != nil {
		t.Fatalf("MineBlock() error: %v", err)
	}
	if b.Hash == "" {
		t.Error("MineBlock() did not set hash")
	}
	if !MeetsTarget(b.Hash, easyTarget) {
		t.Error("MineBlock() hash does not meet target")
	}
}

func TestMineBlock_ExhaustedAttempts(t *testing.T) {
	impossibleTarget := big.NewInt(0)

	tx := NewCoinbaseTx("miner", 50, 1)
	b := &Block{
		Header: BlockHeader{
			Version:       BlockVersion,
			Height:        1,
			PrevBlockHash: "0000000000000000000000000000000000000000000000000000000000000000",
			MerkleRoot:    ComputeMerkleRoot([]string{tx.ID}),
			Timestamp:     time.Now().Unix(),
		},
		Transactions: []Transaction{tx},
	}

	err := MineBlock(b, impossibleTarget, 10)
	if err == nil {
		t.Error("MineBlock() should fail with impossible target")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Block Reward & Halving
// ──────────────────────────────────────────────────────────────────────────────

func TestBlockReward_Initial(t *testing.T) {
	reward := BlockReward(1, 0)
	if reward != 50.0 {
		t.Errorf("BlockReward(1, 0) = %f, want 50", reward)
	}
}

func TestBlockReward_FirstHalving(t *testing.T) {
	reward := BlockReward(HalvingInterval, 0)
	if reward != 25.0 {
		t.Errorf("BlockReward(%d, 0) = %f, want 25", HalvingInterval, reward)
	}
}

func TestBlockReward_SecondHalving(t *testing.T) {
	reward := BlockReward(2*HalvingInterval, 0)
	if reward != 12.5 {
		t.Errorf("BlockReward(%d, 0) = %f, want 12.5", 2*HalvingInterval, reward)
	}
}

func TestBlockReward_CapExceeded(t *testing.T) {
	reward := BlockReward(1, MaxMiningSupply)
	if reward != 0 {
		t.Errorf("BlockReward with full supply = %f, want 0", reward)
	}
}

func TestBlockReward_PartialRemaining(t *testing.T) {
	reward := BlockReward(1, MaxMiningSupply-10)
	if reward != 10 {
		t.Errorf("BlockReward with 10 remaining = %f, want 10", reward)
	}
}

func TestBlockReward_ManyHalvings(t *testing.T) {
	reward := BlockReward(64*HalvingInterval, 0)
	if reward != 0 {
		t.Errorf("BlockReward(64 halvings) = %f, want 0", reward)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Difficulty Adjustment
// ──────────────────────────────────────────────────────────────────────────────

func TestAdjustDifficulty_TooFast(t *testing.T) {
	expectedSpan := int64(DifficultyAdjustmentInterval) * int64(TargetBlockTime.Seconds())
	actualSpan := expectedSpan / 2

	newTarget := AdjustDifficulty(InitialTarget, actualSpan)

	if newTarget.Cmp(InitialTarget) >= 0 {
		t.Error("AdjustDifficulty() should lower target (harder) when blocks are fast")
	}
}

func TestAdjustDifficulty_TooSlow(t *testing.T) {
	expectedSpan := int64(DifficultyAdjustmentInterval) * int64(TargetBlockTime.Seconds())
	actualSpan := expectedSpan * 2

	currentTarget := new(big.Int).Div(InitialTarget, big.NewInt(10))
	newTarget := AdjustDifficulty(currentTarget, actualSpan)

	if newTarget.Cmp(currentTarget) <= 0 {
		t.Error("AdjustDifficulty() should raise target (easier) when blocks are slow")
	}
}

func TestAdjustDifficulty_ClampedMax(t *testing.T) {
	expectedSpan := int64(DifficultyAdjustmentInterval) * int64(TargetBlockTime.Seconds())
	actualSpan := expectedSpan * 100

	currentTarget := new(big.Int).Div(InitialTarget, big.NewInt(100))
	newTarget := AdjustDifficulty(currentTarget, actualSpan)

	maxAllowed := new(big.Int).Mul(currentTarget, big.NewInt(int64(MaxDifficultyAdjustmentFactor)))
	if newTarget.Cmp(maxAllowed) > 0 && newTarget.Cmp(InitialTarget) > 0 {
		t.Error("AdjustDifficulty() should be clamped to 4x")
	}
}

func TestAdjustDifficulty_NeverExceedsInitial(t *testing.T) {
	expectedSpan := int64(DifficultyAdjustmentInterval) * int64(TargetBlockTime.Seconds())
	actualSpan := expectedSpan * 100

	newTarget := AdjustDifficulty(InitialTarget, actualSpan)

	if newTarget.Cmp(InitialTarget) > 0 {
		t.Error("AdjustDifficulty() should never exceed InitialTarget")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Coinbase Transaction (CRITICAL-2: UTXO model)
// ──────────────────────────────────────────────────────────────────────────────

func TestNewCoinbaseTx(t *testing.T) {
	tx := NewCoinbaseTx("miner_address", 50, 10)

	if !tx.IsCoinbase() {
		t.Error("NewCoinbaseTx should create a coinbase transaction")
	}
	if len(tx.Inputs) != 0 {
		t.Errorf("coinbase Inputs = %d, want 0", len(tx.Inputs))
	}
	if len(tx.Outputs) != 1 {
		t.Fatalf("coinbase Outputs = %d, want 1", len(tx.Outputs))
	}
	if tx.Outputs[0].Address != "miner_address" {
		t.Errorf("coinbase output address = %q, want miner_address", tx.Outputs[0].Address)
	}
	if tx.Outputs[0].Amount != 50 {
		t.Errorf("coinbase output amount = %f, want 50", tx.Outputs[0].Amount)
	}
	expectedData := fmt.Sprintf("coinbase:%d", 10)
	if tx.CoinbaseData != expectedData {
		t.Errorf("CoinbaseData = %q, want %q", tx.CoinbaseData, expectedData)
	}
	if tx.ID == "" {
		t.Error("coinbase ID is empty")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Genesis Block
// ──────────────────────────────────────────────────────────────────────────────

func TestNewGenesisBlock(t *testing.T) {
	genesis := NewGenesisBlock()

	if genesis == nil {
		t.Fatal("NewGenesisBlock() returned nil")
	}
	if genesis.Header.Height != 0 {
		t.Errorf("genesis height = %d, want 0", genesis.Header.Height)
	}
	if genesis.Header.PrevBlockHash != "0000000000000000000000000000000000000000000000000000000000000000" {
		t.Error("genesis PrevBlockHash is not all zeros")
	}
	if genesis.Hash == "" {
		t.Error("genesis hash is empty")
	}
	if len(genesis.Transactions) != 1 {
		t.Errorf("genesis TX count = %d, want 1", len(genesis.Transactions))
	}
	genTx := genesis.Transactions[0]
	if !genTx.IsGenesis() {
		t.Error("genesis tx should be identified as genesis")
	}
	if len(genTx.Outputs) != 1 {
		t.Fatalf("genesis TX output count = %d, want 1", len(genTx.Outputs))
	}
	if genTx.Outputs[0].Amount != GenesisSupply {
		t.Errorf("genesis TX amount = %f, want %f", genTx.Outputs[0].Amount, GenesisSupply)
	}
	if genTx.Outputs[0].Address != LegacyGenesisAddress {
		t.Errorf("genesis TX address = %s, want %s", genTx.Outputs[0].Address, LegacyGenesisAddress)
	}
}

func TestGenesisBlockDeterministic(t *testing.T) {
	g1 := NewGenesisBlock()
	g2 := NewGenesisBlock()

	if g1.Hash != g2.Hash {
		t.Error("NewGenesisBlock() is not deterministic")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Block Validation
// ──────────────────────────────────────────────────────────────────────────────

func TestValidateBlockHeader_Genesis(t *testing.T) {
	genesis := NewGenesisBlock()
	err := ValidateBlockHeader(genesis,
		"0000000000000000000000000000000000000000000000000000000000000000", 0)
	if err != nil {
		t.Errorf("ValidateBlockHeader(genesis) error: %v", err)
	}
}

func TestValidateBlockHeader_WrongHeight(t *testing.T) {
	genesis := NewGenesisBlock()
	err := ValidateBlockHeader(genesis,
		"0000000000000000000000000000000000000000000000000000000000000000", 5)
	if err == nil {
		t.Error("ValidateBlockHeader() should fail with wrong height")
	}
}

func TestValidateBlockHeader_WrongPrevHash(t *testing.T) {
	genesis := NewGenesisBlock()
	err := ValidateBlockHeader(genesis, "deadbeef", 0)
	if err == nil {
		t.Error("ValidateBlockHeader() should fail with wrong prev hash")
	}
}

func TestValidateBlockMerkle(t *testing.T) {
	genesis := NewGenesisBlock()
	err := ValidateBlockMerkle(genesis)
	if err != nil {
		t.Errorf("ValidateBlockMerkle(genesis) error: %v", err)
	}
}

func TestValidateBlockMerkle_Tampered(t *testing.T) {
	genesis := NewGenesisBlock()
	genesis.Header.MerkleRoot = "wrong_merkle_root_here"
	err := ValidateBlockMerkle(genesis)
	if err == nil {
		t.Error("ValidateBlockMerkle() should fail with wrong Merkle root")
	}
}

func TestValidateBlock_Genesis(t *testing.T) {
	genesis := NewGenesisBlock()
	err := ValidateBlock(genesis,
		"0000000000000000000000000000000000000000000000000000000000000000", 0)
	if err != nil {
		t.Errorf("ValidateBlock(genesis) error: %v", err)
	}
}

func TestValidateBlock_NoTransactions(t *testing.T) {
	b := &Block{
		Header: BlockHeader{
			Version:       BlockVersion,
			Height:        0,
			PrevBlockHash: "0000000000000000000000000000000000000000000000000000000000000000",
			MerkleRoot:    ComputeMerkleRoot(nil),
			Timestamp:     time.Now().Unix(),
		},
		Transactions: []Transaction{},
	}
	b.Hash = HashBlockHeader(b.Header)

	err := ValidateBlock(b,
		"0000000000000000000000000000000000000000000000000000000000000000", 0)
	if err == nil {
		t.Error("ValidateBlock() should fail for block with no transactions")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// [CRITICAL-1] NewGenesisBlockWithOwner & GenesisOwnerFromBlock
// ──────────────────────────────────────────────────────────────────────────────

func TestNewGenesisBlockWithOwner(t *testing.T) {
	customAddr := "aabbccdd11223344aabbccdd11223344aabbccdd11223344aabbccdd11223344"
	genesis := NewGenesisBlockWithOwner(customAddr)

	if genesis == nil {
		t.Fatal("NewGenesisBlockWithOwner() returned nil")
	}
	if genesis.Header.Height != 0 {
		t.Errorf("genesis height = %d, want 0", genesis.Header.Height)
	}
	if genesis.Transactions[0].Outputs[0].Address != customAddr {
		t.Errorf("genesis TX address = %s, want %s", genesis.Transactions[0].Outputs[0].Address, customAddr)
	}
	if genesis.Transactions[0].Outputs[0].Amount != GenesisSupply {
		t.Errorf("genesis TX amount = %f, want %f", genesis.Transactions[0].Outputs[0].Amount, GenesisSupply)
	}

	legacyGenesis := NewGenesisBlock()
	if genesis.Hash == legacyGenesis.Hash {
		t.Error("custom genesis should have different hash from legacy genesis")
	}
}

func TestNewGenesisBlockWithOwner_Deterministic(t *testing.T) {
	addr := "deadbeef0123456789abcdef0123456789abcdef0123456789abcdef01234567"
	g1 := NewGenesisBlockWithOwner(addr)
	g2 := NewGenesisBlockWithOwner(addr)

	if g1.Hash != g2.Hash {
		t.Error("NewGenesisBlockWithOwner() should be deterministic for same address")
	}
}

func TestGenesisOwnerFromBlock(t *testing.T) {
	customAddr := "1122334455667788112233445566778811223344556677881122334455667788"
	genesis := NewGenesisBlockWithOwner(customAddr)

	owner, ok := GenesisOwnerFromBlock(genesis)
	if !ok {
		t.Fatal("GenesisOwnerFromBlock() returned false")
	}
	if owner != customAddr {
		t.Errorf("GenesisOwnerFromBlock() = %s, want %s", owner, customAddr)
	}
}

func TestGenesisOwnerFromBlock_Legacy(t *testing.T) {
	genesis := NewGenesisBlock()

	owner, ok := GenesisOwnerFromBlock(genesis)
	if !ok {
		t.Fatal("GenesisOwnerFromBlock() returned false for legacy genesis")
	}
	if owner != LegacyGenesisAddress {
		t.Errorf("GenesisOwnerFromBlock() = %s, want %s", owner, LegacyGenesisAddress)
	}
}

func TestGenesisOwnerFromBlock_Nil(t *testing.T) {
	_, ok := GenesisOwnerFromBlock(nil)
	if ok {
		t.Error("GenesisOwnerFromBlock(nil) should return false")
	}
}

func TestGenesisOwnerFromBlock_NonGenesis(t *testing.T) {
	b := &Block{
		Header: BlockHeader{
			Height: 1, // not genesis
		},
		Transactions: []Transaction{},
	}
	_, ok := GenesisOwnerFromBlock(b)
	if ok {
		t.Error("GenesisOwnerFromBlock() should return false for non-genesis block")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// [CRITICAL-2] Legacy Detection
// ──────────────────────────────────────────────────────────────────────────────

func TestIsLegacyBlock_NewFormat(t *testing.T) {
	genesis := NewGenesisBlock()
	if IsLegacyBlock(genesis) {
		t.Error("genesis block should not be detected as legacy")
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// HIGH-3: Fuzz Tests for Serialization
// ══════════════════════════════════════════════════════════════════════════════

// FuzzSerializeTxForHash ensures SerializeTxForHash does not panic on arbitrary input.
func FuzzSerializeTxForHash(f *testing.F) {
	// Seed corpus with typical transactions.
	f.Add(uint32(1), "aaaa", 0, 100.0, "bbbb", "coinbase:1")
	f.Add(uint32(0), "", 0, 0.0, "", "")
	f.Add(uint32(255), "ff", 99, 99999.999, "cc", "genesis")

	f.Fuzz(func(t *testing.T, version uint32, prevTxID string, prevIndex int, amount float64, address string, cbData string) {
		tx := &Transaction{
			Version: version,
			Inputs: []TxInput{
				{PrevTxID: prevTxID, PrevIndex: prevIndex},
			},
			Outputs: []TxOutput{
				{Amount: amount, Address: address},
			},
			CoinbaseData: cbData,
		}
		// Must not panic.
		data := SerializeTxForHash(tx)
		if data == nil {
			t.Error("SerializeTxForHash returned nil")
		}

		// Hash must not panic and must return a 64-char hex string.
		hash := HashTransaction(tx)
		if len(hash) != 64 {
			t.Errorf("hash length = %d, want 64", len(hash))
		}

		// Sighash must not panic.
		sighash := ComputeSighash(tx)
		if len(sighash) != 32 {
			t.Errorf("sighash length = %d, want 32", len(sighash))
		}
	})
}

// FuzzMerkleRoot ensures ComputeMerkleRoot does not panic on arbitrary inputs.
func FuzzMerkleRoot(f *testing.F) {
	f.Add("aabbccdd")
	f.Add("0011")
	f.Add("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	f.Fuzz(func(t *testing.T, txID string) {
		// Must not panic regardless of input.
		_ = ComputeMerkleRoot([]string{txID})
		_ = ComputeMerkleRoot([]string{txID, txID})
	})
}

// FuzzBlockHeaderSerialization ensures serializeHeader + HashBlockHeader don't panic.
func FuzzBlockHeaderSerialization(f *testing.F) {
	f.Add(uint32(2), uint64(0), "0000", "aaaa", int64(1000), "00ff", uint64(42))

	f.Fuzz(func(t *testing.T, version uint32, height uint64, prevHash string, merkle string, timestamp int64, bits string, nonce uint64) {
		h := BlockHeader{
			Version:       version,
			Height:        height,
			PrevBlockHash: prevHash,
			MerkleRoot:    merkle,
			Timestamp:     timestamp,
			Bits:          bits,
			Nonce:         nonce,
		}
		// Must not panic.
		hash := HashBlockHeader(h)
		if hash == "" {
			t.Error("HashBlockHeader returned empty string")
		}
	})
}
