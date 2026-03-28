// Package api provides the HTTP server with JSON endpoints for interacting
// with the cryptocurrency node.
//
// CRITICAL-2: Transactions now use explicit UTXO inputs/outputs.
// - POST /transaction accepts raw signed transactions with explicit inputs/outputs
// - POST /tx/broadcast accepts raw signed transactions (production-safe alias)
//
// CRITICAL-3: Transactions are no longer confirmed instantly.
// - POST /transaction and POST /tx/broadcast return status "pending" + txid
// - Mining happens asynchronously via the miner service
// - GET /status shows mining info (enabled, address, last mined block)
//
// CRITICAL-5: Private keys are no longer accepted over HTTP by default.
// - POST /sign and POST /send are DISABLED in production (return 403).
// - Set ALLOW_INSECURE_WALLET_HTTP=true (dev mode only) to re-enable them.
// - The production endpoint for submitting transactions is POST /tx/broadcast
//   which accepts only pre-signed raw transactions.
// - Wallet operations (key generation, tx building, signing) are done offline
//   via the CLI: `noda wallet new`, `noda wallet build-tx`, `noda wallet sign-tx`.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Bihan293/Noda/block"
	"github.com/Bihan293/Noda/crypto"
	"github.com/Bihan293/Noda/ledger"
	m "github.com/Bihan293/Noda/metrics"
	"github.com/Bihan293/Noda/miner"
	"github.com/Bihan293/Noda/network"
	"github.com/Bihan293/Noda/ratelimit"
)

// Server wraps the ledger and network layer to serve HTTP requests.
type Server struct {
	Ledger               *ledger.Ledger
	Network              *network.Network
	Port                 string
	RateLimiter          *ratelimit.Limiter
	Miner                *miner.Miner // CRITICAL-3: background miner reference
	AllowInsecureWallet  bool         // CRITICAL-5: if false, /sign and /send are disabled
}

// ---------- Helpers ----------

// jsonResponse is a helper to write JSON with a status code.
func jsonResponse(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// errorResponse sends a JSON error message.
func errorResponse(w http.ResponseWriter, status int, msg string) {
	jsonResponse(w, status, map[string]string{"error": msg})
}

// securityHeaders adds common security headers to responses.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		next.ServeHTTP(w, r)
	})
}

// loggingMiddleware wraps a handler and logs every request with timing.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		duration := time.Since(start)
		m.HTTPRequestsTotal.Inc()
		m.HTTPRequestDuration.Set(float64(duration.Microseconds()) / 1000.0)
		slog.Debug("HTTP request",
			"method", r.Method,
			"path", r.URL.Path,
			"duration", duration.Round(time.Microsecond).String(),
			"remote", r.RemoteAddr,
		)
	})
}

// ---------- Request / Response types ----------

// RawTransactionRequest is the JSON body for POST /transaction.
// Accepts a fully formed transaction with explicit inputs/outputs.
type RawTransactionRequest struct {
	Version      uint32          `json:"version"`
	Inputs       []block.TxInput  `json:"inputs"`
	Outputs      []block.TxOutput `json:"outputs"`
	LockTime     uint64          `json:"lock_time"`
	CoinbaseData string          `json:"coinbase_data"`
}

// SendRequest is the JSON body for POST /sign and POST /send.
type SendRequest struct {
	From       string  `json:"from"`
	To         string  `json:"to"`
	Amount     float64 `json:"amount"`
	PrivateKey string  `json:"private_key"`
}

// FaucetRequest is the JSON body for POST /faucet.
type FaucetRequest struct {
	To string `json:"to"`
}

// KeyPairResponse is returned by GET /generate-keys.
type KeyPairResponse struct {
	Address    string `json:"address"`
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key"`
}

// ---------- Input validation ----------

const (
	maxAddressLen    = 256
	maxSignatureLen  = 256
	maxPrivateKeyLen = 256
	maxBodySize      = 1 << 16 // 64 KB
)

// validateHex checks if a string contains only valid hex characters.
func validateHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// validateAddress checks address format and length.
func validateAddress(addr string) error {
	if addr == "" {
		return fmt.Errorf("address is required")
	}
	if len(addr) > maxAddressLen {
		return fmt.Errorf("address too long (max %d chars)", maxAddressLen)
	}
	if !validateHex(addr) {
		return fmt.Errorf("address must be hex-encoded")
	}
	return nil
}

// ---------- Handlers ----------

// handleHealth is a lightweight health check endpoint.
// GET /health
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorResponse(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"node":    "noda",
		"version": "0.9.0",
	})
}

