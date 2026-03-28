# Contributing to Noda

Thank you for your interest in contributing to Noda! This document provides guidelines for contributing.

## Getting Started

1. Fork the repository
2. Clone your fork: `git clone https://github.com/YOUR_USERNAME/Noda.git`
3. Create a feature branch: `git checkout -b feature/my-feature`
4. Make your changes
5. Run tests: `go test ./... -race -count=1`
6. Commit with a descriptive message
7. Push and open a Pull Request

## Development Setup

### Prerequisites

- [Go 1.22+](https://go.dev/dl/)
- Git

### Build & Test

```bash
# Build all packages
go build ./...

# Run all tests
go test ./... -v -race -count=1

# Run tests for a specific package
go test ./block/ -v

# Run vet (static analysis)
go vet ./...
```

## Code Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Write clear, descriptive comments for all exported types and functions
- Use `log.Printf` with structured prefixes (e.g., `[CHAIN]`, `[MEMPOOL]`, `[P2P]`)
- Keep functions focused and small
- Prefer returning errors over panicking

## Project Structure

```
.
├── main.go              # Entry point
├── api/                 # HTTP server and REST endpoints
├── block/               # Block structure, PoW, Merkle tree, halving
├── chain/               # Blockchain management and validation
├── crypto/              # Ed25519 key generation, signing, verification
├── ledger/              # Ledger combining chain + UTXO + mempool + faucet
├── mempool/             # In-memory unconfirmed transaction pool
├── network/             # HTTP-based P2P networking
├── p2p/                 # TCP binary P2P protocol (Bitcoin-style)
├── utxo/                # Unspent Transaction Output set
├── integration/         # End-to-end integration tests
├── .github/workflows/   # CI pipeline (GitHub Actions)
└── Dockerfile           # Multi-stage Docker build
```

## Tokenomics (STRICT)

When making changes, ensure these invariants are preserved:

- **Genesis Supply**: 11,000,000 coins (faucet)
- **Max Mining Supply**: 10,000,000 coins
- **Max Total Supply**: 21,000,000 coins (11M faucet + 10M mining)
- **Faucet**: 5,000 coins per request, global cap 11M, permanently disabled after cap
- **Block Reward**: 50 coins initial, halving every 210,000 blocks
- **Difficulty Adjustment**: Every 2,016 blocks, targeting 10 min/block

## Commit Messages

Use conventional commit format:

```
type(scope): description

feat(block): add Merkle proof verification
fix(mempool): prevent duplicate transaction insertion
test(utxo): add balance query tests
docs(readme): update API endpoint table
ci: add golangci-lint to workflow
```

## Pull Request Process

1. Ensure all tests pass (`go test ./...`)
2. Ensure code builds (`go build ./...`)
3. Ensure no vet warnings (`go vet ./...`)
4. Update documentation if needed
5. Request a review

## Reporting Issues

Please include:
- Go version (`go version`)
- OS and architecture
- Steps to reproduce
- Expected vs actual behavior
- Relevant logs

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
