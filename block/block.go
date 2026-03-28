// Package block implements Bitcoin-like block structures with Proof of Work,
// Merkle Tree, dynamic difficulty adjustment and halving reward schedule.
//
// Transaction model (CRITICAL-2):
//   - Transactions use explicit UTXO inputs and outputs.
//   - Each TxInput references a previous transaction output (outpoint).
//   - Each TxOutput specifies an amount and destination address.
//   - Coinbase transactions have zero inputs and use IsCoinbase() helper.
//   - Transaction ID is computed via deterministic binary serialization.
//   - Signatures cover the sighash of inputs + outputs.
//
// Tokenomics:
//   - Genesis supply: 1,000,000 coins (distributed via faucet)
//   - Initial block reward: 50 coins
//   - Halving interval: every 210,000 blocks
//   - Max mining supply: 20,000,000 coins
//   - Max total supply: 21,000,000 coins (1M faucet + 20M mining)
//   - Difficulty adjustment: every 2016 blocks, target 10 min/block
package block

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"time"
)

// ──────────────────────────────────────────────────────────────────────────────
// Constants
// ──────────────────────────────────────────────────────────────────────────────

const (
	// GenesisSupply is the total supply minted at genesis (distributed via faucet).
	GenesisSupply float64 = 1_000_000

	// MaxTotalSupply is the absolute maximum coins that can ever exist.
	MaxTotalSupply float64 = 21_000_000

	// MaxMiningSupply is the maximum coins that can be created through mining.
	MaxMiningSupply float64 = 20_000_000

	// InitialBlockReward is the coinbase reward for the first era.
	InitialBlockReward float64 = 50.0

	// HalvingInterval is the number of blocks between reward halvings.
	HalvingInterval uint64 = 210_000

	// DifficultyAdjustmentInterval is the number of blocks between difficulty recalculations.
	DifficultyAdjustmentInterval uint64 = 2016

	// TargetBlockTime is the desired average time between blocks.
	TargetBlockTime = 10 * time.Minute

	// MaxDifficultyAdjustmentFactor limits how much difficulty can change in one adjustment.
	MaxDifficultyAdjustmentFactor = 4.0

	// LegacyGenesisAddress is the historical hardcoded address used in pre-CRITICAL-1 chains.
	// New chains derive the genesis owner from the configured FAUCET_KEY / GENESIS_PRIVATE_KEY.
	LegacyGenesisAddress = "8fdc70be14ada0e514953b00e9148df9ba6207233d72b4c8e4f8cbd275c181de"

	// BlockVersion is the current block format version.
	// Version 2 uses explicit UTXO inputs/outputs transaction model.
	BlockVersion uint32 = 2

	// TxVersion is the current transaction format version.
	TxVersion uint32 = 1
)

// InitialTarget is the starting difficulty target (relatively easy for development).
// In production this would be calibrated for the expected hash rate.
// This represents roughly 2^236 — easy enough for CPU mining.
var InitialTarget *big.Int

func init() {
	InitialTarget = new(big.Int)
	// Start with a moderate difficulty: leading 2 zero-bytes (0x00ff...)
	InitialTarget.SetString("00ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", 16)
}

// ──────────────────────────────────────────────────────────────────────────────
// Transaction: Explicit UTXO Inputs and Outputs (CRITICAL-2)
// ──────────────────────────────────────────────────────────────────────────────

// TxInput represents a reference to a previous transaction output being spent.
type TxInput struct {
	PrevTxID  string `json:"prev_tx_id"`  // hash of the previous transaction
	PrevIndex int    `json:"prev_index"`  // index of the output in the previous transaction
	Signature string `json:"signature"`   // hex-encoded Ed25519 signature
	PubKey    string `json:"pub_key"`     // hex-encoded Ed25519 public key of the signer
}

// TxOutput represents a single transaction output.
type TxOutput struct {
	Amount  float64 `json:"amount"`  // coin amount (must be > 0)
	Address string  `json:"address"` // recipient address (hex-encoded public key)
}

