package p2p

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/Bihan293/Noda/block"
	"github.com/Bihan293/Noda/ledger"
)

// ──────────────────────────────────────────────────────────────────────────────
// Constants
// ──────────────────────────────────────────────────────────────────────────────

const (
	// ProtocolVersion is the current P2P protocol version.
	// HIGH-2: Bumped to 2 for checksum-aware header format.
	ProtocolVersion uint32 = 2

	// UserAgent identifies this node implementation.
	UserAgent = "/Noda:0.6.0/"

	// MaxOutboundPeers is the maximum number of outbound connections.
	MaxOutboundPeers = 8

	// MaxInboundPeers is the maximum number of inbound connections.
	MaxInboundPeers = 32

	// MaxGetBlocksLimit is the max number of blocks per getblocks response.
	MaxGetBlocksLimit = 500

	// PingInterval is how often we send pings to keep connections alive.
	PingInterval = 2 * time.Minute

	// PingTimeout is how long we wait for a pong before disconnecting.
	PingTimeout = 30 * time.Second

	// HandshakeTimeout is how long to wait for the handshake to complete.
	HandshakeTimeout = 10 * time.Second

	// ReconnectInterval is the time between reconnection attempts.
	ReconnectInterval = 30 * time.Second

	// BanDuration is how long a misbehaving peer is banned.
	BanDuration = 24 * time.Hour

	// MaxBanScore is the score at which a peer gets banned.
	MaxBanScore = 100

	// ── HIGH-2: Per-peer rate limiting ──

	// PeerMsgRateWindow is the sliding window for per-peer message rate limiting.
	PeerMsgRateWindow = 10 * time.Second

	// PeerMaxMsgsPerWindow is the maximum messages a peer can send within PeerMsgRateWindow.
	PeerMaxMsgsPerWindow = 200

	// ── HIGH-2: Relay deduplication ──

	// RecentRelayCapacity is the maximum number of recently-relayed hashes to track.
	RecentRelayCapacity = 10000

	// MaxRelayFanOut is the max number of peers to relay a single inv to.
	MaxRelayFanOut = 8

	// ── HIGH-2: Ban score values ──

	BanScoreBadChecksum    = 100 // instant ban
	BanScoreInvalidPayload = 25
	BanScoreInvFlood       = 50
	BanScoreAddrFlood      = 30
	BanScoreBadAddr        = 10
	BanScoreUnknownCmd     = 5
	BanScoreInvalidBlock   = 20
	BanScoreDuplicateHS    = 10
	BanScoreDefault        = 10
)

// ──────────────────────────────────────────────────────────────────────────────
// Peer
// ──────────────────────────────────────────────────────────────────────────────

// PeerState represents the connection state of a peer.
type PeerState int

const (
	PeerStateConnecting PeerState = iota
	PeerStateHandshaking
	PeerStateActive
	PeerStateDisconnected
)

// Peer represents a connected remote node.
type Peer struct {
	Conn       net.Conn
	Addr       string    // host:port
	NodeID     string    // remote node's unique ID
	Version    uint32    // remote protocol version
	BestHeight uint64    // remote best block height
	State      PeerState // connection state
	Inbound    bool      // true if the peer connected to us
	LastPing   time.Time // last ping sent
	LastPong   time.Time // last pong received
	PingNonce  uint64    // current outstanding ping nonce
	BanScore   int       // misbehavior score
	CreatedAt  time.Time // connection established time

	// Inventory tracking — hashes we know this peer has.
	KnownBlocks map[string]bool
	KnownTxs    map[string]bool

	// HIGH-2: Per-peer rate limiting.
	msgTimestamps []time.Time // sliding window of message arrival times
	getDataCount  int         // outstanding getdata requests

	mu   sync.RWMutex
	done chan struct{} // closed when peer is disconnected
}

// NewPeer creates a new peer from a connection.
func NewPeer(conn net.Conn, inbound bool) *Peer {
	return &Peer{
		Conn:        conn,
		Addr:        conn.RemoteAddr().String(),
		State:       PeerStateConnecting,
		Inbound:     inbound,
		CreatedAt:   time.Now(),
		KnownBlocks: make(map[string]bool),
		KnownTxs:    make(map[string]bool),
		done:        make(chan struct{}),
	}
}

// Send sends a message to the peer.
func (p *Peer) Send(msg *Message) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.State == PeerStateDisconnected {
		return fmt.Errorf("peer %s is disconnected", p.Addr)
	}
	return WriteMessage(p.Conn, msg)
}

// Disconnect closes the connection and marks the peer as disconnected.
func (p *Peer) Disconnect() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.State == PeerStateDisconnected {
		return
	}
	p.State = PeerStateDisconnected
	p.Conn.Close()
	close(p.done)
}

// AddBanScore increases the peer's ban score. Returns true if the peer should be banned.
func (p *Peer) AddBanScore(score int, reason string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.BanScore += score
	if p.BanScore >= MaxBanScore {
		slog.Warn("Peer banned", "peer", p.Addr, "score", p.BanScore, "reason", reason)
		return true
	}
	slog.Debug("Peer ban score increased", "peer", p.Addr, "score", p.BanScore, "added", score, "reason", reason)
	return false
}

// MarkKnownBlock records that this peer has a block.
func (p *Peer) MarkKnownBlock(hash string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.KnownBlocks[hash] = true
}

// MarkKnownTx records that this peer has a transaction.
func (p *Peer) MarkKnownTx(hash string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.KnownTxs[hash] = true
}

// HasBlock checks if we know this peer has a block.
func (p *Peer) HasBlock(hash string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.KnownBlocks[hash]
}

// HasTx checks if we know this peer has a transaction.
func (p *Peer) HasTx(hash string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.KnownTxs[hash]
}

