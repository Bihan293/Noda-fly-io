package p2p

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// ──────────────────────────────────────────────────────────────────────────────
// Constants
// ──────────────────────────────────────────────────────────────────────────────

func TestMagicBytes(t *testing.T) {
	// "NODA" = 0x4E4F4441
	if MagicBytes != 0x4E4F4441 {
		t.Errorf("MagicBytes = 0x%X, want 0x4E4F4441", MagicBytes)
	}
}

func TestHeaderSize(t *testing.T) {
	// HIGH-2: 4 (magic) + 12 (command) + 4 (payload length) + 4 (checksum) = 24.
	if HeaderSize != 24 {
		t.Errorf("HeaderSize = %d, want 24", HeaderSize)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Checksum (HIGH-2)
// ──────────────────────────────────────────────────────────────────────────────

func TestPayloadChecksum(t *testing.T) {
	data := []byte(`{"nonce":42}`)
	cs := payloadChecksum(data)
	// The checksum should be deterministic.
	cs2 := payloadChecksum(data)
	if cs != cs2 {
		t.Error("payloadChecksum is not deterministic")
	}

	// Different data → different checksum.
	cs3 := payloadChecksum([]byte(`{"nonce":99}`))
	if cs == cs3 {
		t.Error("payloadChecksum should differ for different data")
	}

	// Manually verify: first 4 bytes of double-SHA-256.
	first := sha256.Sum256(data)
	second := sha256.Sum256(first[:])
	var expected [ChecksumSize]byte
	copy(expected[:], second[:ChecksumSize])
	if cs != expected {
		t.Errorf("payloadChecksum mismatch: got %x, want %x", cs, expected)
	}
}

func TestPayloadChecksum_EmptyPayload(t *testing.T) {
	cs := payloadChecksum(nil)
	cs2 := payloadChecksum([]byte{})
	if cs != cs2 {
		t.Error("nil and empty should produce the same checksum")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Command encoding
// ──────────────────────────────────────────────────────────────────────────────

func TestCommandToBytes_RoundTrip(t *testing.T) {
	cmds := []string{CmdVersion, CmdVerack, CmdPing, CmdPong, CmdInv, CmdGetData, CmdTx, CmdBlock, CmdGetBlocks, CmdAddr}

	for _, cmd := range cmds {
		b := commandToBytes(cmd)
		got := bytesToCommand(b)
		if got != cmd {
			t.Errorf("commandToBytes/bytesToCommand round-trip: got %q, want %q", got, cmd)
		}
	}
}

func TestCommandToBytes_Padding(t *testing.T) {
	b := commandToBytes("ping")
	// Should be 12 bytes, "ping" + 8 zero bytes.
	if len(b) != CommandSize {
		t.Errorf("commandToBytes length = %d, want %d", len(b), CommandSize)
	}
	// Check padding bytes are zero.
	for i := 4; i < CommandSize; i++ {
		if b[i] != 0 {
			t.Errorf("commandToBytes padding[%d] = %d, want 0", i, b[i])
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Message encoding / decoding via pipe
// ──────────────────────────────────────────────────────────────────────────────

func TestWriteReadMessage(t *testing.T) {
	// Create a net.Pipe for testing.
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	msg := &Message{
		Command: CmdPing,
		Payload: []byte(`{"nonce":42}`),
	}

	// Write in a goroutine.
	errCh := make(chan error, 1)
	go func() {
		errCh <- WriteMessage(client, msg)
	}()

	// Read on server side.
	got, err := ReadMessage(server)
	if err != nil {
		t.Fatalf("ReadMessage() error: %v", err)
	}

	if wErr := <-errCh; wErr != nil {
		t.Fatalf("WriteMessage() error: %v", wErr)
	}

	if got.Command != CmdPing {
		t.Errorf("ReadMessage().Command = %q, want %q", got.Command, CmdPing)
	}
	if string(got.Payload) != `{"nonce":42}` {
		t.Errorf("ReadMessage().Payload = %q, want %q", string(got.Payload), `{"nonce":42}`)
	}
}

func TestWriteReadMessage_EmptyPayload(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	msg := &Message{Command: CmdVerack}

	go WriteMessage(client, msg)

	got, err := ReadMessage(server)
	if err != nil {
		t.Fatalf("ReadMessage() error: %v", err)
	}
	if got.Command != CmdVerack {
		t.Errorf("Command = %q, want %q", got.Command, CmdVerack)
	}
	if len(got.Payload) != 0 {
		t.Errorf("Payload length = %d, want 0", len(got.Payload))
	}
}

func TestReadMessage_BadMagic(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	// Write a header with wrong magic.
	go func() {
		header := make([]byte, HeaderSize)
		binary.LittleEndian.PutUint32(header[0:4], 0xDEADBEEF) // Wrong magic.
		cmd := commandToBytes(CmdPing)
		copy(header[4:16], cmd[:])
		binary.LittleEndian.PutUint32(header[16:20], 0) // No payload.
		// Checksum for empty payload.
		cs := payloadChecksum(nil)
		copy(header[20:24], cs[:])
		client.Write(header)
	}()

	_, err := ReadMessage(server)
	if err == nil {
		t.Error("ReadMessage() should fail with bad magic bytes")
	}
}

func TestWriteMessage_PayloadTooLarge(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	msg := &Message{
		Command: CmdBlock,
		Payload: make([]byte, MaxPayloadSize+1),
	}

	err := WriteMessage(client, msg)
	if err == nil {
		t.Error("WriteMessage() should fail for payload too large")
	}
}

// HIGH-2: Test bad checksum detection.
func TestReadMessage_BadChecksum(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	payloadData := []byte(`{"nonce":42}`)

	go func() {
		header := make([]byte, HeaderSize)
		binary.LittleEndian.PutUint32(header[0:4], MagicBytes)
		cmd := commandToBytes(CmdPing)
		copy(header[4:16], cmd[:])
		binary.LittleEndian.PutUint32(header[16:20], uint32(len(payloadData)))
		// Write WRONG checksum (all 0xFF).
		header[20] = 0xFF
		header[21] = 0xFF
		header[22] = 0xFF
		header[23] = 0xFF
		client.Write(header)
		client.Write(payloadData)
	}()

	_, err := ReadMessage(server)
	if err == nil {
		t.Error("ReadMessage() should fail with bad checksum")
	}
	if err != ErrBadChecksum {
		t.Errorf("ReadMessage() error = %v, want ErrBadChecksum", err)
	}
}

// HIGH-2: Test unknown command rejection.
func TestReadMessage_UnknownCommand(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go func() {
		header := make([]byte, HeaderSize)
		binary.LittleEndian.PutUint32(header[0:4], MagicBytes)
		cmd := commandToBytes("badcmd")
		copy(header[4:16], cmd[:])
		binary.LittleEndian.PutUint32(header[16:20], 0) // No payload.
		cs := payloadChecksum(nil)
		copy(header[20:24], cs[:])
		client.Write(header)
	}()

	_, err := ReadMessage(server)
	if err == nil {
		t.Error("ReadMessage() should fail with unknown command")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Payload helpers
// ──────────────────────────────────────────────────────────────────────────────

func TestEncodeDecodePayload(t *testing.T) {
	original := &PingPayload{Nonce: 12345}

	data, err := EncodePayload(original)
	if err != nil {
		t.Fatalf("EncodePayload() error: %v", err)
	}

	var decoded PingPayload
	err = DecodePayload(data, &decoded)
	if err != nil {
		t.Fatalf("DecodePayload() error: %v", err)
	}
	if decoded.Nonce != 12345 {
		t.Errorf("decoded Nonce = %d, want 12345", decoded.Nonce)
	}
}

func TestNewMessage(t *testing.T) {
	payload := &VersionPayload{
		Version:    1,
		BestHeight: 100,
		ListenPort: 9333,
		UserAgent:  "/test/",
		Timestamp:  1000,
		NodeID:     "test-node",
	}

	msg, err := NewMessage(CmdVersion, payload)
	if err != nil {
		t.Fatalf("NewMessage() error: %v", err)
	}
	if msg.Command != CmdVersion {
		t.Errorf("Command = %q, want %q", msg.Command, CmdVersion)
	}
	if len(msg.Payload) == 0 {
		t.Error("NewMessage() Payload is empty")
	}
}

func TestNewMessage_NilPayload(t *testing.T) {
	msg, err := NewMessage(CmdVerack, nil)
	if err != nil {
		t.Fatalf("NewMessage(nil) error: %v", err)
	}
	if msg.Command != CmdVerack {
		t.Errorf("Command = %q, want %q", msg.Command, CmdVerack)
	}
	if msg.Payload != nil {
		t.Error("Payload should be nil")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Payload types
// ──────────────────────────────────────────────────────────────────────────────

func TestVersionPayload_RoundTrip(t *testing.T) {
	vp := &VersionPayload{
		Version:    ProtocolVersion,
		BestHeight: 500,
		ListenPort: 9333,
		UserAgent:  UserAgent,
		Timestamp:  time.Now().Unix(),
		NodeID:     "abc123",
	}

	data, _ := EncodePayload(vp)
	var decoded VersionPayload
	DecodePayload(data, &decoded)

	if decoded.Version != vp.Version {
		t.Errorf("Version = %d, want %d", decoded.Version, vp.Version)
	}
	if decoded.BestHeight != vp.BestHeight {
		t.Errorf("BestHeight = %d, want %d", decoded.BestHeight, vp.BestHeight)
	}
}

func TestInvPayload_RoundTrip(t *testing.T) {
	inv := &InvPayload{
		Items: []InvItem{
			{Type: InvTypeTx, Hash: "abc123"},
			{Type: InvTypeBlock, Hash: "def456"},
		},
	}

	data, _ := EncodePayload(inv)
	var decoded InvPayload
	DecodePayload(data, &decoded)

	if len(decoded.Items) != 2 {
		t.Fatalf("Items len = %d, want 2", len(decoded.Items))
	}
	if decoded.Items[0].Type != InvTypeTx {
		t.Errorf("Items[0].Type = %d, want %d", decoded.Items[0].Type, InvTypeTx)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Peer
// ──────────────────────────────────────────────────────────────────────────────

func TestNewPeer(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	p := NewPeer(client, false)
	if p == nil {
		t.Fatal("NewPeer() returned nil")
	}
	if p.State != PeerStateConnecting {
		t.Errorf("initial State = %d, want %d", p.State, PeerStateConnecting)
	}
	if p.Inbound {
		t.Error("Inbound should be false")
	}
	if p.KnownBlocks == nil || p.KnownTxs == nil {
		t.Error("KnownBlocks and KnownTxs should be initialized")
	}
}

func TestPeer_MarkKnown(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	p := NewPeer(client, false)

	p.MarkKnownBlock("block1")
	if !p.HasBlock("block1") {
		t.Error("HasBlock(block1) = false after MarkKnownBlock")
	}
	if p.HasBlock("block2") {
		t.Error("HasBlock(block2) should be false")
	}

	p.MarkKnownTx("tx1")
	if !p.HasTx("tx1") {
		t.Error("HasTx(tx1) = false after MarkKnownTx")
	}
}

func TestPeer_AddBanScore(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	p := NewPeer(client, false)

	// Below threshold.
	banned := p.AddBanScore(50, "test")
	if banned {
		t.Error("should not be banned at score 50")
	}

	// At threshold.
	banned = p.AddBanScore(50, "test")
	if !banned {
		t.Error("should be banned at score 100")
	}
}

func TestPeer_Disconnect(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()

	p := NewPeer(client, false)
	p.Disconnect()

	if p.State != PeerStateDisconnected {
		t.Errorf("State = %d, want %d", p.State, PeerStateDisconnected)
	}

	// Double disconnect should not panic.
	p.Disconnect()
}

func TestPeer_SendDisconnected(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()

	p := NewPeer(client, false)
	p.Disconnect()

	msg := &Message{Command: CmdPing}
	err := p.Send(msg)
	if err == nil {
		t.Error("Send() should fail on disconnected peer")
	}
}

// HIGH-2: Test per-peer rate limiting.
func TestPeer_RecordMessage_RateLimit(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	p := NewPeer(client, false)

	// Should be allowed up to PeerMaxMsgsPerWindow messages.
	for i := 0; i < PeerMaxMsgsPerWindow; i++ {
		if !p.RecordMessage() {
			t.Fatalf("RecordMessage() returned false at message %d (should be allowed up to %d)", i, PeerMaxMsgsPerWindow)
		}
	}

	// The next message should be rejected.
	if p.RecordMessage() {
		t.Error("RecordMessage() should return false after exceeding rate limit")
	}
}

// HIGH-2: Test getdata outstanding limit.
func TestPeer_GetDataLimit(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	p := NewPeer(client, false)

	// Increment up to limit.
	ok := p.IncrGetData(MaxGetDataOutstanding)
	if !ok {
		t.Error("IncrGetData should succeed up to limit")
	}

	// Exceeding limit should fail.
	ok = p.IncrGetData(1)
	if ok {
		t.Error("IncrGetData should fail when limit exceeded")
	}

	// Decrement and try again.
	p.DecrGetData(100)
	ok = p.IncrGetData(100)
	if !ok {
		t.Error("IncrGetData should succeed after DecrGetData")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Peer Address Validation (HIGH-2)
// ──────────────────────────────────────────────────────────────────────────────

func TestIsValidPeerAddress(t *testing.T) {
	tests := []struct {
		ip    string
		port  uint16
		valid bool
		desc  string
	}{
		{"203.0.113.50", 9333, true, "valid public IPv4"},
		{"2001:db8::1", 9333, true, "documentation IPv6 (not in private/loopback ranges)"},
		{"127.0.0.1", 9333, false, "loopback IPv4"},
		{"::1", 9333, false, "loopback IPv6"},
		{"0.0.0.0", 9333, false, "unspecified IPv4"},
		{"::", 9333, false, "unspecified IPv6"},
		{"10.0.0.1", 9333, false, "private 10.0.0.0/8"},
		{"172.16.0.1", 9333, false, "private 172.16.0.0/12"},
		{"192.168.1.1", 9333, false, "private 192.168.0.0/16"},
		{"169.254.1.1", 9333, false, "link-local"},
		{"224.0.0.1", 9333, false, "multicast"},
		{"203.0.113.50", 0, false, "port zero"},
		{"not-an-ip", 9333, false, "invalid IP string"},
		{"", 9333, false, "empty IP"},
	}

	for _, tt := range tests {
		got := isValidPeerAddress(tt.ip, tt.port)
		if got != tt.valid {
			t.Errorf("isValidPeerAddress(%q, %d) = %v, want %v (%s)", tt.ip, tt.port, got, tt.valid, tt.desc)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Relay Deduplication (HIGH-2)
// ──────────────────────────────────────────────────────────────────────────────

func TestNode_RelayDeduplication(t *testing.T) {
	n := &Node{
		recentRelay: make(map[string]time.Time),
	}

	// First relay should succeed.
	if !n.markRelayed("hash1") {
		t.Error("first markRelayed() should return true")
	}

	// Second relay of same hash should be rejected.
	if n.markRelayed("hash1") {
		t.Error("duplicate markRelayed() should return false")
	}

	// Different hash should succeed.
	if !n.markRelayed("hash2") {
		t.Error("markRelayed() of different hash should return true")
	}
}

func TestNode_CleanRecentRelay(t *testing.T) {
	n := &Node{
		recentRelay: make(map[string]time.Time),
	}

	// Add an old entry.
	n.recentRelay["old_hash"] = time.Now().Add(-15 * time.Minute)
	// Add a fresh entry.
	n.recentRelay["new_hash"] = time.Now()

	n.cleanRecentRelay()

	if _, exists := n.recentRelay["old_hash"]; exists {
		t.Error("old entry should have been cleaned up")
	}
	if _, exists := n.recentRelay["new_hash"]; !exists {
		t.Error("fresh entry should still exist")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Helper functions
// ──────────────────────────────────────────────────────────────────────────────

func TestGenerateNodeID(t *testing.T) {
	id1 := generateNodeID()
	id2 := generateNodeID()

	if len(id1) != 32 { // 16 bytes = 32 hex chars.
		t.Errorf("node ID length = %d, want 32", len(id1))
	}
	if id1 == id2 {
		t.Error("generateNodeID() should produce unique IDs")
	}
}

func TestShortAddr(t *testing.T) {
	long := "abcdefghijklmnopqrstuvwxyz123456"
	short := shortAddr(long)
	if short == long {
		t.Error("shortAddr() should truncate long addresses")
	}

	tiny := "abc"
	if shortAddr(tiny) != tiny {
		t.Error("shortAddr() should not truncate short addresses")
	}
}

func TestParsePort(t *testing.T) {
	if parsePort("9333") != 9333 {
		t.Errorf("parsePort(9333) = %d", parsePort("9333"))
	}
	if parsePort("0") != 0 {
		t.Errorf("parsePort(0) = %d", parsePort("0"))
	}
}

func TestReadMessage_FromRawBytes(t *testing.T) {
	// Build a valid raw message with checksum.
	payloadData := []byte(`{"nonce":99}`)
	checksum := payloadChecksum(payloadData)

	var buf bytes.Buffer

	// Magic.
	magic := make([]byte, 4)
	binary.LittleEndian.PutUint32(magic, MagicBytes)
	buf.Write(magic)

	// Command.
	cmd := commandToBytes(CmdPong)
	buf.Write(cmd[:])

	// Payload length.
	pLen := make([]byte, 4)
	binary.LittleEndian.PutUint32(pLen, uint32(len(payloadData)))
	buf.Write(pLen)

	// Checksum (HIGH-2).
	buf.Write(checksum[:])

	// Payload.
	buf.Write(payloadData)

	// Create a pipe and write the raw bytes.
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go func() {
		client.Write(buf.Bytes())
	}()

	msg, err := ReadMessage(server)
	if err != nil {
		t.Fatalf("ReadMessage() error: %v", err)
	}
	if msg.Command != CmdPong {
		t.Errorf("Command = %q, want %q", msg.Command, CmdPong)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Ban Score Constants (HIGH-2)
// ──────────────────────────────────────────────────────────────────────────────

func TestBanScoreConstants(t *testing.T) {
	// Bad checksum should cause instant ban.
	if BanScoreBadChecksum < MaxBanScore {
		t.Errorf("BanScoreBadChecksum (%d) should be >= MaxBanScore (%d)", BanScoreBadChecksum, MaxBanScore)
	}

	// Other scores should be below instant ban.
	if BanScoreInvalidPayload >= MaxBanScore {
		t.Errorf("BanScoreInvalidPayload (%d) should be < MaxBanScore (%d)", BanScoreInvalidPayload, MaxBanScore)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Node ID duplicate detection (HIGH-2)
// ──────────────────────────────────────────────────────────────────────────────

func TestNode_RegisterNodeID(t *testing.T) {
	n := &Node{
		connectedNodeIDs: make(map[string]string),
		peers:            make(map[string]*Peer),
	}

	s1, c1 := net.Pipe()
	defer s1.Close()
	defer c1.Close()

	p1 := NewPeer(c1, false)
	p1.NodeID = "node-abc"

	// First registration should succeed.
	if !n.registerNodeID(p1) {
		t.Error("first registerNodeID() should succeed")
	}

	s2, c2 := net.Pipe()
	defer s2.Close()
	defer c2.Close()

	p2 := NewPeer(c2, false)
	p2.NodeID = "node-abc" // same nodeID, different connection

	// Duplicate should be rejected.
	if n.registerNodeID(p2) {
		t.Error("duplicate registerNodeID() should fail")
	}

	s3, c3 := net.Pipe()
	defer s3.Close()
	defer c3.Close()

	p3 := NewPeer(c3, false)
	p3.NodeID = "node-xyz" // different nodeID

	// Different nodeID should succeed.
	if !n.registerNodeID(p3) {
		t.Error("different nodeID registerNodeID() should succeed")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Per-peer limits constants (HIGH-2)
// ──────────────────────────────────────────────────────────────────────────────

func TestPerPeerLimits(t *testing.T) {
	if MaxInvItems <= 0 {
		t.Error("MaxInvItems must be positive")
	}
	if MaxAddrItems <= 0 {
		t.Error("MaxAddrItems must be positive")
	}
	if MaxGetDataOutstanding <= 0 {
		t.Error("MaxGetDataOutstanding must be positive")
	}
	if MaxRelayFanOut <= 0 {
		t.Error("MaxRelayFanOut must be positive")
	}
	if PeerMaxMsgsPerWindow <= 0 {
		t.Error("PeerMaxMsgsPerWindow must be positive")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Valid commands map (HIGH-2)
// ──────────────────────────────────────────────────────────────────────────────

func TestValidCommands(t *testing.T) {
	expected := []string{
		CmdVersion, CmdVerack, CmdPing, CmdPong,
		CmdInv, CmdGetData, CmdTx, CmdBlock,
		CmdGetBlocks, CmdAddr, CmdGetHeaders, CmdHeaders,
	}
	for _, cmd := range expected {
		if !validCommands[cmd] {
			t.Errorf("validCommands[%q] = false, want true", cmd)
		}
	}
	if validCommands["badcmd"] {
		t.Error("validCommands[badcmd] should be false")
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// HIGH-3: Fuzz Tests for P2P message framing
// ══════════════════════════════════════════════════════════════════════════════

// FuzzP2PMessageRoundTrip fuzzes the write+read cycle via net.Pipe.
func FuzzP2PMessageRoundTrip(f *testing.F) {
	f.Add("version", []byte(`{"version":1}`))
	f.Add("ping", []byte(`{}`))
	f.Add("tx", []byte(`{"id":"abc"}`))

	f.Fuzz(func(t *testing.T, command string, payload []byte) {
		// Only test with valid commands to avoid false negatives from validation.
		if !validCommands[command] {
			return
		}
		// Skip oversized payloads.
		if len(payload) > 1024*1024 {
			return
		}

		msg := &Message{
			Command: command,
			Payload: payload,
		}

		// Create a net.Pipe for in-memory testing.
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()

		// Write in a goroutine.
		errCh := make(chan error, 1)
		go func() {
			errCh <- WriteMessage(client, msg)
		}()

		// Read from the other end.
		got, err := ReadMessage(server)
		if writeErr := <-errCh; writeErr != nil {
			// Write failure is acceptable for fuzzing.
			return
		}
		if err != nil {
			t.Fatalf("ReadMessage failed: %v", err)
		}

		if got.Command != command {
			t.Errorf("command = %q, want %q", got.Command, command)
		}
		if !bytes.Equal(got.Payload, payload) {
			t.Errorf("payload mismatch: got %d bytes, want %d bytes", len(got.Payload), len(payload))
		}
	})
}

// FuzzP2PReadMessageMalformed ensures ReadMessage handles garbage gracefully.
func FuzzP2PReadMessageMalformed(f *testing.F) {
	// Seed with various malformed headers.
	f.Add([]byte{0x00, 0x00, 0x00, 0x00})
	f.Add([]byte("NODA"))

	// Valid magic + garbage.
	hdr := make([]byte, 24)
	binary.LittleEndian.PutUint32(hdr[0:4], MagicBytes)
	f.Add(hdr)

	// Valid header with bad checksum.
	cmdBytes := commandToBytes("ping")
	copy(hdr[4:16], cmdBytes[:])
	binary.LittleEndian.PutUint32(hdr[16:20], 0) // zero payload
	checksum := sha256.Sum256(nil)
	copy(hdr[20:24], checksum[:4])
	f.Add(hdr)

	f.Fuzz(func(t *testing.T, data []byte) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()

		// Write raw bytes.
		go func() {
			client.Write(data)
			client.Close()
		}()

		// ReadMessage should not panic.
		_, _ = ReadMessage(server)
	})
}