// Transaction represents a transfer of coins using the UTXO model.
// Coinbase transactions have no inputs (len(Inputs)==0) and a special
// coinbase marker in the CoinbaseData field.
//
// Regular transactions have:
//   - One or more inputs referencing previous unspent outputs
//   - One or more outputs specifying recipients and amounts
//   - The transaction fee = sum(input values) - sum(output values)
type Transaction struct {
	ID           string     `json:"id"`            // SHA-256 hash of the serialized tx (excluding signatures)
	Version      uint32     `json:"version"`       // transaction format version
	Inputs       []TxInput  `json:"inputs"`        // inputs (empty for coinbase)
	Outputs      []TxOutput `json:"outputs"`       // outputs
	LockTime     uint64     `json:"lock_time"`     // minimum block height or timestamp (0 = no lock)
	CoinbaseData string     `json:"coinbase_data"` // arbitrary data for coinbase txs (empty for regular)
}

// IsCoinbase returns true if this is a coinbase (mining reward) transaction.
// Coinbase transactions have no inputs.
func (tx *Transaction) IsCoinbase() bool {
	return len(tx.Inputs) == 0 && tx.CoinbaseData != ""
}

// IsGenesis returns true if this is the genesis (initial supply) transaction.
func (tx *Transaction) IsGenesis() bool {
	return len(tx.Inputs) == 0 && tx.CoinbaseData == "genesis"
}

// TotalOutputValue returns the sum of all output amounts.
func (tx *Transaction) TotalOutputValue() float64 {
	var total float64
	for _, out := range tx.Outputs {
		total += out.Amount
	}
	return total
}

// ──────────────────────────────────────────────────────────────────────────────
// Deterministic Transaction Serialization & Hashing
// ──────────────────────────────────────────────────────────────────────────────

// SerializeTxForHash produces a deterministic byte sequence of the transaction
// for computing its ID. Signatures are EXCLUDED so the ID is stable before/after
// signing (the ID covers the "structure" of the tx: what is spent and created).
func SerializeTxForHash(tx *Transaction) []byte {
	buf := make([]byte, 0, 512)

	// Version (4 bytes LE)
	vBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(vBuf, tx.Version)
	buf = append(buf, vBuf...)

	// Number of inputs (4 bytes LE)
	nIn := make([]byte, 4)
	binary.LittleEndian.PutUint32(nIn, uint32(len(tx.Inputs)))
	buf = append(buf, nIn...)

	// Each input: PrevTxID + PrevIndex (signature excluded from hash)
	for _, in_ := range tx.Inputs {
		prevBytes, _ := hex.DecodeString(in_.PrevTxID)
		buf = append(buf, prevBytes...)
		iBuf := make([]byte, 4)
		binary.LittleEndian.PutUint32(iBuf, uint32(in_.PrevIndex))
		buf = append(buf, iBuf...)
	}

	// Number of outputs (4 bytes LE)
	nOut := make([]byte, 4)
	binary.LittleEndian.PutUint32(nOut, uint32(len(tx.Outputs)))
	buf = append(buf, nOut...)

	// Each output: Amount (8 bytes LE as uint64 satoshi-like) + Address
	for _, out := range tx.Outputs {
		aBuf := make([]byte, 8)
		binary.LittleEndian.PutUint64(aBuf, math.Float64bits(out.Amount))
		buf = append(buf, aBuf...)
		addrBytes, _ := hex.DecodeString(out.Address)
		buf = append(buf, addrBytes...)
	}

	// LockTime (8 bytes LE)
	ltBuf := make([]byte, 8)
	binary.LittleEndian.PutUint64(ltBuf, tx.LockTime)
	buf = append(buf, ltBuf...)

	// CoinbaseData (length-prefixed)
	cbData := []byte(tx.CoinbaseData)
	cbLen := make([]byte, 4)
	binary.LittleEndian.PutUint32(cbLen, uint32(len(cbData)))
	buf = append(buf, cbLen...)
	buf = append(buf, cbData...)

	return buf
}

