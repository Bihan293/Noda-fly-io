# Noda — Bitcoin-like Node Roadmap

## Current State Analysis

Noda is a minimal cryptocurrency node with:
- Ed25519 cryptography (key generation, signing, verification)
- Flat transaction chain (SHA-256 linked, no blocks)
- In-memory balance ledger with JSON persistence
- HTTP REST API
- Basic HTTP-based P2P (broadcast + longest-chain sync)
- Faucet with per-address cooldown

### What's Missing for a Bitcoin-like Node

| Feature | Current | Target |
|---|---|---|
| Block structure | None (flat TX chain) | Full blocks with Header + Body |
| Proof of Work | None | SHA-256 double-hash PoW |
| Merkle Tree | None | Binary Merkle tree for TX inclusion proofs |
| Difficulty Adjustment | None | Every 2016 blocks (Bitcoin-style) |
| Block Reward | None | 50 coins, halving every 210,000 blocks |
| Mempool | None | In-memory pending TX pool |
| UTXO Model | Account-based balances | UTXO set with inputs/outputs |
| Faucet | 50 coins/req, per-address cooldown | 100 coins/req, global cap 1M total |
| P2P Protocol | HTTP REST | TCP binary protocol (Bitcoin-style) |
| Total Supply | 1M (genesis only) | 21M (1M faucet + 20M mining) |
| Mining | None | PoW mining with coinbase TX |
| Tests | None | Full test suite + CI |
| Production | Basic logging | slog, Prometheus, graceful shutdown |

---

## Tokenomics (STRICT)

```
Genesis Supply:           1,000,000 coins (minted at genesis, held by genesis address)
Faucet Distribution:      100 coins per request
  - Any address can claim (multiple addresses, many times)
  - Global cap: 1,000,000 total coins distributed via faucet
  - Once 1M distributed → faucet permanently disabled for ALL addresses
Mining Rewards:           Starts at 50 coins/block
  - Halving every 210,000 blocks
  - Mining continues until total supply reaches 21,000,000
Max Total Supply:         21,000,000 coins
  - 1,000,000 from faucet (genesis)
  - 20,000,000 from mining rewards
Difficulty Adjustment:    Every 2016 blocks (target: ~10 min/block)
```

---

## Development Roadmap — 6 Stages

### Stage 1: Blocks + PoW + Halving [CRITICAL-2]

**Goal:** Replace flat transaction chain with proper block structure and Proof of Work.

**Deliverables:**
- [ ] New `block/` package:
  - `Block` struct with `Header` (Version, PrevBlockHash, MerkleRoot, Timestamp, Bits/Target, Nonce) and `Body` (list of transactions)
  - `BlockHeader` struct with all Bitcoin-like fields
  - Binary Merkle Tree computation from transaction list
  - SHA-256 double-hash PoW mining function
  - Dynamic difficulty adjustment (recalculate every 2016 blocks, target 10 min/block)
  - Block reward with halving: 50 coins initial, halving every 210,000 blocks
  - Coinbase transaction generation (mining reward)
- [ ] Genesis block (not just genesis TX) containing the 1M supply transaction
- [ ] Block validation (PoW check, Merkle root verification, hash chain integrity)
- [ ] Update `chain/` to store blocks instead of flat transactions
- [ ] Faucet amount updated from 50 → 100 coins per request

### Stage 2: Mempool + UTXO + Faucet Global Cap [CRITICAL-3]

**Goal:** Add transaction pool and UTXO model; enforce faucet's 1M global limit.

**Deliverables:**
- [ ] New `mempool/` package:
  - In-memory pool of unconfirmed transactions
  - TX validation before pool admission (signature, double-spend check)
  - Priority ordering (by fee or arrival time)
  - Eviction policy for pool size limits
  - Thread-safe concurrent access
- [ ] New `utxo/` package:
  - UTXO set: map of unspent transaction outputs
  - UTXO creation from block processing
  - UTXO consumption (mark spent) on TX input
  - Double-spend detection via UTXO lookup
  - Rebuild UTXO set from blockchain
- [ ] Faucet global cap enforcement:
  - Track total coins distributed via faucet (persisted)
  - 100 coins per request
  - Multiple claims allowed (different addresses)
  - Faucet permanently disabled when total distributed >= 1,000,000
  - Remove per-address cooldown (replaced by global cap logic)
- [ ] Update ledger to use UTXO instead of account balances

### Stage 3: TCP P2P Protocol [CRITICAL-4]

