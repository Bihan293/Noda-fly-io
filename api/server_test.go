package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Bihan293/Noda/block"
	"github.com/Bihan293/Noda/ledger"
	"github.com/Bihan293/Noda/network"
)

// newTestServer creates a test server with insecure wallet DISABLED (production mode).
func newTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	l := ledger.NewLedger(dir + "/test.json")
	n := network.NewNetwork(nil)
	return &Server{
		Ledger:              l,
		Network:             n,
		Port:                "0",
		AllowInsecureWallet: false, // CRITICAL-5: production mode by default
	}
}

// newTestServerInsecure creates a test server with insecure wallet ENABLED (dev mode).
func newTestServerInsecure(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	l := ledger.NewLedger(dir + "/test.json")
	n := network.NewNetwork(nil)
	return &Server{
		Ledger:              l,
		Network:             n,
		Port:                "0",
		AllowInsecureWallet: true, // dev mode
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// GET /balance
// ──────────────────────────────────────────────────────────────────────────────

func TestHandleBalance(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest("GET", "/balance?address="+block.LegacyGenesisAddress, nil)
	w := httptest.NewRecorder()

	s.handleBalance(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["address"] != block.LegacyGenesisAddress {
		t.Errorf("address = %v, want %s", resp["address"], block.LegacyGenesisAddress)
	}
	if resp["balance"].(float64) != block.GenesisSupply {
		t.Errorf("balance = %v, want %f", resp["balance"], block.GenesisSupply)
	}
}

func TestHandleBalance_MissingAddress(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest("GET", "/balance", nil)
	w := httptest.NewRecorder()

	s.handleBalance(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleBalance_WrongMethod(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest("POST", "/balance?address=test", nil)
	w := httptest.NewRecorder()

	s.handleBalance(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// GET /chain
// ──────────────────────────────────────────────────────────────────────────────

func TestHandleChain(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest("GET", "/chain", nil)
	w := httptest.NewRecorder()

	s.handleChain(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	// Should return JSON with blocks.
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["blocks"] == nil {
		t.Error("response should contain 'blocks'")
	}
}

func TestHandleChain_WrongMethod(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest("POST", "/chain", nil)
	w := httptest.NewRecorder()

	s.handleChain(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// GET /generate-keys
// ──────────────────────────────────────────────────────────────────────────────

func TestHandleGenerateKeys(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest("GET", "/generate-keys", nil)
	w := httptest.NewRecorder()

	s.handleGenerateKeys(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp KeyPairResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Address == "" {
		t.Error("address should not be empty")
	}
	if resp.PublicKey == "" {
		t.Error("public_key should not be empty")
	}
	if resp.PrivateKey == "" {
		t.Error("private_key should not be empty")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// GET /status
// ──────────────────────────────────────────────────────────────────────────────

func TestHandleStatus(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest("GET", "/status", nil)
	w := httptest.NewRecorder()

	s.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["block_height"] == nil {
		t.Error("response should contain 'block_height'")
	}
	if resp["chain_length"] == nil {
		t.Error("response should contain 'chain_length'")
	}
	if resp["mempool_size"] == nil {
		t.Error("response should contain 'mempool_size'")
	}
	if resp["utxo_count"] == nil {
		t.Error("response should contain 'utxo_count'")
	}
	// [CRITICAL-1] New genesis/faucet ownership fields.
	if resp["genesis_owner"] == nil {
		t.Error("response should contain 'genesis_owner'")
	}
	if _, ok := resp["faucet_owner_match"]; !ok {
		t.Error("response should contain 'faucet_owner_match'")
	}
	if _, ok := resp["usable_faucet_balance"]; !ok {
		t.Error("response should contain 'usable_faucet_balance'")
	}
	// [CRITICAL-5] insecure_wallet_http field.
	if _, ok := resp["insecure_wallet_http"]; !ok {
		t.Error("response should contain 'insecure_wallet_http'")
	}
	if resp["insecure_wallet_http"].(bool) != false {
		t.Error("insecure_wallet_http should be false in production mode")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// GET /mempool
// ──────────────────────────────────────────────────────────────────────────────

func TestHandleMempool(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest("GET", "/mempool", nil)
	w := httptest.NewRecorder()

	s.handleMempool(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["size"].(float64) != 0 {
		t.Errorf("mempool size = %v, want 0", resp["size"])
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// GET /peers
// ──────────────────────────────────────────────────────────────────────────────

func TestHandlePeers(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest("GET", "/peers", nil)
	w := httptest.NewRecorder()

	s.handlePeers(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// POST /peers
// ──────────────────────────────────────────────────────────────────────────────

func TestHandleAddPeer(t *testing.T) {
	s := newTestServer(t)

	body, _ := json.Marshal(map[string]string{"peer": "http://localhost:3001"})
	req := httptest.NewRequest("POST", "/peers", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.handleAddPeer(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", w.Code, http.StatusCreated)
	}
}

func TestHandleAddPeer_EmptyPeer(t *testing.T) {
	s := newTestServer(t)

	body, _ := json.Marshal(map[string]string{"peer": ""})
	req := httptest.NewRequest("POST", "/peers", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.handleAddPeer(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// POST /transaction (invalid)
// ──────────────────────────────────────────────────────────────────────────────

func TestHandleTransaction_InvalidJSON(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest("POST", "/transaction", bytes.NewReader([]byte("not json")))
	w := httptest.NewRecorder()

	s.handleTransaction(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleTransaction_WrongMethod(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest("GET", "/transaction", nil)
	w := httptest.NewRecorder()

	s.handleTransaction(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CRITICAL-5: POST /sign — disabled by default in production
// ──────────────────────────────────────────────────────────────────────────────

func TestHandleSign_DisabledByDefault(t *testing.T) {
	s := newTestServer(t) // production mode

	body, _ := json.Marshal(map[string]interface{}{
		"to":          "aabbccdd",
		"amount":      10,
		"private_key": "aabb",
	})
	req := httptest.NewRequest("POST", "/sign", bytes.NewReader(body))
	w := httptest.NewRecorder()

	s.handleSign(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d (should be forbidden in production)", w.Code, http.StatusForbidden)
	}

	var resp map[string]string
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] == "" {
		t.Error("response should contain an error explaining the endpoint is disabled")
	}
}

func TestHandleSign_WorksInDevMode(t *testing.T) {
	s := newTestServerInsecure(t) // dev mode

	body, _ := json.Marshal(map[string]interface{}{
		"to":          "aabbccdd",
		"amount":      10,
		"private_key": "aabb", // invalid key, but we expect 400 not 403
	})
	req := httptest.NewRequest("POST", "/sign", bytes.NewReader(body))
	w := httptest.NewRecorder()

	s.handleSign(w, req)

	// Should get 400 (bad request due to invalid key), NOT 403.
	if w.Code == http.StatusForbidden {
		t.Error("status should NOT be 403 in dev mode")
	}
}

func TestHandleSign_MissingPrivateKey_DevMode(t *testing.T) {
	s := newTestServerInsecure(t) // dev mode

	body, _ := json.Marshal(map[string]interface{}{
		"to":     "some_addr",
		"amount": 10,
	})
	req := httptest.NewRequest("POST", "/sign", bytes.NewReader(body))
	w := httptest.NewRecorder()

	s.handleSign(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleSign_MissingTo_DevMode(t *testing.T) {
	s := newTestServerInsecure(t) // dev mode

	body, _ := json.Marshal(map[string]interface{}{
		"amount":      10,
		"private_key": "aabb",
	})
	req := httptest.NewRequest("POST", "/sign", bytes.NewReader(body))
	w := httptest.NewRecorder()

	s.handleSign(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleSign_NegativeAmount_DevMode(t *testing.T) {
	s := newTestServerInsecure(t) // dev mode

	body, _ := json.Marshal(map[string]interface{}{
		"to":          "addr",
		"amount":      -5,
		"private_key": "aabb",
	})
	req := httptest.NewRequest("POST", "/sign", bytes.NewReader(body))
	w := httptest.NewRecorder()

	s.handleSign(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CRITICAL-5: POST /send — disabled by default in production
// ──────────────────────────────────────────────────────────────────────────────

func TestHandleSend_DisabledByDefault(t *testing.T) {
	s := newTestServer(t) // production mode

	body, _ := json.Marshal(map[string]interface{}{
		"to":          "aabbccdd",
		"amount":      10,
		"private_key": "aabb",
	})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	w := httptest.NewRecorder()

	s.handleSend(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d (should be forbidden in production)", w.Code, http.StatusForbidden)
	}

	var resp map[string]string
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] == "" {
		t.Error("response should contain an error explaining the endpoint is disabled")
	}
}

func TestHandleSend_WorksInDevMode(t *testing.T) {
	s := newTestServerInsecure(t) // dev mode

	body, _ := json.Marshal(map[string]interface{}{
		"to":          "aabbccdd",
		"amount":      10,
		"private_key": "aabb", // invalid key, but we expect 400 not 403
	})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	w := httptest.NewRecorder()

	s.handleSend(w, req)

	// Should get 400 (bad request due to invalid key), NOT 403.
	if w.Code == http.StatusForbidden {
		t.Error("status should NOT be 403 in dev mode")
	}
}

func TestHandleSend_MissingPrivateKey_DevMode(t *testing.T) {
	s := newTestServerInsecure(t) // dev mode

	body, _ := json.Marshal(map[string]interface{}{
		"to":     "addr",
		"amount": 10,
	})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	w := httptest.NewRecorder()

	s.handleSend(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CRITICAL-5: POST /tx/broadcast — always enabled (production endpoint)
// ──────────────────────────────────────────────────────────────────────────────

func TestHandleBroadcastRawTx_InvalidJSON(t *testing.T) {
	s := newTestServer(t) // production mode

	req := httptest.NewRequest("POST", "/tx/broadcast", bytes.NewReader([]byte("not json")))
	w := httptest.NewRecorder()

	s.handleBroadcastRawTx(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleBroadcastRawTx_WrongMethod(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest("GET", "/tx/broadcast", nil)
	w := httptest.NewRecorder()

	s.handleBroadcastRawTx(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleBroadcastRawTx_AlwaysEnabled(t *testing.T) {
	// /tx/broadcast should work in production mode (no private keys involved).
	s := newTestServer(t) // production mode

	body, _ := json.Marshal(map[string]interface{}{
		"version": 1,
		"inputs":  []interface{}{},
		"outputs": []interface{}{},
	})
	req := httptest.NewRequest("POST", "/tx/broadcast", bytes.NewReader(body))
	w := httptest.NewRecorder()

	s.handleBroadcastRawTx(w, req)

	// Should NOT be 403 — the endpoint accepts raw signed transactions.
	if w.Code == http.StatusForbidden {
		t.Error("/tx/broadcast should NOT return 403 in production mode")
	}
	// Should be 400 because the tx has no inputs/outputs (invalid tx).
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (invalid tx)", w.Code, http.StatusBadRequest)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// POST /faucet
// ──────────────────────────────────────────────────────────────────────────────

func TestHandleFaucet_NotConfigured(t *testing.T) {
	s := newTestServer(t)

	body, _ := json.Marshal(map[string]string{"to": "some_addr"})
	req := httptest.NewRequest("POST", "/faucet", bytes.NewReader(body))
	w := httptest.NewRecorder()

	s.handleFaucet(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleFaucet_MissingTo(t *testing.T) {
	s := newTestServer(t)

	body, _ := json.Marshal(map[string]string{"to": ""})
	req := httptest.NewRequest("POST", "/faucet", bytes.NewReader(body))
	w := httptest.NewRecorder()

	s.handleFaucet(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleFaucet_WrongMethod(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest("GET", "/faucet", nil)
	w := httptest.NewRecorder()

	s.handleFaucet(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// POST /sync
// ──────────────────────────────────────────────────────────────────────────────

func TestHandleSync(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest("POST", "/sync", nil)
	w := httptest.NewRecorder()

	s.handleSync(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

func TestShortAddr(t *testing.T) {
	long := "abcdefghijklmnopqrstuvwxyz123456"
	s := shortAddr(long)
	if s == long {
		t.Error("shortAddr should truncate long addresses")
	}
	short := "abc"
	if shortAddr(short) != short {
		t.Error("shortAddr should not truncate short addresses")
	}
}

func TestLoggingMiddleware(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := loggingMiddleware(handler)
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	wrapped.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("loggingMiddleware status = %d, want %d", w.Code, http.StatusOK)
	}
}
