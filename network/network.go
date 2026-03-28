// Package network handles peer-to-peer communication between nodes.
//
// It provides two modes:
//   - HTTP-based P2P (legacy, for basic peer communication)
//   - TCP-based P2P (Bitcoin-style binary protocol via p2p package)
//
// The HTTP layer remains for wallet-facing REST API endpoints.
// The TCP layer handles block/tx relay, peer discovery, and chain sync.
package network

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Bihan293/Noda/block"
	"github.com/Bihan293/Noda/chain"
	"github.com/Bihan293/Noda/ledger"
	"github.com/Bihan293/Noda/p2p"
)

// httpTimeout is the maximum time we wait for peer responses.
const httpTimeout = 5 * time.Second

// Network manages peer connections and cross-node communication.
// It wraps both the legacy HTTP P2P and the new TCP P2P node.
type Network struct {
	httpPeers []string     // legacy HTTP peer base URLs
	mu        sync.RWMutex // guards the httpPeers slice
	client    *http.Client // shared HTTP client with timeout

	// TCP P2P node (nil if not started).
	TCPNode *p2p.Node
}

// NewNetwork creates a network manager with an optional initial set of HTTP peers.
func NewNetwork(initialPeers []string) *Network {
	peers := make([]string, 0, len(initialPeers))
	peers = append(peers, initialPeers...)
	return &Network{
		httpPeers: peers,
		client:    &http.Client{Timeout: httpTimeout},
	}
}

// SetTCPNode attaches the TCP P2P node to the network layer.
func (n *Network) SetTCPNode(node *p2p.Node) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.TCPNode = node
}

// AddPeer adds a peer URL (HTTP) or address (TCP) if it isn't already known.
func (n *Network) AddPeer(url string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, p := range n.httpPeers {
		if p == url {
			return
		}
	}
	n.httpPeers = append(n.httpPeers, url)
	slog.Info("HTTP peer added", "peer", url, "total", len(n.httpPeers))

	// Also add to TCP node if available.
	if n.TCPNode != nil {
		go n.TCPNode.AddPeer(url)
	}
}

// GetPeers returns a combined list of HTTP and TCP peers.
func (n *Network) GetPeers() []string {
	n.mu.RLock()
	defer n.mu.RUnlock()

	combined := make([]string, len(n.httpPeers))
	copy(combined, n.httpPeers)

	// Add TCP peers.
	if n.TCPNode != nil {
		tcpPeers := n.TCPNode.GetPeers()
		for _, tp := range tcpPeers {
			// Avoid duplicates.
			found := false
			for _, hp := range combined {
				if hp == tp {
					found = true
					break
				}
			}
			if !found {
				combined = append(combined, tp)
			}
		}
	}

	return combined
}

// BroadcastTransaction sends a transaction to all known peers.
// Uses TCP P2P if available, falls back to HTTP.
func (n *Network) BroadcastTransaction(tx block.Transaction) {
	// TCP broadcast (preferred).
	n.mu.RLock()
	tcpNode := n.TCPNode
	n.mu.RUnlock()

	if tcpNode != nil {
		tcpNode.BroadcastTransaction(tx)
	}

	// HTTP broadcast (legacy fallback).
	n.httpBroadcastTransaction(tx)
}

// httpBroadcastTransaction sends a transaction to HTTP peers.
func (n *Network) httpBroadcastTransaction(tx block.Transaction) {
	// Send the full UTXO transaction with inputs/outputs.
	body, err := json.Marshal(map[string]interface{}{
		"version":       tx.Version,
		"inputs":        tx.Inputs,
		"outputs":       tx.Outputs,
		"lock_time":     tx.LockTime,
		"coinbase_data": tx.CoinbaseData,
	})
	if err != nil {
		slog.Error("Broadcast marshal error", "error", err)
		return
	}

	peers := n.getHTTPPeers()
	for _, peer := range peers {
		go func(peerURL string) {
			url := peerURL + "/transaction"
			resp, err := n.client.Post(url, "application/json", bytes.NewReader(body))
			if err != nil {
				slog.Debug("Broadcast failed", "peer", peerURL, "error", err)
				return
			}
			resp.Body.Close()
			slog.Debug("Broadcast sent", "peer", peerURL, "status", resp.StatusCode)
		}(peer)
	}
}

// SyncChain fetches the chain from peers and adopts the longest valid one.
// Uses TCP P2P if available, falls back to HTTP.
func (n *Network) SyncChain(l *ledger.Ledger) bool {
	// Try TCP sync first.
	n.mu.RLock()
	tcpNode := n.TCPNode
	n.mu.RUnlock()

	if tcpNode != nil {
		if tcpNode.SyncChain() {
			return true
		}
	}

	// HTTP fallback.
	return n.httpSyncChain(l)
}

// httpSyncChain syncs the chain from HTTP peers.
func (n *Network) httpSyncChain(l *ledger.Ledger) bool {
	peers := n.getHTTPPeers()
	replaced := false

	for _, peer := range peers {
		peerChain, err := n.fetchChain(peer)
		if err != nil {
			slog.Debug("Sync from peer failed", "peer", peer, "error", err)
			continue
		}
		if l.ReplaceChain(peerChain) {
			slog.Info("Chain replaced from peer", "peer", peer, "length", peerChain.Len())
			replaced = true
		}
	}
	return replaced
}

// getHTTPPeers returns a copy of the HTTP peer list.
func (n *Network) getHTTPPeers() []string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	cp := make([]string, len(n.httpPeers))
	copy(cp, n.httpPeers)
	return cp
}

// fetchChain retrieves the blockchain JSON from a single HTTP peer.
func (n *Network) fetchChain(peerURL string) (*chain.Blockchain, error) {
	url := peerURL + "/chain"
	resp, err := n.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	bc, err := chain.FromJSON(data)
	if err != nil {
		return nil, fmt.Errorf("decode chain: %w", err)
	}
	return bc, nil
}

// PeerCount returns the total number of connected peers (HTTP + TCP).
func (n *Network) PeerCount() int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	count := len(n.httpPeers)
	if n.TCPNode != nil {
		count += n.TCPNode.PeerCount()
	}
	return count
}
