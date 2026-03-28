# Noda — Minimal Cryptocurrency Node in Go

A lightweight Bitcoin-like crypto node with PoW mining, UTXO model, TCP P2P protocol, Prometheus metrics, and a 21M supply cap.

## Features

- **Ed25519 cryptography** — key generation, transaction signing, and verification
- **Block-based chain** — SHA-256 double-hash PoW with Merkle trees
- **Dynamic difficulty** — adjusts every 2,016 blocks (target: ~10 min/block)
- **Block reward halving** — 50 coins initial, halving every 210,000 blocks
- **UTXO model** — unspent transaction output tracking for balance integrity
- **Mempool** — in-memory unconfirmed transaction pool with eviction
- **Faucet** — 100 coins per request, global 1M cap, permanently disabled after cap
- **Tokenomics** — 21M total supply (1M faucet + 20M mining)
- **HTTP API** — RESTful endpoints with rate limiting and security headers
- **Secure by default** — node API does NOT accept private keys in production mode
- **Offline wallet CLI** — build, sign, and broadcast transactions locally
- **TCP P2P** — Bitcoin-style binary protocol with handshake, inventory, block/tx relay
- **Prometheus metrics** — `/metrics` endpoint for monitoring
- **Structured logging** — `log/slog` with configurable levels
- **Graceful shutdown** — context-based with OS signal handling
- **Rate limiting** — per-IP token bucket limiter
- **Docker-ready** — multi-stage build + Docker Compose for multi-node networks
- **Zero dependencies** — uses only the Go standard library

---

## Configuration

Noda reads configuration from **environment variables** first, then **CLI flags**.
CLI flags override environment variables. Both fall back to sensible defaults.

| Env Variable | CLI Flag       | Default          | Description                                      |
|--------------|----------------|------------------|--------------------------------------------------|
| `PORT`       | `-port`        | `3000`           | HTTP port for the node                           |
| `P2P_PORT`   | `-p2p-port`    | `9333`           | TCP P2P port for peer connections                |
| `DATA_FILE`  | `-data`        | `node_data.json` | Path to the JSON persistence file                |
| `FAUCET_KEY` | `-faucet-key`  | (none)           | Hex-encoded Ed25519 private key for faucet wallet|
| `PEERS`      | `-peers`       | (none)           | Comma-separated HTTP peer URLs                   |
| `TCP_PEERS`  | `-tcp-peers`   | (none)           | Comma-separated TCP peer addresses (host:port)   |
| `LOG_LEVEL`  | `-log-level`   | `info`           | Log level: debug, info, warn, error              |
| `RATE_LIMIT` | `-rate-limit`  | `10`             | Max requests per second per IP                   |
| `ALLOW_INSECURE_WALLET_HTTP` | `-allow-insecure-wallet` | `false` | Allow /sign and /send with private keys (dev only) |

---

## How to Run Locally

### Prerequisites