// HashTransaction computes the SHA-256 hash of a transaction's serialized form.
// This is the canonical transaction ID.
func HashTransaction(tx *Transaction) string {
	data := SerializeTxForHash(tx)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// ComputeSighash computes the hash that must be signed by each input.
// The sighash covers the full transaction structure (inputs outpoints + all outputs)
// so the signature commits to exactly what is being spent and created.
// This is a simplified SIGHASH_ALL equivalent.
func ComputeSighash(tx *Transaction) []byte {
	data := SerializeTxForHash(tx)
	h := sha256.Sum256(data)
	return h[:]
}

// ──────────────────────────────────────────────────────────────────────────────
// Block Header & Block
// ──────────────────────────────────────────────────────────────────────────────

// BlockHeader contains all metadata about a block.
type BlockHeader struct {
	Version       uint32 `json:"version"`         // block format version
	Height        uint64 `json:"height"`          // block number (0 = genesis)
	PrevBlockHash string `json:"prev_block_hash"` // hash of the previous block header
	MerkleRoot    string `json:"merkle_root"`     // root of the Merkle tree of transactions
	Timestamp     int64  `json:"timestamp"`       // unix timestamp when block was mined
	Bits          string `json:"bits"`            // compact target representation (hex of target)
	Nonce         uint64 `json:"nonce"`           // PoW nonce
}

// Block is a complete block containing a header and a list of transactions.
type Block struct {
	Header       BlockHeader   `json:"header"`
	Transactions []Transaction `json:"transactions"`
	Hash         string        `json:"hash"` // SHA-256 double-hash of the header
}

// ──────────────────────────────────────────────────────────────────────────────
// Merkle Tree
// ──────────────────────────────────────────────────────────────────────────────

// ComputeMerkleRoot computes the binary Merkle tree root hash from transaction IDs.
// If the list is empty, returns a hash of empty string.
// If the list has an odd number, the last element is duplicated.
func ComputeMerkleRoot(txIDs []string) string {
	if len(txIDs) == 0 {
		h := sha256.Sum256([]byte(""))
		return hex.EncodeToString(h[:])
	}

	// Start with transaction hashes as leaf nodes.
	level := make([][]byte, len(txIDs))
	for i, id := range txIDs {
		b, err := hex.DecodeString(id)
		if err != nil {
			// If ID is not valid hex, hash the string directly.
			h := sha256.Sum256([]byte(id))
			level[i] = h[:]
		} else {
			level[i] = b
		}
	}

	// Build tree bottom-up.
	for len(level) > 1 {
		// Duplicate last element if odd.
		if len(level)%2 != 0 {
			level = append(level, level[len(level)-1])
		}

		nextLevel := make([][]byte, len(level)/2)
		for i := 0; i < len(level); i += 2 {
			combined := append(level[i], level[i+1]...)
			h := doubleSHA256(combined)
			nextLevel[i/2] = h[:]
		}
		level = nextLevel
	}

	return hex.EncodeToString(level[0])
}

// ──────────────────────────────────────────────────────────────────────────────
// Hashing & Proof of Work
// ──────────────────────────────────────────────────────────────────────────────

// doubleSHA256 computes SHA-256(SHA-256(data)), the Bitcoin-style double hash.
func doubleSHA256(data []byte) [32]byte {
	first := sha256.Sum256(data)
	return sha256.Sum256(first[:])
}

// HashBlockHeader computes the double-SHA-256 hash of a block header.
func HashBlockHeader(h BlockHeader) string {
	// Serialize header fields into a deterministic byte sequence.
	data := serializeHeader(h)
	hash := doubleSHA256(data)
	return hex.EncodeToString(hash[:])
}

// serializeHeader converts a BlockHeader into a deterministic byte slice for hashing.
func serializeHeader(h BlockHeader) []byte {
	buf := make([]byte, 0, 256)

	// Version (4 bytes, little-endian)
	vBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(vBuf, h.Version)
	buf = append(buf, vBuf...)

	// Height (8 bytes, little-endian)
	hBuf := make([]byte, 8)
	binary.LittleEndian.PutUint64(hBuf, h.Height)
	buf = append(buf, hBuf...)

	// PrevBlockHash (raw bytes)
	prevBytes, _ := hex.DecodeString(h.PrevBlockHash)
	buf = append(buf, prevBytes...)

	// MerkleRoot (raw bytes)
	merkleBytes, _ := hex.DecodeString(h.MerkleRoot)
	buf = append(buf, merkleBytes...)

	// Timestamp (8 bytes, little-endian)
	tBuf := make([]byte, 8)
	binary.LittleEndian.PutUint64(tBuf, uint64(h.Timestamp))
	buf = append(buf, tBuf...)

	// Bits (raw bytes of target hex)
	buf = append(buf, []byte(h.Bits)...)

	// Nonce (8 bytes, little-endian)
	nBuf := make([]byte, 8)
	binary.LittleEndian.PutUint64(nBuf, h.Nonce)
	buf = append(buf, nBuf...)

	return buf
}

// TargetFromBits parses the hex-encoded target string into a big.Int.
func TargetFromBits(bits string) *big.Int {
	t := new(big.Int)
	t.SetString(bits, 16)
	return t
}

// BitsFromTarget converts a big.Int target back to its hex string representation.
func BitsFromTarget(target *big.Int) string {
	return fmt.Sprintf("%064x", target)
}

// MeetsTarget checks whether the given block hash satisfies the target.
// The hash (as a big-endian number) must be <= target.
func MeetsTarget(hashHex string, target *big.Int) bool {
	hashInt := new(big.Int)
	hashInt.SetString(hashHex, 16)
	return hashInt.Cmp(target) <= 0
}

// MineBlock performs Proof of Work mining on the given block.
// It increments the nonce until the block hash meets the target or maxAttempts is reached.
// Returns the mined block with hash set, or an error if maxAttempts was exhausted.
func MineBlock(b *Block, target *big.Int, maxAttempts uint64) error {
	b.Header.Bits = BitsFromTarget(target)

	for nonce := uint64(0); nonce < maxAttempts; nonce++ {
		b.Header.Nonce = nonce
		hash := HashBlockHeader(b.Header)
		if MeetsTarget(hash, target) {
			b.Hash = hash
			return nil
		}
	}
	return fmt.Errorf("mining failed: exhausted %d attempts", maxAttempts)
}

// ──────────────────────────────────────────────────────────────────────────────
// Block Reward & Halving
// ──────────────────────────────────────────────────────────────────────────────

// BlockReward calculates the mining reward for a given block height.
// Reward starts at InitialBlockReward and halves every HalvingInterval blocks.
// Returns 0 once the reward has been halved below the minimum representable amount,
// or once total mined supply would exceed MaxMiningSupply.
func BlockReward(height uint64, totalMined float64) float64 {
	halvings := height / HalvingInterval

	// After 64 halvings the reward is effectively zero.
	if halvings >= 64 {
		return 0
	}

	reward := InitialBlockReward / math.Pow(2, float64(halvings))

	// Ensure we don't exceed the mining supply cap.
	remaining := MaxMiningSupply - totalMined
	if remaining <= 0 {
		return 0
	}
	if reward > remaining {
		reward = remaining
	}

	// Minimum reward threshold (below 1 satoshi-equivalent, we stop).
	if reward < 0.00000001 {
		return 0
	}

	return reward
}

// ──────────────────────────────────────────────────────────────────────────────
// Dynamic Difficulty Adjustment
// ──────────────────────────────────────────────────────────────────────────────

// AdjustDifficulty recalculates the target based on actual vs expected time span.
// Called every DifficultyAdjustmentInterval blocks.
//
// Parameters:
//   - currentTarget: the current difficulty target
//   - actualTimeSpan: actual seconds elapsed for the last 2016 blocks
//
// The adjustment is clamped to a factor of MaxDifficultyAdjustmentFactor in either direction.
func AdjustDifficulty(currentTarget *big.Int, actualTimeSpan int64) *big.Int {
	expectedTimeSpan := int64(DifficultyAdjustmentInterval) * int64(TargetBlockTime.Seconds())

	// Clamp the actual time span.
	minSpan := expectedTimeSpan / int64(MaxDifficultyAdjustmentFactor)
	maxSpan := expectedTimeSpan * int64(MaxDifficultyAdjustmentFactor)

	if actualTimeSpan < minSpan {
		actualTimeSpan = minSpan
	}
	if actualTimeSpan > maxSpan {
		actualTimeSpan = maxSpan
	}

	// newTarget = currentTarget * actualTimeSpan / expectedTimeSpan
	newTarget := new(big.Int).Set(currentTarget)
	newTarget.Mul(newTarget, big.NewInt(actualTimeSpan))
	newTarget.Div(newTarget, big.NewInt(expectedTimeSpan))

	// Don't let target exceed the initial (easiest) target.
	if newTarget.Cmp(InitialTarget) > 0 {
		newTarget.Set(InitialTarget)
	}

	// Don't let target go to zero.
	if newTarget.Sign() <= 0 {
		newTarget.SetInt64(1)
	}

	return newTarget
}

// ──────────────────────────────────────────────────────────────────────────────
// Cumulative Work (CRITICAL-4)
// ──────────────────────────────────────────────────────────────────────────────

// MaxTargetBig is 2^256 — used for work calculation.
var MaxTargetBig *big.Int

func init() {
	MaxTargetBig = new(big.Int).Lsh(big.NewInt(1), 256)
}

// WorkForTarget returns the amount of expected hashes to find a hash ≤ target.
// work = 2^256 / (target + 1). Returns at least 1 for any valid target.
// This is the standard Bitcoin-style work calculation.
func WorkForTarget(target *big.Int) *big.Int {
	if target.Sign() <= 0 {
		return big.NewInt(1)
	}
	// work = 2^256 / (target + 1)
	denom := new(big.Int).Add(target, big.NewInt(1))
	work := new(big.Int).Div(MaxTargetBig, denom)
	if work.Sign() <= 0 {
		return big.NewInt(1)
	}
	return work
}

// WorkForBits computes the work for a target given as a hex bits string.
func WorkForBits(bits string) *big.Int {
	return WorkForTarget(TargetFromBits(bits))
}

// ──────────────────────────────────────────────────────────────────────────────
// Coinbase Transaction
// ──────────────────────────────────────────────────────────────────────────────

// NewCoinbaseTx creates a coinbase (mining reward) transaction.
// Coinbase transactions have no inputs and a single output to the miner.
func NewCoinbaseTx(minerAddress string, reward float64, height uint64) Transaction {
	tx := Transaction{
		Version: TxVersion,
		Inputs:  nil, // coinbase has no inputs
		Outputs: []TxOutput{
			{Amount: reward, Address: minerAddress},
		},
		CoinbaseData: fmt.Sprintf("coinbase:%d", height),
	}
	tx.ID = HashTransaction(&tx)
	return tx
}

// ──────────────────────────────────────────────────────────────────────────────
// Genesis Block
// ──────────────────────────────────────────────────────────────────────────────

// NewGenesisBlock creates the genesis block with the initial supply transaction.
// The genesis block has height 0, no previous hash, and a pre-set nonce/hash.
// It uses the LegacyGenesisAddress for backward compatibility.
// New code should use NewGenesisBlockWithOwner instead.
func NewGenesisBlock() *Block {
	return NewGenesisBlockWithOwner(LegacyGenesisAddress)
}

// NewGenesisBlockWithOwner creates the genesis block assigning the initial supply
// to the provided owner address. This address is the one that must be controlled
// by the faucet key to spend genesis funds.
func NewGenesisBlockWithOwner(ownerAddress string) *Block {
	// Genesis transaction: mint the entire faucet supply to the owner address.
	genesisTx := Transaction{
		Version: TxVersion,
		Inputs:  nil, // genesis has no inputs
		Outputs: []TxOutput{
			{Amount: GenesisSupply, Address: ownerAddress},
		},
		LockTime:     0,
		CoinbaseData: "genesis",
	}
	genesisTx.ID = HashTransaction(&genesisTx)

	// Build the genesis block.
	txIDs := []string{genesisTx.ID}
	merkleRoot := ComputeMerkleRoot(txIDs)

	header := BlockHeader{
		Version:       BlockVersion,
		Height:        0,
		PrevBlockHash: "0000000000000000000000000000000000000000000000000000000000000000",
		MerkleRoot:    merkleRoot,
		Timestamp:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix(),
		Bits:          BitsFromTarget(InitialTarget),
		Nonce:         0, // Genesis nonce is 0 (no PoW required for genesis).
	}

	block := &Block{
		Header:       header,
		Transactions: []Transaction{genesisTx},
	}

	// Compute hash (no PoW validation for genesis).
	block.Hash = HashBlockHeader(header)

	return block
}

// GenesisOwnerFromBlock extracts the genesis supply recipient from a genesis block.
// Returns the address and true if found, or empty string and false.
func GenesisOwnerFromBlock(b *Block) (string, bool) {
	if b == nil || b.Header.Height != 0 || len(b.Transactions) == 0 {
		return "", false
	}
	for _, tx := range b.Transactions {
		if tx.IsGenesis() && len(tx.Outputs) > 0 && tx.Outputs[0].Amount == GenesisSupply {
			return tx.Outputs[0].Address, true
		}
	}
	return "", false
}

// ──────────────────────────────────────────────────────────────────────────────
// Block Validation
// ──────────────────────────────────────────────────────────────────────────────

// ValidateBlockHeader checks the structural integrity of a block header.
//   - Hash matches the computed double-SHA-256 of the header.
//   - Hash meets the declared target (Bits).
//   - PrevBlockHash matches the expected value.
//   - Height is correct.
func ValidateBlockHeader(b *Block, expectedPrevHash string, expectedHeight uint64) error {
	// Check height.
	if b.Header.Height != expectedHeight {
		return fmt.Errorf("invalid height: expected %d, got %d", expectedHeight, b.Header.Height)
	}

	// Check prev hash.
	if b.Header.PrevBlockHash != expectedPrevHash {
		return fmt.Errorf("invalid prev_block_hash at height %d", b.Header.Height)
	}

	// Check computed hash.
	computed := HashBlockHeader(b.Header)
	if b.Hash != computed {
		return fmt.Errorf("hash mismatch at height %d: stored=%s computed=%s",
			b.Header.Height, b.Hash[:16], computed[:16])
	}

	// Skip PoW check for genesis block.
	if b.Header.Height == 0 {
		return nil
	}

	// Check PoW.
	target := TargetFromBits(b.Header.Bits)
	if !MeetsTarget(b.Hash, target) {
		return fmt.Errorf("PoW not satisfied at height %d", b.Header.Height)
	}

	return nil
}

// ValidateBlockMerkle verifies that the Merkle root in the header matches
// the transactions in the block body.
func ValidateBlockMerkle(b *Block) error {
	txIDs := make([]string, len(b.Transactions))
	for i, tx := range b.Transactions {
		txIDs[i] = tx.ID
	}
	computed := ComputeMerkleRoot(txIDs)
	if b.Header.MerkleRoot != computed {
		return fmt.Errorf("merkle root mismatch at height %d", b.Header.Height)
	}
	return nil
}

// ValidateBlock performs full validation of a block:
// header integrity, PoW, Merkle root, and basic transaction sanity.
func ValidateBlock(b *Block, expectedPrevHash string, expectedHeight uint64) error {
	if err := ValidateBlockHeader(b, expectedPrevHash, expectedHeight); err != nil {
		return err
	}
	if err := ValidateBlockMerkle(b); err != nil {
		return err
	}

	// Must have at least one transaction.
	if len(b.Transactions) == 0 {
		return fmt.Errorf("block at height %d has no transactions", b.Header.Height)
	}

	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Legacy Detection
// ──────────────────────────────────────────────────────────────────────────────

// IsLegacyTransaction returns true if the block appears to contain legacy
// account-like transactions (From/To/Amount model without explicit inputs/outputs).
// This is detected by checking for JSON fields that only exist in legacy format.
// Used for fail-fast rejection of incompatible chain data.
func IsLegacyBlock(b *Block) bool {
	for _, tx := range b.Transactions {
		// In the new model, non-coinbase transactions MUST have inputs.
		// Coinbase/genesis have CoinbaseData set.
		// If a transaction has no inputs AND no CoinbaseData, it's legacy.
		if len(tx.Inputs) == 0 && tx.CoinbaseData == "" && len(tx.Outputs) > 0 {
			return true
		}
	}
	return false
}
