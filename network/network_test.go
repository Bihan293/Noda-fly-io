package network

import (
	"testing"
)

func TestNewNetwork(t *testing.T) {
	n := NewNetwork(nil)
	if n == nil {
		t.Fatal("NewNetwork(nil) returned nil")
	}
	if len(n.GetPeers()) != 0 {
		t.Errorf("GetPeers() len = %d, want 0", len(n.GetPeers()))
	}
}

func TestNewNetwork_WithPeers(t *testing.T) {
	peers := []string{"http://localhost:3001", "http://localhost:3002"}
	n := NewNetwork(peers)
	if len(n.GetPeers()) != 2 {
		t.Errorf("GetPeers() len = %d, want 2", len(n.GetPeers()))
	}
}

func TestAddPeer(t *testing.T) {
	n := NewNetwork(nil)
	n.AddPeer("http://localhost:3001")
	n.AddPeer("http://localhost:3002")

	peers := n.GetPeers()
	if len(peers) != 2 {
		t.Errorf("GetPeers() len = %d, want 2", len(peers))
	}
}

func TestAddPeer_Duplicate(t *testing.T) {
	n := NewNetwork(nil)
	n.AddPeer("http://localhost:3001")
	n.AddPeer("http://localhost:3001") // Duplicate.

	peers := n.GetPeers()
	if len(peers) != 1 {
		t.Errorf("GetPeers() len = %d, want 1 (no duplicates)", len(peers))
	}
}

func TestPeerCount(t *testing.T) {
	n := NewNetwork([]string{"http://a", "http://b"})
	if n.PeerCount() != 2 {
		t.Errorf("PeerCount() = %d, want 2", n.PeerCount())
	}
}