// RecordMessage records a message arrival for rate limiting.
// Returns false if the peer has exceeded the rate limit.
// HIGH-2: per-peer message rate limiting.
func (p *Peer) RecordMessage() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-PeerMsgRateWindow)

	// Trim old timestamps.
	valid := 0
	for _, ts := range p.msgTimestamps {
		if ts.After(cutoff) {
			p.msgTimestamps[valid] = ts
			valid++
		}
	}
	p.msgTimestamps = p.msgTimestamps[:valid]

	if len(p.msgTimestamps) >= PeerMaxMsgsPerWindow {
		return false // rate limit exceeded
	}

	p.msgTimestamps = append(p.msgTimestamps, now)
	return true
}

// IncrGetData increments the outstanding getdata counter.
// Returns false if the limit is exceeded.
func (p *Peer) IncrGetData(count int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.getDataCount+count > MaxGetDataOutstanding {
		return false
	}
	p.getDataCount += count
	return true
}

// DecrGetData decrements the outstanding getdata counter.
func (p *Peer) DecrGetData(count int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.getDataCount -= count
	if p.getDataCount < 0 {
		p.getDataCount = 0
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Node — the P2P network layer
// ──────────────────────────────────────────────────────────────────────────────

// Node manages TCP peer connections and implements the P2P protocol.
type Node struct {
	listenPort uint16
	nodeID     string
	ledger     *ledger.Ledger
	listener   net.Listener

	peers     map[string]*Peer      // addr -> peer
	banned    map[string]time.Time  // addr -> ban expiry
	seedAddrs []string              // initial seed addresses to connect to
	mu        sync.RWMutex

	// HIGH-2: Track NodeIDs of connected peers for self/dup-connection detection.
	connectedNodeIDs map[string]string // nodeID -> addr

	// HIGH-2: Relay deduplication — track recently relayed hashes.
	recentRelay     map[string]time.Time
	recentRelayMu   sync.Mutex

	quit chan struct{}
	wg   sync.WaitGroup
}

// NewNode creates a new P2P node.
func NewNode(listenPort uint16, l *ledger.Ledger, seeds []string) *Node {
	return &Node{
		listenPort:       listenPort,
		nodeID:           generateNodeID(),
		ledger:           l,
		peers:            make(map[string]*Peer),
		banned:           make(map[string]time.Time),
		seedAddrs:        seeds,
		connectedNodeIDs: make(map[string]string),
		recentRelay:      make(map[string]time.Time),
		quit:             make(chan struct{}),
	}
}

// generateNodeID creates a random 16-byte hex node identifier.
func generateNodeID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// NodeID returns the node's unique identifier.
func (n *Node) NodeID() string {
	return n.nodeID
}

// Start begins listening for incoming connections and connects to seed peers.
func (n *Node) Start() error {
	addr := fmt.Sprintf("0.0.0.0:%d", n.listenPort)
	var err error
	n.listener, err = net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("P2P listen failed on %s: %w", addr, err)
	}

	slog.Info("P2P listening", "address", addr, "node_id", n.nodeID[:16])

	// Accept incoming connections.
	n.wg.Add(1)
	go n.acceptLoop()

	// Connect to seed peers.
	n.wg.Add(1)
	go n.connectToSeeds()

	// Reconnection loop.
	n.wg.Add(1)
	go n.reconnectLoop()

	// HIGH-2: Periodic relay cache cleanup.
	n.wg.Add(1)
	go n.relayCleanupLoop()

	return nil
}

// Stop shuts down the P2P node.
func (n *Node) Stop() {
	close(n.quit)
	if n.listener != nil {
		n.listener.Close()
	}

	// Disconnect all peers.
	n.mu.RLock()
	for _, peer := range n.peers {
		peer.Disconnect()
	}
	n.mu.RUnlock()

	n.wg.Wait()
	slog.Info("P2P node stopped")
}

// ──────────────────────────────────────────────────────────────────────────────
// Connection Management
// ──────────────────────────────────────────────────────────────────────────────

// acceptLoop accepts incoming TCP connections.
func (n *Node) acceptLoop() {
	defer n.wg.Done()
	for {
		conn, err := n.listener.Accept()
		if err != nil {
			select {
			case <-n.quit:
				return
			default:
				slog.Error("P2P accept error", "error", err)
				continue
			}
		}

		remoteAddr := conn.RemoteAddr().String()

		// Check if banned.
		if n.isBanned(remoteAddr) {
			slog.Debug("Rejected banned peer", "peer", remoteAddr)
			conn.Close()
			continue
		}

		// Check inbound limit.
		if n.inboundCount() >= MaxInboundPeers {
			slog.Debug("Inbound limit reached, rejecting", "peer", remoteAddr)
			conn.Close()
			continue
		}

		slog.Debug("Inbound connection", "peer", remoteAddr)
		peer := NewPeer(conn, true)
		n.addPeer(peer)

		n.wg.Add(1)
		go n.handlePeer(peer)
	}
}

// connectToSeeds connects to all seed addresses.
func (n *Node) connectToSeeds() {
	defer n.wg.Done()
	for _, addr := range n.seedAddrs {
		select {
		case <-n.quit:
			return
		default:
		}
		n.connectOutbound(addr)
	}
}

// connectOutbound establishes an outbound connection to a peer.
func (n *Node) connectOutbound(addr string) {
	if n.isBanned(addr) {
		return
	}

	// Check if already connected.
	n.mu.RLock()
	if _, exists := n.peers[addr]; exists {
		n.mu.RUnlock()
		return
	}
	n.mu.RUnlock()

	// Check outbound limit.
	if n.outboundCount() >= MaxOutboundPeers {
		return
	}

	slog.Debug("Connecting to peer", "peer", addr)
	conn, err := net.DialTimeout("tcp", addr, HandshakeTimeout)
	if err != nil {
		slog.Debug("Failed to connect", "peer", addr, "error", err)
		return
	}

	peer := NewPeer(conn, false)
	n.addPeer(peer)

	n.wg.Add(1)
	go n.handlePeer(peer)
}

// reconnectLoop periodically reconnects to seed peers.
func (n *Node) reconnectLoop() {
	defer n.wg.Done()
	ticker := time.NewTicker(ReconnectInterval)
	defer ticker.Stop()

	for {
		select {
		case <-n.quit:
			return
		case <-ticker.C:
			for _, addr := range n.seedAddrs {
				n.connectOutbound(addr)
			}
		}
	}
}

// relayCleanupLoop periodically cleans up the relay deduplication cache.
// HIGH-2: Prevents unbounded memory growth.
func (n *Node) relayCleanupLoop() {
	defer n.wg.Done()
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-n.quit:
			return
		case <-ticker.C:
			n.cleanRecentRelay()
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Peer Management
// ──────────────────────────────────────────────────────────────────────────────

func (n *Node) addPeer(p *Peer) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.peers[p.Addr] = p
}

func (n *Node) removePeer(p *Peer) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.peers, p.Addr)
	// HIGH-2: Clean up nodeID tracking.
	if p.NodeID != "" {
		delete(n.connectedNodeIDs, p.NodeID)
	}
}