// handleBalance returns the balance for a given address (from UTXO set).
// GET /balance?address=<hex_pubkey>
func (s *Server) handleBalance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorResponse(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	addr := r.URL.Query().Get("address")
	if err := validateAddress(addr); err != nil {
		errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	balance := s.Ledger.GetBalance(addr)
	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"address":    addr,
		"balance":    balance,
		"utxo_count": len(s.Ledger.UTXOSet.GetUTXOsForAddress(addr)),
	})
}

// handleTransaction processes a pre-signed raw UTXO transaction.
// POST /transaction — body: {version, inputs, outputs, lock_time}
// CRITICAL-3: Returns status "pending" — tx goes to mempool, NOT mined instantly.
func (s *Server) handleTransaction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorResponse(w, http.StatusMethodNotAllowed, "POST only")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	var req RawTransactionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Warn("TX decode failed", "error", err)
		errorResponse(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Build the transaction.
	tx := block.Transaction{
		Version:      req.Version,
		Inputs:       req.Inputs,
		Outputs:      req.Outputs,
		LockTime:     req.LockTime,
		CoinbaseData: req.CoinbaseData,
	}
	tx.ID = block.HashTransaction(&tx)

	slog.Info("TX received", "inputs", len(tx.Inputs), "outputs", len(tx.Outputs))

	if err := s.Ledger.SubmitTransaction(tx); err != nil {
		m.TxRejected.Inc()
		errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	m.TxAccepted.Inc()
	s.updateMetrics()

	// Broadcast the valid transaction to all peers.
	go s.Network.BroadcastTransaction(tx)

	jsonResponse(w, http.StatusCreated, map[string]interface{}{
		"message":       "transaction accepted",
		"txid":          tx.ID,
		"status":        "pending",
		"confirmations": 0,
	})
}

// handleChain returns the full blockchain.
// GET /chain
func (s *Server) handleChain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorResponse(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	jsonResponse(w, http.StatusOK, s.Ledger.GetChain())
}

// handleGenerateKeys creates a new key pair for the user.
// GET /generate-keys
func (s *Server) handleGenerateKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorResponse(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		errorResponse(w, http.StatusInternalServerError, "key generation failed")
		return
	}
	slog.Debug("Keys generated", "address", shortAddr(kp.Address))
	jsonResponse(w, http.StatusOK, KeyPairResponse{
		Address:    kp.Address,
		PublicKey:  kp.Address,
		PrivateKey: fmt.Sprintf("%x", kp.PrivateKey),
	})
}

// handlePeers returns the current list of peer URLs.
// GET /peers
func (s *Server) handlePeers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorResponse(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	jsonResponse(w, http.StatusOK, map[string][]string{
		"peers": s.Network.GetPeers(),
	})
}

