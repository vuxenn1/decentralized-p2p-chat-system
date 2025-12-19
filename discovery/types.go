package discovery

import (
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// DiscoveredPeer describes a peer found through discovery.
type DiscoveredPeer struct {
	PeerID peer.ID               // Remote peer identifier
	Addrs  []multiaddr.Multiaddr // Advertised connection addresses
}

// DefaultNamespace scopes discovery to this chat application.
const DefaultNamespace = "p2pchat/etu"
