// Noda — a Bitcoin-like cryptocurrency node in Go.
//
// Features:
//   - Block-based chain with Proof of Work (SHA-256 double-hash)
//   - Merkle Tree for transaction inclusion proofs
//   - Dynamic difficulty adjustment (every 2016 blocks)
//   - Block reward with halving (50 coins, halving every 210,000 blocks)
//   - UTXO set for balance tracking and double-spend prevention
//   - Mempool for unconfirmed transaction management
//   - Background miner: block template assembly from mempool + PoW (CRITICAL-3)
//   - Transaction fees: sum(inputs) - sum(outputs), collected by miner (CRITICAL-3)
//   - Faucet: 100 coins per request, global cap 1,000,000 (no per-address cooldown)
//   - Mining rewards up to 20,000,000 (total supply cap: 21,000,000)
//   - Ed25519 cryptography for transaction signing
//   - HTTP API for wallet interactions with rate limiting
//   - Prometheus-compatible /metrics endpoint
//   - Bitcoin-style TCP P2P protocol with binary message framing
//   - P2P networking with chain synchronization
//   - Structured logging via log/slog
//   - Graceful shutdown with context cancellation
//
// Configuration is read from environment variables first, then CLI flags.
// Environment variables take precedence over defaults but CLI flags override everything.
//
// Environment variables:
//
//	PORT               — HTTP port to listen on              (default: 3000)
//	P2P_PORT           — TCP P2P port to listen on           (default: 9333)
//	DATA_FILE          — path to storage (legacy JSON or directory)  (default: node_data.json)
//	FAUCET_KEY         — hex-encoded Ed25519 private key     (optional)
//	PEERS              — comma-separated list of HTTP peer URLs  (optional)
//	TCP_PEERS          — comma-separated list of TCP peer addresses (host:port) (optional)
//	LOG_LEVEL          — log level: debug, info, warn, error (default: info)
//	RATE_LIMIT         — requests per second per IP           (default: 10)
//	MINING_ENABLED     — enable background mining             (default: true if MINER_ADDRESS set)
//	MINER_ADDRESS      — address to receive mining rewards    (optional)
//	BLOCK_MAX_TX       — max transactions per block           (default: 100)
//	MINING_INTERVAL_MS — interval between mining attempts ms  (default: 5000)
//	MINING_MAX_ATTEMPTS — max PoW nonce attempts per block    (default: 10000000)
//	ALLOW_INSECURE_WALLET_HTTP — allow /sign and /send with private keys (default: false)
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Bihan293/Noda/api"
	"github.com/Bihan293/Noda/block"
	"github.com/Bihan293/Noda/crypto"
	"github.com/Bihan293/Noda/ledger"
	m "github.com/Bihan293/Noda/metrics"
	"github.com/Bihan293/Noda/miner"
	"github.com/Bihan293/Noda/network"
	"github.com/Bihan293/Noda/p2p"
	"github.com/Bihan293/Noda/ratelimit"
)