- [Go 1.22+](https://go.dev/dl/)

### Build and run

```bash
# Build the binary
go build -o noda .

# Run with defaults (port 3000, data in node_data.json)
./noda

# Run with environment variables
PORT=8080 DATA_FILE=mydata.json LOG_LEVEL=debug ./noda

# Run with CLI flags
./noda -port 3001 -peers "http://localhost:3000" -data node1.json

# Run with faucet enabled
FAUCET_KEY="your_hex_private_key" ./noda

# Run with rate limiting adjusted
RATE_LIMIT=50 ./noda

# Run with insecure wallet mode (dev/test ONLY — allows /sign and /send with private keys)
ALLOW_INSECURE_WALLET_HTTP=true ./noda
```

### Run multiple nodes locally

```bash
# Terminal 1 — main node with faucet
FAUCET_KEY="<hex_private_key>" ./noda

# Terminal 2 — connects to node 1
PORT=3001 P2P_PORT=9334 PEERS="http://localhost:3000" TCP_PEERS="localhost:9333" DATA_FILE=node1.json ./noda

# Terminal 3 — connects to both
PORT=3002 P2P_PORT=9335 PEERS="http://localhost:3000,http://localhost:3001" TCP_PEERS="localhost:9333,localhost:9334" DATA_FILE=node2.json ./noda
```

---

## How to Run with Docker

### Build the image

```bash
docker build -t noda .
```

### Run a single node

```bash
# Basic run (port 3000)
docker run -p 3000:3000 -p 9333:9333 noda

# With persistent data
docker run -p 3000:3000 -v noda-data:/app noda

# With faucet enabled
docker run -p 3000:3000 \
  -e FAUCET_KEY="your_hex_private_key" \
  noda

# Full example
docker run -d --name noda-node \
  -p 3000:3000 -p 9333:9333 \
  -e PORT=3000 \
  -e FAUCET_KEY="your_hex_private_key" \
  -e LOG_LEVEL=info \
  -e RATE_LIMIT=20 \
  -v noda-data:/app \
  noda
```

### Run a multi-node network with Docker Compose

```bash
# Start all 3 nodes
docker compose up --build

# Stop
docker compose down

# View logs
docker compose logs -f
```

Nodes:
- **Node 1**: http://localhost:3000 (faucet-capable)
- **Node 2**: http://localhost:3001
- **Node 3**: http://localhost:3002

---

## API Endpoints

> **Security Note:** In production mode (default), the node does NOT accept private keys
> over HTTP. The `/sign` and `/send` endpoints are disabled and return `403 Forbidden`.
> Use the offline wallet CLI to sign transactions locally, then broadcast via
> `POST /tx/broadcast`. Set `ALLOW_INSECURE_WALLET_HTTP=true` for development only.

| Method | Endpoint            | Description                                    |
|--------|---------------------|------------------------------------------------|
| GET    | `/health`           | Lightweight health check                       |
| GET    | `/metrics`          | Prometheus-format metrics                      |
| GET    | `/balance?address=` | Get balance for an address                     |
| POST   | `/transaction`      | Submit a pre-signed transaction                |
| POST   | `/tx/broadcast`     | Submit a pre-signed raw transaction (production endpoint) |
| GET    | `/chain`            | Get the full blockchain                        |
| POST   | `/sign`             | Sign a transaction (DEV MODE ONLY — requires `ALLOW_INSECURE_WALLET_HTTP=true`) |
| POST   | `/send`             | Sign + validate + add to mempool (DEV MODE ONLY) |
| POST   | `/faucet`           | Get free coins (100 per request, 1M cap)       |
| GET    | `/generate-keys`    | Generate a new Ed25519 key pair                |
| GET    | `/status`           | Node info (height, peers, faucet, UTXO, mining)|
| GET    | `/mempool`          | View pending transactions                      |
| GET    | `/peers`            | List known peers                               |
| POST   | `/peers`            | Add a new peer `{"peer": "http://..."}`        |
| POST   | `/sync`             | Trigger chain sync from peers                  |

## Complete Usage Walkthrough

### Step 1: Generate Keys (offline)

```bash
# Option A: Use the node API (convenient for development)
curl -s http://localhost:3000/generate-keys | jq

# Option B: Use the wallet CLI (recommended for production)
# The wallet package provides offline key generation and transaction signing.
```

### Step 2: Get Test Coins from Faucet

```bash
curl -s -X POST http://localhost:3000/faucet \
  -H "Content-Type: application/json" \
  -d '{"to": "your_address_here"}' | jq
```

### Step 3: Send Coins (Production — offline signing)

```bash
# Step 3a: Build the raw unsigned transaction offline
# (Use the wallet package or manually construct the JSON)

# Step 3b: Sign the transaction offline with your private key
# (Use the wallet package: wallet.SignTransaction(...))

# Step 3c: Broadcast the signed transaction to the node
curl -s -X POST http://localhost:3000/tx/broadcast \
  -H "Content-Type: application/json" \
  -d '{
    "version": 1,
    "inputs": [{"prev_tx_id": "...", "prev_index": 0, "signature": "...", "pub_key": "..."}],
    "outputs": [{"amount": 25, "address": "recipient_address"}, {"amount": 75, "address": "your_change_address"}]
  }' | jq
```

### Step 3 (Alternative): Send Coins (DEV MODE ONLY)

```bash
# Only works when ALLOW_INSECURE_WALLET_HTTP=true
curl -s -X POST http://localhost:3000/send \
  -H "Content-Type: application/json" \
  -d '{
    "to": "recipient_address_here",
    "amount": 25,
    "private_key": "your_private_key_here"
  }' | jq
```

### Step 4: Check Balance

```bash
curl -s "http://localhost:3000/balance?address=your_address" | jq
```

### Step 5: View Metrics

```bash
curl -s http://localhost:3000/metrics
```

### Step 6: Health Check

```bash
curl -s http://localhost:3000/health | jq
```

---

## Monitoring

### Prometheus Metrics

The `/metrics` endpoint exposes metrics in Prometheus text exposition format:

```
noda_block_height 42
noda_block_count 43
noda_total_mined_coins 2100
noda_total_faucet_coins 1000
noda_block_reward 50
noda_mempool_size 3
noda_utxo_count 128
noda_peer_count_total 5
noda_faucet_remaining_coins 9.99e+05
noda_faucet_active 1
noda_http_requests_total 1234
noda_tx_accepted_total 42
noda_tx_rejected_total 7
noda_blocks_mined_total 42
```

### Grafana Dashboard

You can import these metrics into Grafana for visualization. Configure Prometheus to scrape `http://your-node:3000/metrics`.

---

## Project Structure

```
.
├── main.go                  # Entry point — config, slog, graceful shutdown, component wiring
├── Dockerfile               # Multi-stage Docker build (golang:alpine → alpine)
├── docker-compose.yml       # Multi-node local network (3 nodes)
├── .dockerignore            # Excludes unnecessary files from Docker context
├── crypto/
│   ├── crypto.go            # Ed25519 key gen, signing, verification
│   └── crypto_test.go       # Key pair, sign/verify, address derivation tests
├── block/
│   ├── block.go             # Block structure, PoW, Merkle tree, halving, genesis
│   └── block_test.go        # PoW, difficulty, halving, Merkle, validation tests
├── chain/
│   ├── chain.go             # Blockchain management and serialization
│   └── chain_test.go        # Chain creation, block addition, JSON round-trip tests
├── mempool/
│   ├── mempool.go           # In-memory unconfirmed transaction pool
│   └── mempool_test.go      # Add/remove, FIFO, eviction, double-spend tests
├── utxo/
│   ├── utxo.go              # Unspent Transaction Output set
│   └── utxo_test.go         # Add/spend, balance, ApplyBlock, rebuild tests
├── ledger/
│   ├── ledger.go            # Ledger: chain + UTXO + mempool + faucet
│   └── ledger_test.go       # Faucet, validation, persistence, replacement tests
├── api/
│   ├── server.go            # HTTP server with rate limiting, metrics, security
│   └── server_test.go       # All endpoint tests with httptest
├── wallet/
│   ├── wallet.go            # Offline wallet: key gen, tx build, sign, broadcast
│   └── wallet_test.go       # Wallet offline operations tests
├── network/
│   ├── network.go           # HTTP-based P2P networking
│   └── network_test.go      # Peer management tests
├── p2p/
│   ├── message.go           # TCP protocol messages and wire encoding
│   ├── node.go              # P2P node, handshake, block/tx relay
│   └── p2p_test.go          # Message encoding, peer state, payload tests
├── miner/
│   ├── miner.go             # Background miner: mempool → block template → PoW
│   └── miner_test.go        # Miner configuration and block assembly tests
├── metrics/
│   └── metrics.go           # Prometheus-compatible metrics (zero deps)
├── ratelimit/
│   └── ratelimit.go         # Per-IP token bucket rate limiter
├── integration/
│   └── integration_test.go  # End-to-end tests (mining, UTXO, tokenomics)
├── .github/
│   └── workflows/
│       └── ci.yml           # GitHub Actions CI (build, test, vet, docker)
├── go.mod                   # Go module (zero external dependencies)
├── CONTRIBUTING.md          # Contribution guidelines
├── CHANGELOG.md             # Version history
├── ROADMAP.md               # Development roadmap
└── README.md
```

## Tokenomics

```
Genesis Supply:           1,000,000 coins (minted at genesis)
Faucet Distribution:      100 coins per request
  - Any address can claim (multiple times)
  - Global cap: 1,000,000 total coins via faucet
  - Once 1M distributed → faucet permanently disabled
Mining Rewards:           Starts at 50 coins/block
  - Halving every 210,000 blocks
  - Mining continues until total supply reaches 21,000,000
Max Total Supply:         21,000,000 coins
  - 1,000,000 from faucet (genesis)
  - 20,000,000 from mining rewards
Difficulty Adjustment:    Every 2,016 blocks (target: ~10 min/block)
```

## Testing

Noda has comprehensive test coverage across all packages.

### Run all tests

```bash
go test ./... -v -race -count=1
```

### Run tests for a specific package

```bash
go test ./block/ -v
go test ./crypto/ -v
go test ./wallet/ -v
go test ./integration/ -v
```

### Test Coverage

| Package | Tests |
|---------|-------|
| `crypto/` | Key generation, sign/verify round-trip, invalid inputs |
| `block/` | PoW mining, Merkle tree, difficulty adjustment, halving, genesis, validation |
| `chain/` | Blockchain creation, block addition, serialization, chain validation |
| `mempool/` | Add/remove, FIFO ordering, eviction, double-spend detection |
| `utxo/` | Add/spend, balance queries, ApplyBlock, rebuild from blocks |
| `ledger/` | Faucet state, transaction validation, persistence, chain replacement |
| `wallet/` | Offline key gen, tx building, signing, verification |
| `p2p/` | Message encoding/decoding, peer state, payload round-trips |
| `network/` | Peer management, broadcast |
| `api/` | All HTTP endpoints, security gate (insecure mode), error responses |
| `integration/` | End-to-end mining, UTXO consistency, tokenomics verification |

### CI Pipeline

GitHub Actions runs on every push and pull request:
- `go build ./...` — compilation check
- `go test ./... -v -race` — full test suite with race detector
- `go vet ./...` — static analysis
- `gofmt` — formatting check
- Fuzz testing for block serialization and P2P message framing
- Docker build + health check

### Invariant / Property Tests

| Invariant | What it verifies |
|---|---|
| `sum(UTXO) == genesis_supply + total_mined` | No coins are created or destroyed outside of coinbase/genesis |
| `total_supply <= 21,000,000` | Max supply is never exceeded |
| `faucet_distributed <= 1,000,000` | Faucet cap is enforced |
| `no double-spend in main chain` | Each outpoint is spent at most once |
| `mining_rewards <= 20,000,000` | Mining reward cap is enforced |

### Fuzz Tests

| Target | Package | What it fuzzes |
|---|---|---|
| `FuzzSerializeTxForHash` | `block/` | Transaction binary serialization |
| `FuzzMerkleRoot` | `block/` | Merkle tree computation |
| `FuzzBlockHeaderSerialization` | `block/` | Block header serialization + hashing |
| `FuzzP2PMessageRoundTrip` | `p2p/` | P2P message write/read round-trip |
| `FuzzP2PReadMessageMalformed` | `p2p/` | P2P message parser with garbage input |

## Security

- **No private keys over HTTP**: In production mode (default), the node API does NOT accept private keys. `/sign` and `/send` return `403 Forbidden`. Use the offline wallet package to sign transactions locally.
- **Production transaction endpoint**: Use `POST /tx/broadcast` to submit pre-signed raw transactions.
- **Dev mode**: Set `ALLOW_INSECURE_WALLET_HTTP=true` to enable `/sign` and `/send` for development/testing.
- **Rate limiting**: Configurable per-IP token bucket (default: 10 req/s)
- **Input validation**: Hex address format, length limits
- **Body size limits**: 64 KB max request body
- **Server timeouts**: Read (15s), Write (30s), Idle (60s)
- **Security headers**: X-Content-Type-Options, X-Frame-Options, X-XSS-Protection
- **Peer banning**: Misbehaving peers are banned for 24 hours
- **Non-root Docker**: Runs as unprivileged `noda` user
- **Faucet key protection**: Faucet private keys are never logged or returned in API responses

## Design Decisions

- **Ed25519** over ECDSA: faster, deterministic signatures, no nonce pitfalls
- **log/slog** for structured logging: key-value pairs, configurable levels, machine-parseable
- **Custom Prometheus metrics**: zero external dependencies, standard text format
- **Token bucket rate limiter**: smooth rate control, burst tolerance
- **Graceful shutdown**: context cancellation, connection draining, 10s timeout
- **UTXO model**: prevents double-spend, enables parallel validation
- **Cumulative-work chain selection**: the node with the most cumulative PoW work wins during sync (not longest chain)
- **Offline wallet**: transaction signing happens locally, never over HTTP
- **Crash-safe storage**: blockstore + chainstate persistence with atomic writes, recovery, and legacy migration
- **No external dependencies**: uses only the Go standard library
- **Multi-stage Docker build**: ~15 MB final image, non-root user, health check included

## Known Limitations

- **Script system**: Noda uses a simplified Ed25519 pubkey model instead of Bitcoin's full Script language. All inputs in a transaction must be from the same address.
- **Fee rate**: Fee ordering is based on absolute fee, not fee-per-byte (no virtual size calculation yet).
- **SPV / light clients**: Not supported. All nodes are full nodes.
- **Wallet CLI**: The wallet package provides building blocks but does not include a standalone CLI binary yet.
- **Peer discovery**: Bootstrap requires explicit `--tcp-peers` / `--peers` flags. No DNS seeds.
- **Persistence format**: Blocks are stored as individual JSON files. For very large chains, a binary format (e.g. BoltDB/BadgerDB) would be more efficient.
- **Network protocol**: P2P messages use JSON payloads inside the binary frame. A full binary payload encoding would be more compact.

## License

MIT
