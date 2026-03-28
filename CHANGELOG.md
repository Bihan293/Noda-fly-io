# Changelog

All notable changes to Noda are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [1.0.0] — 2026-03-28

### Changed
- **Tokenomics rebalance** — improved coin distribution for a healthier economy
  - Faucet amount reduced: 5,000 → 100 coins per request (more gradual distribution)
  - Genesis/faucet supply reduced: 11,000,000 → 1,000,000 coins (less pre-mined)
  - Mining supply increased: 10,000,000 → 20,000,000 coins (more rewards for miners)
  - Total supply remains 21,000,000 (1M faucet + 20M mining)
  - All tests and documentation updated to match new tokenomics

## [0.9.1] — 2026-03-28

### Added
- **Final launch gate** — comprehensive quality gate for public release [HIGH-3]
  - Invariant tests: `sum(UTXO) == genesis + mined`, no double-spend, faucet cap, mining cap
  - Property test: `total_supply <= 21,000,000` across all halving eras
  - Fuzz tests for block serialization (`FuzzSerializeTxForHash`, `FuzzMerkleRoot`, `FuzzBlockHeaderSerialization`)
  - Fuzz tests for P2P message framing (`FuzzP2PMessageRoundTrip`, `FuzzP2PReadMessageMalformed`)
  - Full tx lifecycle integration test: build → sign → mempool → mine → confirm
  - Restart persistence round-trip test with multiple blocks and transactions
  - Competing branches + reorg test with UTXO consistency verification
  - Multi-node sync after divergence test
  - Faucet bootstrap and exhaust simulation test
  - Mining reward clamping at 20M cap test
- **Enhanced CI pipeline** — GitHub Actions now includes:
  - Fuzz tests for block and P2P packages
  - All invariant/property tests run with race detector
- **Known Limitations** section in README — honest disclosure of current scope

### Changed
- README updated: corrected "longest chain" → "cumulative work" chain selection
- README updated: "JSON storage" → "crash-safe blockstore + chainstate"
- ROADMAP updated: all stages marked as Done including Stage 4 (Storage + Chain Reorg)
- CHANGELOG updated with complete version history

## [0.5.0] — 2026-03-27

### Added
- **Structured logging** — migrated from `log` to `log/slog` across all packages
  - Configurable log level via `LOG_LEVEL` env var or `-log-level` flag (debug/info/warn/error)
  - Key-value structured log entries for machine-parseable output
- **Prometheus metrics** — `/metrics` endpoint in Prometheus text exposition format
  - Block height, block count, total mined/faucet coins, block reward, difficulty
  - Mempool size, UTXO count, peer counts (HTTP + TCP)
  - Faucet remaining supply and active status
  - HTTP request counters and duration
  - Transaction accepted/rejected counters
  - P2P message counters
  - Zero external dependencies (custom metrics package)
- **Rate limiting** — per-IP token-bucket rate limiter
  - Configurable via `RATE_LIMIT` env var or `-rate-limit` flag (default: 10 req/s)
  - Returns 429 Too Many Requests with Retry-After header
  - Automatic stale client cleanup
- **Graceful shutdown** — proper context-based shutdown
  - HTTP server graceful drain (10s timeout)
  - P2P node clean disconnect
  - OS signal handling (SIGINT, SIGTERM)
- **Security hardening**
  - Input validation for addresses (hex format, max length)
  - Request body size limits (64 KB max)
  - HTTP server timeouts (read: 15s, write: 30s, idle: 60s)
  - Security headers (X-Content-Type-Options, X-Frame-Options, X-XSS-Protection)
  - Peer URL validation (must start with http:// or https://)
- **Health check endpoint** — `GET /health` (lightweight, no rate limiting)
- **Docker Compose** — multi-node local network (3 nodes with auto-discovery)
- **New packages**: `metrics/`, `ratelimit/`
- Updated Dockerfile with P2P port exposure and health check

### Changed
- `api.Server.Start()` now accepts `context.Context` for graceful shutdown
- `api.Server` struct has new `RateLimiter` field
- P2P UserAgent updated to `/Noda:0.5.0/`
- Upgraded HTTP server with proper timeouts and max header size

## [Unreleased]

## [0.4.0] — 2026-03-27

### Added
- Bitcoin-style TCP P2P protocol (`p2p/` package)
  - Binary message framing (magic + command + payload)
  - Version/verack handshake
  - Ping/pong keep-alive
  - Inventory system (inv, getdata)
  - Block and transaction relay
  - Initial Block Download (IBD) via getblocks
  - Peer discovery via addr messages
  - Ban system for misbehaving peers
- Full test suite for all packages
- CI pipeline via GitHub Actions (build, test, vet)
- `CONTRIBUTING.md` — contribution guidelines

## [0.3.0] — 2026-03-27

### Added
- Mempool (`mempool/` package) — in-memory pool of unconfirmed transactions
- UTXO set (`utxo/` package) — unspent transaction output tracking
- Faucet global cap enforcement: 100 coins/request, total cap 1,000,000
- Per-address cooldown removed; replaced by global cap logic

## [0.2.0] — 2026-03-27

### Added
- Block structure with Header and Body (`block/` package)
- Proof of Work mining (SHA-256 double-hash)
- Binary Merkle Tree for transaction inclusion proofs
- Dynamic difficulty adjustment (every 2,016 blocks)
- Block reward with halving (50 coins, halving every 210,000 blocks)
- Coinbase transaction generation
- Genesis block

## [0.1.0] — 2026-03-27

### Added
- Initial cryptocurrency node implementation
- Ed25519 cryptography for transaction signing
- Flat transaction chain with SHA-256 linking
- In-memory balance ledger with JSON persistence
- HTTP REST API for wallet interactions
- Basic HTTP-based P2P networking
- Faucet with per-address cooldown
- Docker support (multi-stage build)