// handleAddPeer registers a new peer.
// POST /peers — body: {"peer": "http://host:port"}
func (s *Server) handleAddPeer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorResponse(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	var body struct {
		Peer string `json:"peer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Peer == "" {
		errorResponse(w, http.StatusBadRequest, "peer URL required")
		return
	}
	// Basic URL validation.
	if !strings.HasPrefix(body.Peer, "http://") && !strings.HasPrefix(body.Peer, "https://") {
		errorResponse(w, http.StatusBadRequest, "peer URL must start with http:// or https://")
		return
	}
	s.Network.AddPeer(body.Peer)
	jsonResponse(w, http.StatusCreated, map[string]string{
		"message": "peer added",
		"peer":    body.Peer,
	})
}

// handleSync triggers a chain sync from peers (longest chain rule).
// POST /sync
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorResponse(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	replaced := s.Network.SyncChain(s.Ledger)
	if replaced {
		s.updateMetrics()
	}
	jsonResponse(w, http.StatusOK, map[string]bool{
		"chain_replaced": replaced,
	})
}

// handleStatus returns node status including block height, mining info, mempool, UTXO, and faucet state.
// GET /status
// CRITICAL-3: Now includes mining_enabled, miner_address, last_mined_block_hash.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorResponse(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	ch := s.Ledger.GetChain()
	resp := map[string]interface{}{
		"port":                  s.Port,
		"version":               "0.9.0",
		"tx_model":              "utxo_inputs_outputs",
		"chain_selection":       "cumulative_work",
		"block_height":          ch.Height(),
		"chain_length":          ch.Len(),
		"cumulative_work":       ch.CumulativeWork().String(),
		"peers":                 len(s.Network.GetPeers()),
		"http_peers":            len(s.Network.GetPeers()),
		"total_mined":           ch.TotalMined,
		"block_reward":          s.Ledger.GetBlockReward(),
		"total_faucet":          ch.TotalFaucet,
		"faucet_active":         s.Ledger.IsFaucetActive(),
		"mempool_size":          s.Ledger.GetMempoolSize(),
		"utxo_count":            s.Ledger.UTXOSet.Size(),
		"p2p_peers":             s.Network.PeerCount(),
		"max_supply":            block.MaxTotalSupply,
		"genesis_owner":         s.Ledger.GenesisOwner(),
		"faucet_owner_match":    s.Ledger.FaucetOwnerMatch(),
		"usable_faucet_balance": s.Ledger.UsableFaucetBalance(),
		"insecure_wallet_http":  s.AllowInsecureWallet,
	}

	// Block index info (CRITICAL-4).
	if idx := s.Ledger.GetBlockIndex(); idx != nil {
		resp["orphan_count"] = idx.OrphanCount()
	}

	// Mining info (CRITICAL-3).
	if s.Miner != nil {
		resp["mining_enabled"] = s.Miner.IsEnabled()
		resp["miner_address"] = s.Miner.MinerAddress()
		resp["last_mined_block_hash"] = s.Miner.LastMinedHash()
		resp["blocks_mined_by_node"] = s.Miner.BlocksMined()
	} else {
		resp["mining_enabled"] = false
		resp["miner_address"] = ""
		resp["last_mined_block_hash"] = ""
		resp["blocks_mined_by_node"] = 0
	}

	if addr := s.Ledger.FaucetAddress(); addr != "" {
		resp["faucet_address"] = addr
		resp["faucet_owner"] = addr
		resp["faucet_balance"] = s.Ledger.GetBalance(addr)
		resp["faucet_remaining"] = s.Ledger.FaucetRemaining()
	}
	jsonResponse(w, http.StatusOK, resp)
}

// handleMempool returns the current mempool state.
// GET /mempool
func (s *Server) handleMempool(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorResponse(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	pending := s.Ledger.GetPendingTransactions(100)
	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"size":         s.Ledger.GetMempoolSize(),
		"transactions": pending,
	})
}

// insecureEndpointBlocked returns true (and writes a 403 response) if insecure
// wallet endpoints are disabled. Used by /sign and /send.
func (s *Server) insecureEndpointBlocked(w http.ResponseWriter, endpoint string) bool {
	if s.AllowInsecureWallet {
		return false
	}
	errorResponse(w, http.StatusForbidden,
		fmt.Sprintf("%s is disabled in production mode. "+
			"Use the offline CLI (noda wallet sign-tx / build-tx) to sign transactions locally, "+
			"then broadcast via POST /tx/broadcast. "+
			"Set ALLOW_INSECURE_WALLET_HTTP=true to enable this endpoint (dev/test only).", endpoint))
	return true
}

// handleSign signs a transaction using the wallet builder and returns it without broadcasting.
// POST /sign — body: {from, to, amount, private_key}
// CRITICAL-5: Disabled by default in production. Requires AllowInsecureWallet=true.
func (s *Server) handleSign(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorResponse(w, http.StatusMethodNotAllowed, "POST only")
		return
	}

	// CRITICAL-5: Block this endpoint in production.
	if s.insecureEndpointBlocked(w, "POST /sign") {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	var req SendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorResponse(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.PrivateKey == "" {
		errorResponse(w, http.StatusBadRequest, "private_key is required")
		return
	}
	if len(req.PrivateKey) > maxPrivateKeyLen || !validateHex(req.PrivateKey) {
		errorResponse(w, http.StatusBadRequest, "invalid private_key format")
		return
	}
	if req.To == "" {
		errorResponse(w, http.StatusBadRequest, "'to' address is required")
		return
	}
	if err := validateAddress(req.To); err != nil {
		errorResponse(w, http.StatusBadRequest, "invalid 'to': "+err.Error())
		return
	}
	if req.Amount <= 0 {
		errorResponse(w, http.StatusBadRequest, "amount must be positive")
		return
	}

	from := req.From
	if from == "" {
		derived, err := crypto.AddressFromPrivateKey(req.PrivateKey)
		if err != nil {
			errorResponse(w, http.StatusBadRequest, "cannot derive address: "+err.Error())
			return
		}
		from = derived
	}

	// Use the wallet-level builder to create the transaction.
	tx, err := s.Ledger.BuildTransaction(req.PrivateKey, from, req.To, req.Amount)
	if err != nil {
		errorResponse(w, http.StatusBadRequest, "build failed: "+err.Error())
		return
	}

	slog.Debug("TX signed (insecure mode)", "from", shortAddr(from), "to", shortAddr(req.To), "amount", req.Amount)

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"transaction": tx,
		"txid":        tx.ID,
		"warning":     "This endpoint accepts private keys over HTTP. Use offline signing in production.",
	})
}

// handleSend is the all-in-one endpoint: build + sign + validate + mempool + broadcast.
// POST /send — body: {from, to, amount, private_key}
// CRITICAL-3: Returns status "pending" — tx goes to mempool, NOT confirmed instantly.
// CRITICAL-5: Disabled by default in production. Requires AllowInsecureWallet=true.
func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorResponse(w, http.StatusMethodNotAllowed, "POST only")
		return
	}

	// CRITICAL-5: Block this endpoint in production.
	if s.insecureEndpointBlocked(w, "POST /send") {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	var req SendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorResponse(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.PrivateKey == "" {
		errorResponse(w, http.StatusBadRequest, "private_key is required")
		return
	}
	if len(req.PrivateKey) > maxPrivateKeyLen || !validateHex(req.PrivateKey) {
		errorResponse(w, http.StatusBadRequest, "invalid private_key format")
		return
	}
	if req.To == "" {
		errorResponse(w, http.StatusBadRequest, "'to' address is required")
		return
	}
	if err := validateAddress(req.To); err != nil {
		errorResponse(w, http.StatusBadRequest, "invalid 'to': "+err.Error())
		return
	}
	if req.Amount <= 0 {
		errorResponse(w, http.StatusBadRequest, "amount must be positive")
		return
	}

	from := req.From
	if from == "" {
		derived, err := crypto.AddressFromPrivateKey(req.PrivateKey)
		if err != nil {
			errorResponse(w, http.StatusBadRequest, "cannot derive address: "+err.Error())
			return
		}
		from = derived
	}

	// Use the wallet-level builder to create the transaction.
	tx, err := s.Ledger.BuildTransaction(req.PrivateKey, from, req.To, req.Amount)
	if err != nil {
		errorResponse(w, http.StatusBadRequest, "build failed: "+err.Error())
		return
	}

	slog.Info("Processing send (insecure mode)", "from", shortAddr(from), "to", shortAddr(req.To), "amount", req.Amount)

	if err := s.Ledger.SubmitTransaction(*tx); err != nil {
		m.TxRejected.Inc()
		errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	m.TxAccepted.Inc()
	s.updateMetrics()

	// Broadcast to peers.
	go s.Network.BroadcastTransaction(*tx)

	jsonResponse(w, http.StatusCreated, map[string]interface{}{
		"message":       "transaction accepted",
		"txid":          tx.ID,
		"from":          from,
		"to":            req.To,
		"amount":        req.Amount,
		"status":        "pending",
		"confirmations": 0,
		"warning":       "This endpoint accepts private keys over HTTP. Use offline signing in production.",
	})
}

// handleBroadcastRawTx is the production-safe endpoint for submitting pre-signed
// raw transactions. It does NOT accept private keys — only fully formed signed
// transactions with explicit inputs/outputs.
// POST /tx/broadcast — body: {version, inputs, outputs, lock_time}
// CRITICAL-5: This is the recommended production endpoint for transaction submission.
func (s *Server) handleBroadcastRawTx(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorResponse(w, http.StatusMethodNotAllowed, "POST only")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	var req RawTransactionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Warn("TX broadcast decode failed", "error", err)
		errorResponse(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Build the transaction.
	tx := block.Transaction{
		Version:      req.Version,
		Inputs:       req.Inputs,
		Outputs:      req.Outputs,
		LockTime:     req.LockTime,
		CoinbaseData: req.CoinbaseData,
	}
	tx.ID = block.HashTransaction(&tx)

	slog.Info("TX broadcast received", "inputs", len(tx.Inputs), "outputs", len(tx.Outputs))

	if err := s.Ledger.SubmitTransaction(tx); err != nil {
		m.TxRejected.Inc()
		errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	m.TxAccepted.Inc()
	s.updateMetrics()

	// Broadcast the valid transaction to all peers.
	go s.Network.BroadcastTransaction(tx)

	jsonResponse(w, http.StatusCreated, map[string]interface{}{
		"message":       "transaction accepted",
		"txid":          tx.ID,
		"status":        "pending",
		"confirmations": 0,
	})
}

// handleFaucet sends free coins to a given address.
// POST /faucet — body: {"to": "..."}
// No per-address cooldown — only global 11M cap applies.
// CRITICAL-3: Faucet tx goes to mempool, confirmed asynchronously by miner.
func (s *Server) handleFaucet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorResponse(w, http.StatusMethodNotAllowed, "POST only")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	var req FaucetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorResponse(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if err := validateAddress(req.To); err != nil {
		errorResponse(w, http.StatusBadRequest, "invalid 'to': "+err.Error())
		return
	}

	slog.Info("Faucet request", "to", shortAddr(req.To))

	tx, err := s.Ledger.ProcessFaucet(req.To)
	if err != nil {
		errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	s.updateMetrics()

	// Broadcast faucet transaction to peers.
	go s.Network.BroadcastTransaction(*tx)

	jsonResponse(w, http.StatusCreated, map[string]interface{}{
		"message":          fmt.Sprintf("%.0f coins sent from faucet (pending confirmation)", tx.Outputs[0].Amount),
		"to":               req.To,
		"amount":           tx.Outputs[0].Amount,
		"txid":             tx.ID,
		"status":           "pending",
		"confirmations":    0,
		"faucet_remaining": s.Ledger.FaucetRemaining(),
	})
}

// ---------- Metrics update ----------

// updateMetrics syncs all gauge metrics with the current ledger state.
func (s *Server) updateMetrics() {
	ch := s.Ledger.GetChain()
	m.BlockHeight.Set(int64(ch.Height()))
	m.BlockCount.Set(int64(ch.Len()))
	m.TotalMined.Set(ch.TotalMined)
	m.TotalFaucet.Set(ch.TotalFaucet)
	m.BlockReward.Set(s.Ledger.GetBlockReward())
	m.MempoolSize.Set(int64(s.Ledger.GetMempoolSize()))
	m.UTXOCount.Set(int64(s.Ledger.UTXOSet.Size()))
	m.PeerCount.Set(int64(s.Network.PeerCount()))
	m.FaucetRemaining.Set(s.Ledger.FaucetRemaining())
	if s.Ledger.IsFaucetActive() {
		m.FaucetActive.Set(1)
	} else {
		m.FaucetActive.Set(0)
	}
}

// ---------- Router ----------

// Start registers routes and starts the HTTP server on the given port.
// Supports graceful shutdown via the provided context.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// Health check (no rate limiting).
	mux.HandleFunc("/health", s.handleHealth)

	// Prometheus metrics endpoint (no rate limiting).
	mux.Handle("/metrics", m.Handler())

	// Core endpoints
	mux.HandleFunc("/balance", s.handleBalance)
	mux.HandleFunc("/transaction", s.handleTransaction)
	mux.HandleFunc("/chain", s.handleChain)

	// Convenience endpoints (CRITICAL-5: /sign and /send gated by AllowInsecureWallet)
	mux.HandleFunc("/sign", s.handleSign)
	mux.HandleFunc("/send", s.handleSend)
	mux.HandleFunc("/faucet", s.handleFaucet)

	// Production endpoint for raw signed transactions (CRITICAL-5)
	mux.HandleFunc("/tx/broadcast", s.handleBroadcastRawTx)

	// Utility endpoints
	mux.HandleFunc("/generate-keys", s.handleGenerateKeys)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/mempool", s.handleMempool)

	// Peer management
	mux.HandleFunc("/peers", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.handlePeers(w, r)
		case http.MethodPost:
			s.handleAddPeer(w, r)
		default:
			errorResponse(w, http.StatusMethodNotAllowed, "GET or POST only")
		}
	})
	mux.HandleFunc("/sync", s.handleSync)

	// Apply middleware chain: security → rate limiting → logging → routes.
	var handler http.Handler = mux
	if s.RateLimiter != nil {
		handler = s.RateLimiter.Middleware(handler)
	}
	handler = loggingMiddleware(handler)
	handler = securityHeaders(handler)

	addr := ":" + s.Port
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MB
	}

	insecureMode := "disabled"
	if s.AllowInsecureWallet {
		insecureMode = "ENABLED (dev mode — /sign and /send accept private keys)"
	}

	slog.Info("Noda Node listening",
		"address", "http://0.0.0.0"+addr,
		"endpoints", "/health /metrics /balance /transaction /tx/broadcast /chain /sign /send /faucet /generate-keys /status /mempool /peers /sync",
		"insecure_wallet", insecureMode,
	)

	// Graceful shutdown.
	go func() {
		<-ctx.Done()
		slog.Info("Shutting down HTTP server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("HTTP server shutdown error", "error", err)
		}
	}()

	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// shortAddr returns the first 8 and last 4 chars of an address for logging.
func shortAddr(addr string) string {
	if len(addr) <= 16 {
		return addr
	}
	return addr[:8] + "..." + addr[len(addr)-4:]
}
