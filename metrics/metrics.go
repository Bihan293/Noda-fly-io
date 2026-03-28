// Package metrics provides Prometheus-compatible metrics collection for the Noda node.
//
// All metrics are exported in Prometheus text exposition format via a simple HTTP handler.
// No external dependencies — uses only the Go standard library.
//
// Supported metric types:
//   - Counter (monotonically increasing value)
//   - Gauge   (value that can go up and down)
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// ──────────────────────────────────────────────────────────────────────────────
// Metric types
// ──────────────────────────────────────────────────────────────────────────────

// Counter is a monotonically increasing counter.
type Counter struct {
	val atomic.Int64
}

// Inc increments the counter by 1.
func (c *Counter) Inc() { c.val.Add(1) }

// Add increments the counter by n.
func (c *Counter) Add(n int64) { c.val.Add(n) }

// Value returns the current counter value.
func (c *Counter) Value() int64 { return c.val.Load() }

// Gauge is a value that can go up and down.
type Gauge struct {
	val atomic.Int64
}

// Set sets the gauge to a specific value.
func (g *Gauge) Set(n int64) { g.val.Store(n) }

// Inc increments the gauge by 1.
func (g *Gauge) Inc() { g.val.Add(1) }

// Dec decrements the gauge by 1.
func (g *Gauge) Dec() { g.val.Add(-1) }

// Add adds n to the gauge.
func (g *Gauge) Add(n int64) { g.val.Add(n) }

// Value returns the current gauge value.
func (g *Gauge) Value() int64 { return g.val.Load() }

// FloatGauge is a gauge that stores a float64 value.
type FloatGauge struct {
	val atomic.Value // stores float64
}

// NewFloatGauge creates a new FloatGauge initialized to 0.
func NewFloatGauge() *FloatGauge {
	fg := &FloatGauge{}
	fg.val.Store(float64(0))
	return fg
}

// Set sets the float gauge to a specific value.
func (fg *FloatGauge) Set(v float64) { fg.val.Store(v) }

// Value returns the current float gauge value.
func (fg *FloatGauge) Value() float64 { return fg.val.Load().(float64) }

// ──────────────────────────────────────────────────────────────────────────────
// Registry — singleton metrics registry
// ──────────────────────────────────────────────────────────────────────────────

type metricEntry struct {
	name     string
	help     string
	mtype    string // "counter" or "gauge"
	valueFn  func() string
}

var (
	registryMu sync.RWMutex
	registry   []metricEntry
)

// RegisterCounter registers a named counter with help text.
func RegisterCounter(name, help string, c *Counter) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = append(registry, metricEntry{
		name:  name,
		help:  help,
		mtype: "counter",
		valueFn: func() string {
			return fmt.Sprintf("%d", c.Value())
		},
	})
}

// RegisterGauge registers a named gauge with help text.
func RegisterGauge(name, help string, g *Gauge) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = append(registry, metricEntry{
		name:  name,
		help:  help,
		mtype: "gauge",
		valueFn: func() string {
			return fmt.Sprintf("%d", g.Value())
		},
	})
}