func (n *Node) getPeer(addr string) *Peer {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.peers[addr]
}

// registerNodeID registers a peer's nodeID after handshake.
// Returns false if the nodeID is already connected (duplicate connection).
// HIGH-2: prevents duplicate connections to the same node via different addresses.
func (n *Node) registerNodeID(p *Peer) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if existingAddr, exists := n.connectedNodeIDs[p.NodeID]; exists {
		slog.Debug("Duplicate node ID detected",
			"node_id", shortID(p.NodeID),
			"existing_addr", existingAddr,
			"new_addr", p.Addr,
		)
		return false
	}
	n.connectedNodeIDs[p.NodeID] = p.Addr
	return true
}

func (n *Node) inboundCount() int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	count := 0
	for _, p := range n.peers {
		if p.Inbound && p.State != PeerStateDisconnected {
			count++
		}
	}
	return count
}

func (n *Node) outboundCount() int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	count := 0
	for _, p := range n.peers {
		if !p.Inbound && p.State != PeerStateDisconnected {
			count++
		}
	}
	return count
}

func (n *Node) isBanned(addr string) bool {
	host, _, _ := net.SplitHostPort(addr)
	if host == "" {
		host = addr
	}
	n.mu.RLock()
	defer n.mu.RUnlock()
	expiry, ok := n.banned[host]
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		// Ban expired — clean up (will be done on next write lock).
		return false
	}
	return true
}

func (n *Node) banPeer(p *Peer) {
	host, _, _ := net.SplitHostPort(p.Addr)
	if host == "" {
		host = p.Addr
	}
	n.mu.Lock()
	n.banned[host] = time.Now().Add(BanDuration)
	n.mu.Unlock()
	p.Disconnect()
	n.removePeer(p)
	slog.Warn("Peer banned", "peer", p.Addr, "duration", BanDuration)
}

// GetPeers returns addresses of all connected peers (for HTTP API compatibility).
func (n *Node) GetPeers() []string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	addrs := make([]string, 0, len(n.peers))
	for addr, p := range n.peers {
		if p.State == PeerStateActive {
			addrs = append(addrs, addr)
		}
	}
	return addrs
}

// PeerCount returns the number of active peers.
func (n *Node) PeerCount() int {
	return len(n.GetPeers())
}

// ──────────────────────────────────────────────────────────────────────────────
// Relay Deduplication (HIGH-2)
// ──────────────────────────────────────────────────────────────────────────────

// markRelayed records that we have relayed a hash. Returns false if it was
// already relayed recently (dedup hit).
func (n *Node) markRelayed(hash string) bool {
	n.recentRelayMu.Lock()
	defer n.recentRelayMu.Unlock()

	if _, exists := n.recentRelay[hash]; exists {
		return false // already relayed
	}
	n.recentRelay[hash] = time.Now()
	return true
}