// envOrDefault returns the value of the environment variable named by key,
// or fallback if the variable is not set or empty.
func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	// ---- Defaults from environment variables ----
	defaultPort := envOrDefault("PORT", "3000")
	defaultP2PPort := envOrDefault("P2P_PORT", "9333")
	defaultData := envOrDefault("DATA_FILE", "node_data.json")
	defaultFaucet := envOrDefault("FAUCET_KEY", "")
	defaultPeers := envOrDefault("PEERS", "")
	defaultTCPPeers := envOrDefault("TCP_PEERS", "")
	defaultLogLevel := envOrDefault("LOG_LEVEL", "info")
	defaultRateLimit := envOrDefault("RATE_LIMIT", "10")

	// Mining defaults (CRITICAL-3).
	defaultMiningEnabled := envOrDefault("MINING_ENABLED", "")
	defaultMinerAddress := envOrDefault("MINER_ADDRESS", "")
	defaultBlockMaxTx := envOrDefault("BLOCK_MAX_TX", "100")
	defaultMiningInterval := envOrDefault("MINING_INTERVAL_MS", "5000")
	defaultMiningMaxAttempts := envOrDefault("MINING_MAX_ATTEMPTS", "10000000")

	// CRITICAL-5: Insecure wallet mode (default: disabled).
	defaultAllowInsecure := envOrDefault("ALLOW_INSECURE_WALLET_HTTP", "false")

	// ---- CLI Flags (override env vars) ----
	port := flag.String("port", defaultPort, "HTTP port for this node (env: PORT)")
	p2pPort := flag.String("p2p-port", defaultP2PPort, "TCP P2P port for this node (env: P2P_PORT)")
	peersFlag := flag.String("peers", defaultPeers, "Comma-separated HTTP peer URLs (env: PEERS)")
	tcpPeersFlag := flag.String("tcp-peers", defaultTCPPeers, "Comma-separated TCP peer addresses host:port (env: TCP_PEERS)")
	dataFile := flag.String("data", defaultData, "Path to JSON storage file (env: DATA_FILE)")
	faucetKey := flag.String("faucet-key", defaultFaucet, "Hex-encoded Ed25519 private key for the faucet wallet (env: FAUCET_KEY)")
	logLevel := flag.String("log-level", defaultLogLevel, "Log level: debug, info, warn, error (env: LOG_LEVEL)")
	rateLimitFlag := flag.String("rate-limit", defaultRateLimit, "Requests per second per IP (env: RATE_LIMIT)")

	// Mining flags (CRITICAL-3).
	miningEnabledFlag := flag.String("mining-enabled", defaultMiningEnabled, "Enable background mining (env: MINING_ENABLED)")
	minerAddressFlag := flag.String("miner-address", defaultMinerAddress, "Address to receive mining rewards (env: MINER_ADDRESS)")
	blockMaxTxFlag := flag.String("block-max-tx", defaultBlockMaxTx, "Max transactions per block (env: BLOCK_MAX_TX)")
	miningIntervalFlag := flag.String("mining-interval", defaultMiningInterval, "Mining attempt interval in ms (env: MINING_INTERVAL_MS)")
	miningMaxAttemptsFlag := flag.String("mining-max-attempts", defaultMiningMaxAttempts, "Max PoW nonce attempts (env: MINING_MAX_ATTEMPTS)")

	// CRITICAL-5: Insecure wallet flag.
	allowInsecureFlag := flag.String("allow-insecure-wallet", defaultAllowInsecure, "Allow /sign and /send with private keys (env: ALLOW_INSECURE_WALLET_HTTP)")

	flag.Parse()

	// ---- Configure structured logging with slog ----
	var level slog.Level
	switch strings.ToLower(*logLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
	})
	logger := slog.New(handler)
	slog.SetDefault(logger)

	// ---- Parse HTTP peers ----
	var httpPeers []string
	if *peersFlag != "" {
		for _, p := range strings.Split(*peersFlag, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				httpPeers = append(httpPeers, p)
			}
		}
	}

	// ---- Parse TCP peers ----
	var tcpPeers []string
	if *tcpPeersFlag != "" {
		for _, p := range strings.Split(*tcpPeersFlag, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				tcpPeers = append(tcpPeers, p)
			}
		}
	}

	// ---- Parse rate limit ----
	ratePerSec, err := strconv.ParseFloat(*rateLimitFlag, 64)
	if err != nil || ratePerSec <= 0 {
		ratePerSec = 10
	}
	limiter := ratelimit.New(ratePerSec, int(ratePerSec*2))

	// ---- Initialize components ----
	slog.Info("╔══════════════════════════════════════════════════════════════╗")
	slog.Info("║         Noda Crypto Node — Bitcoin-like v1.0.0             ║")
	slog.Info("║  UTXO I/O + Mempool + Miner + Fees + CumWork + TCP P2P   ║")
	slog.Info("╚══════════════════════════════════════════════════════════════╝")
	slog.Info("Configuration",
		"http_port", *port,
		"p2p_port", *p2pPort,
		"data_file", *dataFile,
		"log_level", *logLevel,
		"rate_limit", ratePerSec,
		"http_peers", httpPeers,
		"tcp_peers", tcpPeers,
		"insecure_wallet", *allowInsecureFlag,
	)

	// Load or create ledger (chain + UTXO + mempool).
	// If a faucet key is provided, derive the genesis owner address from it
	// so that a new chain assigns the genesis supply to the correct owner.
	var l *ledger.Ledger
	if *faucetKey != "" {
		genesisOwner, err := crypto.AddressFromPrivateKey(*faucetKey)
		if err != nil {
			slog.Error("Invalid FAUCET_KEY: cannot derive address", "error", err)
			os.Exit(1)
		}
		l = ledger.LoadLedgerWithOwner(*dataFile, genesisOwner)
	} else {
		l = ledger.LoadLedger(*dataFile)
	}

	// Update metrics from initial state.
	m.BlockHeight.Set(int64(l.GetChainHeight()))
	m.BlockCount.Set(int64(l.GetChain().Len()))
	m.UTXOCount.Set(int64(l.UTXOSet.Size()))
	m.MempoolSize.Set(int64(l.GetMempoolSize()))
	m.BlockReward.Set(l.GetBlockReward())
	m.TotalMined.Set(l.GetChain().TotalMined)
	m.TotalFaucet.Set(l.GetChain().TotalFaucet)

	slog.Info("Ledger loaded",
		"block_height", l.GetChainHeight(),
		"blocks", l.GetChain().Len(),
		"utxo_count", l.UTXOSet.Size(),
		"mempool_size", l.GetMempoolSize(),
		"block_reward", l.GetBlockReward(),
		"max_supply", block.MaxTotalSupply,
		"genesis_supply", block.GenesisSupply,
		"mining_supply", block.MaxMiningSupply,
	)

	// Configure faucet wallet if key is provided.
	if *faucetKey != "" {
		if err := l.SetFaucetKeyAndValidateGenesis(*faucetKey); err != nil {
			slog.Error("FATAL: faucet/genesis key validation failed", "error", err)
			slog.Error("The provided FAUCET_KEY does not match the genesis owner recorded in the chain.")
			slog.Error("Either use the correct key or delete the data file to start a new chain.")
			os.Exit(1)
		}
		m.FaucetActive.Set(1)
		m.FaucetRemaining.Set(l.FaucetRemaining())
		slog.Info("Faucet configured",
			"faucet_address", l.FaucetAddress(),
			"genesis_owner", l.GenesisOwner(),
			"owner_match", l.FaucetOwnerMatch(),
			"usable_balance", l.UsableFaucetBalance(),
			"remaining", l.FaucetRemaining(),
		)
	} else {
		m.FaucetActive.Set(0)
		slog.Info("Faucet disabled — set FAUCET_KEY or use -faucet-key to enable")
	}

	// ---- Configure Miner (CRITICAL-3) ----
	minerCfg := miner.DefaultConfig()
	minerCfg.MinerAddress = *minerAddressFlag

	// Parse mining enabled flag.
	switch strings.ToLower(*miningEnabledFlag) {
	case "true", "1", "yes":
		minerCfg.Enabled = true
	case "false", "0", "no":
		minerCfg.Enabled = false
	default:
		// Auto: enable if MINER_ADDRESS is set.
		minerCfg.Enabled = minerCfg.MinerAddress != ""
	}

	if v, err := strconv.Atoi(*blockMaxTxFlag); err == nil && v > 0 {
		minerCfg.BlockMaxTx = v
	}
	if v, err := strconv.ParseInt(*miningIntervalFlag, 10, 64); err == nil && v > 0 {
		minerCfg.Interval = time.Duration(v) * time.Millisecond
	}
	if v, err := strconv.ParseUint(*miningMaxAttemptsFlag, 10, 64); err == nil && v > 0 {
		minerCfg.MaxAttempts = v
	}

	minerWorker := miner.New(minerCfg, l)

	slog.Info("Miner configuration",
		"enabled", minerCfg.Enabled,
		"address", minerCfg.MinerAddress,
		"block_max_tx", minerCfg.BlockMaxTx,
		"interval", minerCfg.Interval,
		"max_attempts", minerCfg.MaxAttempts,
	)

	// Create the HTTP network layer with initial peers.
	net := network.NewNetwork(httpPeers)

	// ---- Start TCP P2P Node ----
	var p2pPortNum uint16
	fmt.Sscanf(*p2pPort, "%d", &p2pPortNum)

	p2pNode := p2p.NewNode(p2pPortNum, l, tcpPeers)
	if err := p2pNode.Start(); err != nil {
		slog.Warn("TCP P2P failed to start", "error", err)
	} else {
		// Link TCP node to the network layer.
		net.SetTCPNode(p2pNode)
		slog.Info("P2P node started", "port", p2pPortNum)
	}

	// Attempt initial sync from HTTP peers.
	if len(httpPeers) > 0 {
		slog.Info("Syncing chain from HTTP peers...")
		if net.SyncChain(l) {
			slog.Info("Chain updated from peers",
				"height", l.GetChainHeight(),
				"utxo_count", l.UTXOSet.Size(),
			)
		} else {
			slog.Info("Local chain is up to date")
		}
	}

	// ---- Context for graceful shutdown ----
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle OS signals.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		slog.Info("Shutdown signal received", "signal", sig.String())
		cancel()

		// Give services 10 seconds to drain.
		time.Sleep(10 * time.Second)
		slog.Info("Forced shutdown")
		os.Exit(0)
	}()

	// ---- Start Background Miner (CRITICAL-3) ----
	go minerWorker.Run(ctx)

	// Parse insecure wallet mode (CRITICAL-5).
	allowInsecureWallet := false
	switch strings.ToLower(*allowInsecureFlag) {
	case "true", "1", "yes":
		allowInsecureWallet = true
		slog.Warn("INSECURE WALLET MODE ENABLED — /sign and /send accept private keys over HTTP")
		slog.Warn("This mode is for development/testing ONLY. Do NOT use in production!")
	}

	// ---- Start HTTP server ----
	server := &api.Server{
		Ledger:              l,
		Network:             net,
		Port:                *port,
		RateLimiter:         limiter,
		Miner:               minerWorker,
		AllowInsecureWallet: allowInsecureWallet,
	}

	slog.Info("Starting HTTP server", "port", *port)
	if err := server.Start(ctx); err != nil {
		slog.Error("Server error", "error", err)
		p2pNode.Stop()
		os.Exit(1)
	}

	// If Start returns without error, it means the context was cancelled.
	slog.Info("Shutting down P2P node...")
	p2pNode.Stop()
	slog.Info("Shutdown complete")
}