// RegisterFloatGauge registers a named float gauge with help text.
func RegisterFloatGauge(name, help string, fg *FloatGauge) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = append(registry, metricEntry{
		name:  name,
		help:  help,
		mtype: "gauge",
		valueFn: func() string {
			return fmt.Sprintf("%g", fg.Value())
		},
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// Global Metrics — Noda-specific
// ──────────────────────────────────────────────────────────────────────────────

var (
	// Blockchain
	BlockHeight  = &Gauge{}
	BlockCount   = &Gauge{}
	TotalMined   = NewFloatGauge()
	TotalFaucet  = NewFloatGauge()
	BlockReward  = NewFloatGauge()
	Difficulty   = NewFloatGauge()

	// Mempool
	MempoolSize = &Gauge{}

	// UTXO
	UTXOCount = &Gauge{}

	// Network
	PeerCount     = &Gauge{}
	HTTPPeerCount = &Gauge{}
	TCPPeerCount  = &Gauge{}

	// Faucet
	FaucetRemaining = NewFloatGauge()
	FaucetActive    = &Gauge{} // 1=active, 0=disabled

	// API
	HTTPRequestsTotal   = &Counter{}
	HTTPRequestDuration = NewFloatGauge() // last request duration in ms (simplified)

	// Transaction counters
	TxAccepted = &Counter{}
	TxRejected = &Counter{}

	// Mining
	BlocksMined = &Counter{}

	// P2P
	P2PMessagesReceived = &Counter{}
	P2PMessagesSent     = &Counter{}
)

func init() {
	// Blockchain metrics
	RegisterGauge("noda_block_height", "Current blockchain height", BlockHeight)
	RegisterGauge("noda_block_count", "Total number of blocks in chain", BlockCount)
	RegisterFloatGauge("noda_total_mined_coins", "Total coins created through mining", TotalMined)
	RegisterFloatGauge("noda_total_faucet_coins", "Total coins distributed via faucet", TotalFaucet)
	RegisterFloatGauge("noda_block_reward", "Current block mining reward", BlockReward)
	RegisterFloatGauge("noda_difficulty", "Current mining difficulty (target value)", Difficulty)

	// Mempool
	RegisterGauge("noda_mempool_size", "Number of pending transactions in mempool", MempoolSize)

	// UTXO
	RegisterGauge("noda_utxo_count", "Number of unspent transaction outputs", UTXOCount)

	// Network
	RegisterGauge("noda_peer_count_total", "Total connected peers (HTTP + TCP)", PeerCount)
	RegisterGauge("noda_http_peer_count", "Number of HTTP peers", HTTPPeerCount)
	RegisterGauge("noda_tcp_peer_count", "Number of TCP peers", TCPPeerCount)

	// Faucet
	RegisterFloatGauge("noda_faucet_remaining_coins", "Remaining faucet distribution capacity", FaucetRemaining)
	RegisterGauge("noda_faucet_active", "Whether faucet is active (1) or disabled (0)", FaucetActive)

	// API
	RegisterCounter("noda_http_requests_total", "Total HTTP requests processed", HTTPRequestsTotal)
	RegisterFloatGauge("noda_http_request_duration_ms", "Duration of last HTTP request in milliseconds", HTTPRequestDuration)

	// Transactions
	RegisterCounter("noda_tx_accepted_total", "Total transactions accepted", TxAccepted)
	RegisterCounter("noda_tx_rejected_total", "Total transactions rejected", TxRejected)

	// Mining
	RegisterCounter("noda_blocks_mined_total", "Total blocks mined by this node", BlocksMined)

	// P2P
	RegisterCounter("noda_p2p_messages_received_total", "Total P2P messages received", P2PMessagesReceived)
	RegisterCounter("noda_p2p_messages_sent_total", "Total P2P messages sent", P2PMessagesSent)
}

// ──────────────────────────────────────────────────────────────────────────────
// HTTP Handler for /metrics
// ──────────────────────────────────────────────────────────────────────────────

// Handler returns an http.HandlerFunc that serves metrics in Prometheus text format.
func Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		registryMu.RLock()
		entries := make([]metricEntry, len(registry))
		copy(entries, registry)
		registryMu.RUnlock()

		// Sort by name for deterministic output.
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].name < entries[j].name
		})

		var sb strings.Builder
		for _, e := range entries {
			sb.WriteString(fmt.Sprintf("# HELP %s %s\n", e.name, e.help))
			sb.WriteString(fmt.Sprintf("# TYPE %s %s\n", e.name, e.mtype))
			sb.WriteString(fmt.Sprintf("%s %s\n", e.name, e.valueFn()))
		}

		w.Write([]byte(sb.String()))
	}
}