**Goal:** Replace HTTP-based P2P with Bitcoin-style TCP binary protocol.

**Deliverables:**
- [ ] New TCP-based `p2p/` package:
  - Persistent TCP connections between nodes
  - Binary message framing (length-prefixed)
  - Message types: `version`, `verack`, `inv`, `getdata`, `block`, `tx`, `getblocks`, `ping`, `pong`, `addr`
  - Handshake protocol (version exchange)
  - Peer discovery (addr message propagation)
  - Block announcement and relay
  - Transaction relay
  - Block download (getblocks → inv → getdata → block)
  - Connection manager (max peers, reconnection logic)
- [ ] Initial Block Download (IBD) — sync full chain from peers
- [ ] Inventory system (track what each peer has)
- [ ] Ban/disconnect misbehaving peers
- [ ] Keep HTTP API for wallet/user interaction (read-only chain queries + send TX)

### Stage 4: Storage + Chain Reorganization [CRITICAL-4.5]

**Goal:** Persistent block storage and proper chain reorg handling.

**Deliverables:**
- [ ] Block storage on disk (one file per block or embedded DB)
- [ ] Block index for fast lookup by hash or height
- [ ] Chain reorganization (reorg) support:
  - Detect competing chains
  - Rollback UTXO set to fork point
  - Apply new best chain
- [ ] Orphan block pool (blocks received out of order)
- [ ] Headers-first sync strategy

### Stage 5: Tests + Open-Source Readiness [CRITICAL-5]

**Goal:** Comprehensive testing and documentation for open-source release.

**Deliverables:**
- [ ] Unit tests for every package:
  - `block/` — PoW, Merkle tree, difficulty, halving
  - `mempool/` — add/remove/evict, validation
  - `utxo/` — spend/unspend, rebuild
  - `p2p/` — message encoding, handshake
  - `crypto/` — sign/verify round-trip
  - `chain/` — block addition, reorg
  - `ledger/` — faucet cap, balance queries
- [ ] Integration tests:
  - Multi-node sync scenario
  - Mining + faucet + transfer end-to-end
  - Chain reorg simulation
- [ ] CI pipeline (GitHub Actions):
  - `go build ./...`
  - `go test ./...`
  - `go vet ./...`
  - `golangci-lint`
- [ ] Updated README with architecture diagram
- [ ] CONTRIBUTING.md, LICENSE, CHANGELOG.md

### Stage 6: Production Polish [CRITICAL-6]

**Goal:** Production-ready node with observability, configuration, and security.

**Deliverables:**
- [ ] Structured logging with `log/slog` (replace `log` package)
- [ ] Prometheus metrics endpoint (`/metrics`):
  - Block height, difficulty, hash rate
  - Mempool size, TX count
  - Peer count, connection stats
  - Faucet remaining supply
- [ ] Configuration via Viper (YAML/TOML/env/flags)
- [ ] Graceful shutdown (context cancellation, connection draining)
- [ ] Security hardening:
  - Rate limiting on API endpoints
  - Input validation tightening
  - Max message size limits
  - Peer reputation system
- [ ] Final tokenomics verification:
  - Faucet total cap exactly 1,000,000
  - Mining rewards sum to exactly 20,000,000
  - Total supply never exceeds 21,000,000
- [ ] Docker Compose for multi-node local network
- [ ] Performance benchmarks

---

## Repository Suggestion

**Name:** `Noda` (keep current)
**Description:** `A Bitcoin-like cryptocurrency node in Go — PoW mining, UTXO model, TCP P2P, 21M supply cap`

---

## Progress Tracking

| Stage | Description | Status |
|-------|-------------|--------|
| 1 | Blocks + PoW + Halving | **Done** |
| 2 | Mempool + UTXO + Faucet 11M cap | **Done** |
| 3 | TCP P2P Protocol | **Done** |
| 4 | Storage + Chain Reorg | **Done** |
| 5 | Tests + Open-Source | **Done** |
| 7 | Final Launch Gate | **Done** |

---

## Launch Readiness

All 7 stages are complete. The node passes:
- Full test suite with race detector (`go test -race ./...`)
- Static analysis (`go vet ./...`)
- Fuzz testing on serialization and P2P framing
- Property/invariant tests for tokenomics and UTXO consistency
- Crash-safe persistence with recovery
- P2P hardening with checksums and anti-DoS measures
- Cumulative-work chain selection with reorg support