// cleanRecentRelay removes entries older than 10 minutes and trims to capacity.
func (n *Node) cleanRecentRelay() {
	n.recentRelayMu.Lock()
	defer n.recentRelayMu.Unlock()

	cutoff := time.Now().Add(-10 * time.Minute)
	for hash, ts := range n.recentRelay {
		if ts.Before(cutoff) {
			delete(n.recentRelay, hash)
		}
	}
	// Hard cap on capacity.
	if len(n.recentRelay) > RecentRelayCapacity {
		count := 0
		for hash := range n.recentRelay {
			if count > RecentRelayCapacity/2 {
				delete(n.recentRelay, hash)
			}
			count++
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Peer Address Validation (HIGH-2)
// ──────────────────────────────────────────────────────────────────────────────

// isValidPeerAddress checks that a peer address is valid for relay / connection.
// Rejects loopback, link-local, unspecified and private ranges unless the addr
// is an explicit seed.
func isValidPeerAddress(ip string, port uint16) bool {
	if port == 0 || port > 65535 {
		return false
	}

	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}

	// Reject unspecified (0.0.0.0, ::).
	if parsed.IsUnspecified() {
		return false
	}

	// Reject loopback (127.0.0.0/8, ::1).
	if parsed.IsLoopback() {
		return false
	}

	// Reject link-local (169.254.0.0/16, fe80::/10).
	if parsed.IsLinkLocalUnicast() || parsed.IsLinkLocalMulticast() {
		return false
	}

	// Reject multicast.
	if parsed.IsMulticast() {
		return false
	}

	// Reject private ranges for public relay (RFC 1918 + RFC 4193).
	if isPrivateIP(parsed) {
		return false
	}

	return true
}

// isPrivateIP checks common private IP ranges.
func isPrivateIP(ip net.IP) bool {
	privateRanges := []struct {
		network string
	}{
		{"10.0.0.0/8"},
		{"172.16.0.0/12"},
		{"192.168.0.0/16"},
		{"fc00::/7"},
	}
	for _, r := range privateRanges {
		_, cidr, err := net.ParseCIDR(r.network)
		if err != nil {
			continue
		}
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// ──────────────────────────────────────────────────────────────────────────────
// Peer Handler — main message loop
// ──────────────────────────────────────────────────────────────────────────────

// handlePeer manages the lifecycle of a single peer connection.
func (n *Node) handlePeer(p *Peer) {
	defer n.wg.Done()
	defer func() {
		p.Disconnect()
		n.removePeer(p)
		slog.Debug("Peer disconnected", "peer", p.Addr)
	}()

	// Perform handshake.
	if err := n.doHandshake(p); err != nil {
		slog.Debug("Handshake failed", "peer", p.Addr, "error", err)
		return
	}

	// HIGH-2: Check for duplicate node ID (same node via different address).
	if !n.registerNodeID(p) {
		slog.Debug("Dropping duplicate connection to same node", "peer", p.Addr, "node_id", shortID(p.NodeID))
		return
	}

	slog.Info("Peer connected",
		"peer", p.Addr,
		"version", p.Version,
		"height", p.BestHeight,
		"node_id", shortID(p.NodeID),
	)

	// Start ping loop.
	n.wg.Add(1)
	go n.pingLoop(p)

	// Trigger IBD if peer has more blocks.
	localHeight := n.ledger.GetChainHeight()
	if p.BestHeight > localHeight {
		slog.Info("Starting IBD from peer",
			"peer", p.Addr,
			"peer_height", p.BestHeight,
			"local_height", localHeight,
		)
		n.requestBlocks(p)
	}

	// Main message loop.
	for {
		select {
		case <-n.quit:
			return
		case <-p.done:
			return
		default:
		}

		msg, err := ReadMessage(p.Conn)
		if err != nil {
			// Check if it's a timeout (non-fatal, we keep the connection).
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}

			// HIGH-2: Bad checksum → instant ban.
			if err == ErrBadChecksum {
				slog.Warn("Bad checksum from peer", "peer", p.Addr)
				if banned := p.AddBanScore(BanScoreBadChecksum, "bad checksum"); banned {
					n.banPeer(p)
				}
				return
			}
			// HIGH-2: Bad magic → disconnect immediately.
			if err == ErrBadMagic {
				slog.Debug("Bad magic from peer", "peer", p.Addr)
				return
			}

			slog.Debug("Read error from peer", "peer", p.Addr, "error", err)
			return
		}

		// HIGH-2: Per-peer message rate limiting.
		if !p.RecordMessage() {
			slog.Warn("Peer exceeded message rate limit", "peer", p.Addr)
			if banned := p.AddBanScore(BanScoreInvFlood, "message rate limit exceeded"); banned {
				n.banPeer(p)
			}
			return
		}

		if err := n.handleMessage(p, msg); err != nil {
			slog.Debug("Error handling message",
				"command", msg.Command,
				"peer", p.Addr,
				"error", err,
			)
			if banned := p.AddBanScore(BanScoreDefault, err.Error()); banned {
				n.banPeer(p)
				return
			}
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Handshake
// ──────────────────────────────────────────────────────────────────────────────

// doHandshake performs the version/verack exchange.
func (n *Node) doHandshake(p *Peer) error {
	p.mu.Lock()
	p.State = PeerStateHandshaking
	p.mu.Unlock()

	versionPayload := &VersionPayload{
		Version:    ProtocolVersion,
		BestHeight: n.ledger.GetChainHeight(),
		ListenPort: n.listenPort,
		UserAgent:  UserAgent,
		Timestamp:  time.Now().Unix(),
		NodeID:     n.nodeID,
	}

	// Outbound: we send version first.
	if !p.Inbound {
		if err := n.sendVersion(p, versionPayload); err != nil {
			return fmt.Errorf("send version: %w", err)
		}

		// Wait for their version.
		msg, err := n.readWithTimeout(p.Conn, HandshakeTimeout)
		if err != nil {
			return fmt.Errorf("read version: %w", err)
		}
		if msg.Command != CmdVersion {
			return fmt.Errorf("expected version, got %s", msg.Command)
		}
		if err := n.processVersion(p, msg); err != nil {
			return err
		}

		// Send verack.
		if err := n.sendVerack(p); err != nil {
			return fmt.Errorf("send verack: %w", err)
		}

		// Wait for verack.
		msg, err = n.readWithTimeout(p.Conn, HandshakeTimeout)
		if err != nil {
			return fmt.Errorf("read verack: %w", err)
		}
		if msg.Command != CmdVerack {
			return fmt.Errorf("expected verack, got %s", msg.Command)
		}
	} else {
		// Inbound: wait for their version first.
		msg, err := n.readWithTimeout(p.Conn, HandshakeTimeout)
		if err != nil {
			return fmt.Errorf("read version: %w", err)
		}
		if msg.Command != CmdVersion {
			return fmt.Errorf("expected version, got %s", msg.Command)
		}
		if err := n.processVersion(p, msg); err != nil {
			return err
		}

		// Send our version.
		if err := n.sendVersion(p, versionPayload); err != nil {
			return fmt.Errorf("send version: %w", err)
		}

		// Send verack.
		if err := n.sendVerack(p); err != nil {
			return fmt.Errorf("send verack: %w", err)
		}

		// Wait for verack.
		msg, err = n.readWithTimeout(p.Conn, HandshakeTimeout)
		if err != nil {
			return fmt.Errorf("read verack: %w", err)
		}
		if msg.Command != CmdVerack {
			return fmt.Errorf("expected verack, got %s", msg.Command)
		}
	}

	p.mu.Lock()
	p.State = PeerStateActive
	p.mu.Unlock()

	return nil
}

func (n *Node) sendVersion(p *Peer, vp *VersionPayload) error {
	msg, err := NewMessage(CmdVersion, vp)
	if err != nil {
		return err
	}
	return p.Send(msg)
}

func (n *Node) sendVerack(p *Peer) error {
	msg := &Message{Command: CmdVerack}
	return p.Send(msg)
}

func (n *Node) processVersion(p *Peer, msg *Message) error {
	var vp VersionPayload
	if err := DecodePayload(msg.Payload, &vp); err != nil {
		return fmt.Errorf("decode version payload: %w", err)
	}

	// HIGH-2: Reject self-connections by NodeID.
	if vp.NodeID == n.nodeID {
		return fmt.Errorf("detected self-connection")
	}

	// HIGH-2: Validate NodeID is non-empty.
	if vp.NodeID == "" {
		return fmt.Errorf("peer sent empty node_id")
	}

	// HIGH-2: Validate UserAgent is reasonable length.
	if len(vp.UserAgent) > 256 {
		return fmt.Errorf("user_agent too long (%d bytes)", len(vp.UserAgent))
	}

	p.mu.Lock()
	p.Version = vp.Version
	p.BestHeight = vp.BestHeight
	p.NodeID = vp.NodeID
	p.mu.Unlock()

	return nil
}

func (n *Node) readWithTimeout(conn net.Conn, timeout time.Duration) (*Message, error) {
	conn.SetReadDeadline(time.Now().Add(timeout))
	return ReadMessage(conn)
}

// ──────────────────────────────────────────────────────────────────────────────
// Message Handling
// ──────────────────────────────────────────────────────────────────────────────

// handleMessage dispatches incoming messages to the appropriate handler.
func (n *Node) handleMessage(p *Peer, msg *Message) error {
	switch msg.Command {
	case CmdPing:
		return n.handlePing(p, msg)
	case CmdPong:
		return n.handlePong(p, msg)
	case CmdInv:
		return n.handleInv(p, msg)
	case CmdGetData:
		return n.handleGetData(p, msg)
	case CmdTx:
		return n.handleTx(p, msg)
	case CmdBlock:
		return n.handleBlock(p, msg)
	case CmdGetBlocks:
		return n.handleGetBlocks(p, msg)
	case CmdGetHeaders:
		return n.handleGetHeaders(p, msg)
	case CmdHeaders:
		return n.handleHeaders(p, msg)
	case CmdAddr:
		return n.handleAddr(p, msg)
	case CmdVersion:
		// Duplicate version — misbehaving.
		p.AddBanScore(BanScoreDuplicateHS, "duplicate version")
		return fmt.Errorf("unexpected duplicate version message")
	case CmdVerack:
		// Duplicate verack — misbehaving.
		p.AddBanScore(BanScoreDuplicateHS, "duplicate verack")
		return fmt.Errorf("unexpected duplicate verack message")
	default:
		slog.Debug("Unknown command from peer", "peer", p.Addr, "command", msg.Command)
		p.AddBanScore(BanScoreUnknownCmd, "unknown command: "+msg.Command)
		return nil
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Ping / Pong
// ──────────────────────────────────────────────────────────────────────────────

func (n *Node) pingLoop(p *Peer) {
	defer n.wg.Done()
	ticker := time.NewTicker(PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-n.quit:
			return
		case <-p.done:
			return
		case <-ticker.C:
			nonce := generateNonce()
			p.mu.Lock()
			p.PingNonce = nonce
			p.LastPing = time.Now()
			p.mu.Unlock()

			msg, _ := NewMessage(CmdPing, &PingPayload{Nonce: nonce})
			if err := p.Send(msg); err != nil {
				slog.Debug("Ping failed", "peer", p.Addr, "error", err)
				return
			}
		}
	}
}

func (n *Node) handlePing(p *Peer, msg *Message) error {
	var pp PingPayload
	if err := DecodePayload(msg.Payload, &pp); err != nil {
		p.AddBanScore(BanScoreInvalidPayload, "invalid ping payload")
		return fmt.Errorf("decode ping: %w", err)
	}
	// Respond with pong using the same nonce.
	resp, _ := NewMessage(CmdPong, &PingPayload{Nonce: pp.Nonce})
	return p.Send(resp)
}

func (n *Node) handlePong(p *Peer, msg *Message) error {
	var pp PingPayload
	if err := DecodePayload(msg.Payload, &pp); err != nil {
		p.AddBanScore(BanScoreInvalidPayload, "invalid pong payload")
		return fmt.Errorf("decode pong: %w", err)
	}
	p.mu.Lock()
	if pp.Nonce == p.PingNonce {
		p.LastPong = time.Now()
	}
	p.mu.Unlock()
	return nil
}

func generateNonce() uint64 {
	b := make([]byte, 8)
	rand.Read(b)
	return uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24 |
		uint64(b[4])<<32 | uint64(b[5])<<40 | uint64(b[6])<<48 | uint64(b[7])<<56
}

// ──────────────────────────────────────────────────────────────────────────────
// Inventory (inv / getdata)
// ──────────────────────────────────────────────────────────────────────────────

func (n *Node) handleInv(p *Peer, msg *Message) error {
	var inv InvPayload
	if err := DecodePayload(msg.Payload, &inv); err != nil {
		p.AddBanScore(BanScoreInvalidPayload, "invalid inv payload")
		return fmt.Errorf("decode inv: %w", err)
	}

	// HIGH-2: Enforce inv item limit.
	if len(inv.Items) > MaxInvItems {
		p.AddBanScore(BanScoreInvFlood, fmt.Sprintf("inv too large: %d items", len(inv.Items)))
		return fmt.Errorf("inv contains %d items (max %d)", len(inv.Items), MaxInvItems)
	}

	// Determine which items we need.
	var needed []InvItem
	for _, item := range inv.Items {
		// HIGH-2: Validate item type.
		if item.Type != InvTypeTx && item.Type != InvTypeBlock {
			p.AddBanScore(BanScoreInvalidPayload, fmt.Sprintf("invalid inv type: %d", item.Type))
			continue
		}
		// HIGH-2: Validate hash is non-empty and reasonable length.
		if len(item.Hash) == 0 || len(item.Hash) > 128 {
			p.AddBanScore(BanScoreInvalidPayload, "invalid inv hash length")
			continue
		}

		switch item.Type {
		case InvTypeBlock:
			p.MarkKnownBlock(item.Hash)
			// CRITICAL-4: Check block index instead of linear scan.
			bc := n.ledger.GetChain()
			idx := bc.Index
			if idx != nil && !idx.HasBlock(item.Hash) && !idx.HasOrphan(item.Hash) {
				needed = append(needed, item)
			} else if idx == nil {
				// Fallback: linear scan.
				found := false
				for _, b := range bc.Blocks {
					if b.Hash == item.Hash {
						found = true
						break
					}
				}
				if !found {
					needed = append(needed, item)
				}
			}
		case InvTypeTx:
			p.MarkKnownTx(item.Hash)
			// We request TXs we haven't seen.
			if !n.ledger.Mempool.Has(item.Hash) {
				needed = append(needed, item)
			}
		}
	}

	if len(needed) > 0 {
		// HIGH-2: Check getdata outstanding limit.
		if !p.IncrGetData(len(needed)) {
			slog.Debug("Peer getdata limit exceeded, skipping", "peer", p.Addr)
			return nil
		}
		resp, err := NewMessage(CmdGetData, &InvPayload{Items: needed})
		if err != nil {
			p.DecrGetData(len(needed))
			return err
		}
		return p.Send(resp)
	}

	return nil
}

func (n *Node) handleGetData(p *Peer, msg *Message) error {
	var inv InvPayload
	if err := DecodePayload(msg.Payload, &inv); err != nil {
		p.AddBanScore(BanScoreInvalidPayload, "invalid getdata payload")
		return fmt.Errorf("decode getdata: %w", err)
	}

	// HIGH-2: Enforce getdata item limit.
	if len(inv.Items) > MaxInvItems {
		p.AddBanScore(BanScoreInvFlood, fmt.Sprintf("getdata too large: %d items", len(inv.Items)))
		return fmt.Errorf("getdata contains %d items (max %d)", len(inv.Items), MaxInvItems)
	}

	bc := n.ledger.GetChain()

	for _, item := range inv.Items {
		switch item.Type {
		case InvTypeBlock:
			// CRITICAL-4: Use block index for O(1) lookup.
			b := bc.GetBlockByHash(item.Hash)
			if b != nil {
				resp, err := NewMessage(CmdBlock, b)
				if err != nil {
					return err
				}
				if err := p.Send(resp); err != nil {
					return fmt.Errorf("send block: %w", err)
				}
			}
		case InvTypeTx:
			// Look in mempool.
			tx := n.ledger.Mempool.Get(item.Hash)
			if tx != nil {
				resp, err := NewMessage(CmdTx, tx)
				if err != nil {
					return err
				}
				if err := p.Send(resp); err != nil {
					return fmt.Errorf("send tx: %w", err)
				}
			}
		}
	}

	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Transaction Relay
// ──────────────────────────────────────────────────────────────────────────────

func (n *Node) handleTx(p *Peer, msg *Message) error {
	var tx block.Transaction
	if err := DecodePayload(msg.Payload, &tx); err != nil {
		p.AddBanScore(BanScoreInvalidPayload, "invalid tx payload")
		return fmt.Errorf("decode tx: %w", err)
	}

	p.MarkKnownTx(tx.ID)
	p.DecrGetData(1)

	// HIGH-2: Validate transaction BEFORE relay.
	// Submit to our ledger — this performs full validation.
	if err := n.ledger.SubmitTransaction(tx); err != nil {
		// Not an error worth banning for — just log and skip.
		slog.Debug("TX from peer rejected", "peer", p.Addr, "error", err)
		return nil
	}

	slog.Info("TX accepted from peer",
		"peer", p.Addr,
		"inputs", len(tx.Inputs),
		"outputs", len(tx.Outputs),
	)

	// HIGH-2: Relay to other peers who don't know about it.
	// Only relay if we haven't recently relayed this hash.
	n.broadcastTx(&tx, p)

	return nil
}

// BroadcastTransaction announces a transaction to all connected peers.
func (n *Node) BroadcastTransaction(tx block.Transaction) {
	n.broadcastTx(&tx, nil)
}

func (n *Node) broadcastTx(tx *block.Transaction, exclude *Peer) {
	// HIGH-2: Relay deduplication.
	if !n.markRelayed(tx.ID) {
		return // already relayed recently
	}

	inv := &InvPayload{
		Items: []InvItem{{Type: InvTypeTx, Hash: tx.ID}},
	}
	msg, err := NewMessage(CmdInv, inv)
	if err != nil {
		slog.Error("Failed to create inv message for TX", "error", err)
		return
	}

	n.mu.RLock()
	defer n.mu.RUnlock()

	sent := 0
	for _, p := range n.peers {
		if p == exclude || p.State != PeerStateActive {
			continue
		}
		if p.HasTx(tx.ID) {
			continue
		}
		// HIGH-2: Limit fan-out.
		if sent >= MaxRelayFanOut {
			break
		}
		go p.Send(msg)
		sent++
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Block Relay
// ──────────────────────────────────────────────────────────────────────────────

func (n *Node) handleBlock(p *Peer, msg *Message) error {
	var b block.Block
	if err := DecodePayload(msg.Payload, &b); err != nil {
		p.AddBanScore(BanScoreInvalidPayload, "invalid block payload")
		return fmt.Errorf("decode block: %w", err)
	}

	p.MarkKnownBlock(b.Hash)
	p.DecrGetData(1)

	slog.Debug("Received block from peer",
		"peer", p.Addr,
		"height", b.Header.Height,
		"hash", shortHash(b.Hash),
	)

	// CRITICAL-4: Use ProcessBlock which handles orphans and reorg.
	// HIGH-2: ProcessBlock performs full validation BEFORE accepting.
	accepted, isOrphan, err := n.ledger.ProcessBlock(&b)
	if err != nil {
		slog.Debug("Block rejected",
			"height", b.Header.Height,
			"peer", p.Addr,
			"error", err,
		)
		if banned := p.AddBanScore(BanScoreInvalidBlock, "invalid block"); banned {
			n.banPeer(p)
		}
		return nil
	}

	if !accepted {
		// Duplicate block — ignore silently.
		return nil
	}

	if isOrphan {
		// We need the parent — request blocks from this peer.
		slog.Debug("Orphan block received, requesting missing blocks",
			"height", b.Header.Height,
			"peer", p.Addr,
		)
		n.requestBlocks(p)
		return nil
	}

	slog.Info("Block accepted from peer",
		"height", b.Header.Height,
		"peer", p.Addr,
		"hash", shortHash(b.Hash),
	)

	// HIGH-2: Relay to other peers (with dedup and fan-out limit).
	n.broadcastBlock(&b, p)

	return nil
}

// BroadcastBlock announces a new block to all connected peers.
func (n *Node) BroadcastBlock(b *block.Block) {
	n.broadcastBlock(b, nil)
}

func (n *Node) broadcastBlock(b *block.Block, exclude *Peer) {
	// HIGH-2: Relay deduplication.
	if !n.markRelayed(b.Hash) {
		return // already relayed recently
	}

	inv := &InvPayload{
		Items: []InvItem{{Type: InvTypeBlock, Hash: b.Hash}},
	}
	msg, err := NewMessage(CmdInv, inv)
	if err != nil {
		slog.Error("Failed to create inv message for block", "error", err)
		return
	}

	n.mu.RLock()
	defer n.mu.RUnlock()

	sent := 0
	for _, p := range n.peers {
		if p == exclude || p.State != PeerStateActive {
			continue
		}
		if p.HasBlock(b.Hash) {
			continue
		}
		// HIGH-2: Limit fan-out.
		if sent >= MaxRelayFanOut {
			break
		}
		go p.Send(msg)
		sent++
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// GetBlocks / Headers / Initial Block Download (IBD) — CRITICAL-4
// ──────────────────────────────────────────────────────────────────────────────

// requestBlocks sends a getblocks message to a peer to start IBD.
func (n *Node) requestBlocks(p *Peer) {
	bc := n.ledger.GetChain()
	fromHash := bc.LastHash()

	payload := &GetBlocksPayload{
		FromHash: fromHash,
		Limit:    MaxGetBlocksLimit,
	}

	msg, err := NewMessage(CmdGetBlocks, payload)
	if err != nil {
		slog.Error("Failed to create getblocks message", "error", err)
		return
	}

	if err := p.Send(msg); err != nil {
		slog.Debug("Failed to send getblocks", "peer", p.Addr, "error", err)
	}
}

// requestHeaders sends a getheaders message for headers-first sync.
func (n *Node) requestHeaders(p *Peer) {
	bc := n.ledger.GetChain()
	fromHash := bc.LastHash()

	payload := &GetHeadersPayload{
		FromHash: fromHash,
		Limit:    MaxGetBlocksLimit,
	}

	msg, err := NewMessage(CmdGetHeaders, payload)
	if err != nil {
		slog.Error("Failed to create getheaders message", "error", err)
		return
	}

	if err := p.Send(msg); err != nil {
		slog.Debug("Failed to send getheaders", "peer", p.Addr, "error", err)
	}
}

func (n *Node) handleGetBlocks(p *Peer, msg *Message) error {
	var payload GetBlocksPayload
	if err := DecodePayload(msg.Payload, &payload); err != nil {
		p.AddBanScore(BanScoreInvalidPayload, "invalid getblocks payload")
		return fmt.Errorf("decode getblocks: %w", err)
	}

	bc := n.ledger.GetChain()
	blocks := bc.Blocks

	// Find the starting point.
	startIdx := 0
	if payload.FromHash != "" {
		for i, b := range blocks {
			if b.Hash == payload.FromHash {
				startIdx = i + 1 // Start after the known block.
				break
			}
		}
	}

	limit := payload.Limit
	if limit <= 0 || limit > MaxGetBlocksLimit {
		limit = MaxGetBlocksLimit
	}

	// Collect block hashes to send as inv.
	var items []InvItem
	for i := startIdx; i < len(blocks) && len(items) < limit; i++ {
		items = append(items, InvItem{
			Type: InvTypeBlock,
			Hash: blocks[i].Hash,
		})
	}

	if len(items) > 0 {
		resp, err := NewMessage(CmdInv, &InvPayload{Items: items})
		if err != nil {
			return err
		}
		return p.Send(resp)
	}

	return nil
}

// handleGetHeaders responds with block header metadata for headers-first sync.
func (n *Node) handleGetHeaders(p *Peer, msg *Message) error {
	var payload GetHeadersPayload
	if err := DecodePayload(msg.Payload, &payload); err != nil {
		p.AddBanScore(BanScoreInvalidPayload, "invalid getheaders payload")
		return fmt.Errorf("decode getheaders: %w", err)
	}

	bc := n.ledger.GetChain()
	blocks := bc.Blocks

	// Find the starting point.
	startIdx := 0
	if payload.FromHash != "" {
		for i, b := range blocks {
			if b.Hash == payload.FromHash {
				startIdx = i + 1
				break
			}
		}
	}

	limit := payload.Limit
	if limit <= 0 || limit > MaxGetBlocksLimit {
		limit = MaxGetBlocksLimit
	}

	// Collect headers.
	var headers []BlockHeaderInfo
	for i := startIdx; i < len(blocks) && len(headers) < limit; i++ {
		b := blocks[i]
		headers = append(headers, BlockHeaderInfo{
			Hash:       b.Hash,
			Height:     b.Header.Height,
			PrevHash:   b.Header.PrevBlockHash,
			MerkleRoot: b.Header.MerkleRoot,
			Timestamp:  b.Header.Timestamp,
			Bits:       b.Header.Bits,
			Nonce:      b.Header.Nonce,
			TxCount:    len(b.Transactions),
		})
	}

	if len(headers) > 0 {
		resp, err := NewMessage(CmdHeaders, &HeadersPayload{Headers: headers})
		if err != nil {
			return err
		}
		return p.Send(resp)
	}

	return nil
}

// handleHeaders processes received headers (headers-first sync).
// After receiving headers, the node requests block bodies for those we need.
func (n *Node) handleHeaders(p *Peer, msg *Message) error {
	var payload HeadersPayload
	if err := DecodePayload(msg.Payload, &payload); err != nil {
		p.AddBanScore(BanScoreInvalidPayload, "invalid headers payload")
		return fmt.Errorf("decode headers: %w", err)
	}

	// Request block bodies for headers we don't have.
	bc := n.ledger.GetChain()
	var needed []InvItem
	for _, hdr := range payload.Headers {
		if bc.GetBlockByHash(hdr.Hash) == nil {
			needed = append(needed, InvItem{
				Type: InvTypeBlock,
				Hash: hdr.Hash,
			})
		}
	}

	if len(needed) > 0 {
		resp, err := NewMessage(CmdGetData, &InvPayload{Items: needed})
		if err != nil {
			return err
		}
		return p.Send(resp)
	}

	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Addr — Peer Discovery (HIGH-2: strict validation)
// ──────────────────────────────────────────────────────────────────────────────

func (n *Node) handleAddr(p *Peer, msg *Message) error {
	var addrPayload AddrPayload
	if err := DecodePayload(msg.Payload, &addrPayload); err != nil {
		p.AddBanScore(BanScoreInvalidPayload, "invalid addr payload")
		return fmt.Errorf("decode addr: %w", err)
	}

	// HIGH-2: Enforce addr item limit.
	if len(addrPayload.Addresses) > MaxAddrItems {
		p.AddBanScore(BanScoreAddrFlood, fmt.Sprintf("addr too large: %d items", len(addrPayload.Addresses)))
		return fmt.Errorf("addr contains %d addresses (max %d)", len(addrPayload.Addresses), MaxAddrItems)
	}

	accepted := 0
	for _, addr := range addrPayload.Addresses {
		// Don't connect to ourselves.
		if addr.NodeID == n.nodeID {
			continue
		}

		// HIGH-2: Strict peer address validation.
		if !isValidPeerAddress(addr.IP, addr.Port) {
			slog.Debug("Rejected invalid peer address from addr message",
				"peer", p.Addr,
				"addr_ip", addr.IP,
				"addr_port", addr.Port,
			)
			continue
		}

		// HIGH-2: Reject addresses with suspiciously old timestamps (>24h).
		if addr.Timestamp > 0 {
			age := time.Now().Unix() - addr.Timestamp
			if age > 24*3600 || age < -600 { // >24h old or >10m in the future
				continue
			}
		}

		target := fmt.Sprintf("%s:%d", addr.IP, addr.Port)

		// Try to connect if we don't already know this peer.
		go n.connectOutbound(target)
		accepted++
	}

	slog.Debug("Processed addr message",
		"peer", p.Addr,
		"total", len(addrPayload.Addresses),
		"accepted", accepted,
	)

	return nil
}

// ShareAddresses sends our peer list to a specific peer.
// HIGH-2: Only shares validated, non-private addresses. Caps output.
func (n *Node) ShareAddresses(p *Peer) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	var addrs []PeerAddress
	for _, peer := range n.peers {
		if peer == p || peer.State != PeerStateActive {
			continue
		}
		host, port, err := net.SplitHostPort(peer.Addr)
		if err != nil {
			continue
		}
		portNum := parsePort(port)

		// HIGH-2: Only share validated addresses.
		if !isValidPeerAddress(host, portNum) {
			continue
		}

		addrs = append(addrs, PeerAddress{
			IP:        host,
			Port:      portNum,
			Timestamp: time.Now().Unix(),
			NodeID:    peer.NodeID,
		})

		// HIGH-2: Cap at MaxAddrItems.
		if len(addrs) >= MaxAddrItems {
			break
		}
	}

	if len(addrs) == 0 {
		return
	}

	msg, err := NewMessage(CmdAddr, &AddrPayload{Addresses: addrs})
	if err != nil {
		return
	}
	p.Send(msg)
}

// ──────────────────────────────────────────────────────────────────────────────
// SyncChain — for backward compatibility with network.Network interface
// ──────────────────────────────────────────────────────────────────────────────

// SyncChain triggers a chain sync from all connected peers.
// Uses the TCP protocol's getblocks mechanism.
func (n *Node) SyncChain() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()

	synced := false
	for _, p := range n.peers {
		if p.State != PeerStateActive {
			continue
		}
		if p.BestHeight > n.ledger.GetChainHeight() {
			n.requestBlocks(p)
			synced = true
		}
	}
	return synced
}

// AddPeer adds a new peer address for outbound connection (HTTP API compat).
func (n *Node) AddPeer(addr string) {
	// HIGH-2: Basic validation of the address format.
	if !strings.Contains(addr, ":") {
		slog.Debug("Invalid peer address (no port)", "addr", addr)
		return
	}

	// Add to seeds so reconnect loop picks it up.
	n.mu.Lock()
	n.seedAddrs = append(n.seedAddrs, addr)
	n.mu.Unlock()

	go n.connectOutbound(addr)
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

func shortAddr(addr string) string {
	if len(addr) <= 16 {
		return addr
	}
	return addr[:8] + "..." + addr[len(addr)-4:]
}

func shortHash(hash string) string {
	if len(hash) <= 16 {
		return hash
	}
	return hash[:16] + "..."
}

func shortID(id string) string {
	if len(id) <= 16 {
		return id
	}
	return id[:16] + "..."
}

func parsePort(s string) uint16 {
	var port uint16
	fmt.Sscanf(s, "%d", &port)
	return port
}
