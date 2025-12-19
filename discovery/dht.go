// Package discovery provides DHT-backed peer discovery helpers for the chat app.
package discovery

import (
	"context"
	"fmt"
	"sync"
	"time"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
)

func colorize(color int, text string) string {
	return fmt.Sprintf("\x1b[%dm%s\x1b[0m", color, text)
}

// DHTService wraps DHT-based peer discovery utilities.
type DHTService struct {
	host      host.Host                  // local libp2p host
	dht       *dht.IpfsDHT               // Kademlia DHT instance
	discovery *drouting.RoutingDiscovery // high-level discovery helper
	ctx       context.Context            // base lifecycle context
	cancel    context.CancelFunc         // cancels ctx
}

// NewDHTService creates a DHT-backed discovery helper tied to the given host.
func NewDHTService(ctx context.Context, h host.Host) (*DHTService, error) {
	ctx, cancel := context.WithCancel(ctx)

	kdht, err := dht.New(ctx, h, dht.Mode(dht.ModeAutoServer))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create DHT: %w", err)
	}

	routingDiscovery := drouting.NewRoutingDiscovery(kdht)

	return &DHTService{
		host:      h,
		dht:       kdht,
		discovery: routingDiscovery,
		ctx:       ctx,
		cancel:    cancel,
	}, nil
}

// Bootstrap dials the configured bootstrap peers and seeds the routing table.
func (d *DHTService) Bootstrap() error {
	fmt.Println(colorize(95, "Bootstrapping DHT..."))

	if err := d.dht.Bootstrap(d.ctx); err != nil {
		return fmt.Errorf("failed to bootstrap DHT: %w", err)
	}

	var wg sync.WaitGroup
	successCount := 0
	var mu sync.Mutex

	for _, addrStr := range DefaultBootstrapPeers {
		wg.Add(1)

		go func(addr string) {
			defer wg.Done()

			maddr, err := peer.AddrInfoFromString(addr)
			if err != nil {
				fmt.Printf("Invalid bootstrap address: %s\n", colorize(93, err.Error()))
				return
			}

			ctx, cancel := context.WithTimeout(d.ctx, 10*time.Second)
			defer cancel()

			id := maddr.ID.String()
			short := id
			if len(id) > 8 {
				short = id[len(id)-8:]
			}

			if err := d.host.Connect(ctx, *maddr); err != nil {
				fmt.Printf("Failed to connect to bootstrap peer: %s\n", colorize(91, short))
				return
			}

			mu.Lock()
			successCount++
			mu.Unlock()

			fmt.Printf("Successfully connected to bootstrap peer: %s\n", colorize(92, short))
		}(addrStr)
	}

	wg.Wait()

	if successCount == 0 {
		return fmt.Errorf("failed to connect to any bootstrap peers")
	}

	fmt.Printf("Successfully connected to %s out of %d bootstrap peers.\n", colorize(92, fmt.Sprintf("%d", successCount)), len(DefaultBootstrapPeers))

	time.Sleep(2 * time.Second)

	return nil
}

// Advertise announces the host under the given namespace.
func (d *DHTService) Advertise(namespace string) error {
	dutil.Advertise(d.ctx, d.discovery, namespace) // uses TTL-managed records

	fmt.Printf("Advertised on namespace: '%s'\n", colorize(95, namespace))
	return nil
}

// AdvertiseContinuously re-advertises until the context is canceled.
func (d *DHTService) AdvertiseContinuously(namespace string) {
	d.Advertise(namespace)

	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			d.Advertise(namespace)
		case <-d.ctx.Done():
			return
		}
	}
}

// DiscoverPeers streams peers advertising under the namespace, excluding the local host.
// The search stops when either the provided context or the service context is canceled.
func (d *DHTService) DiscoverPeers(ctx context.Context, namespace string) (<-chan DiscoveredPeer, error) {
	fmt.Printf("Searching for peers on namespace: '%s'\n", colorize(95, namespace))

	searchCtx, cancel := context.WithCancel(d.ctx)

	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				cancel()
			case <-searchCtx.Done():
			}
		}()
	}

	peerChan, err := d.discovery.FindPeers(searchCtx, namespace)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to find peers: %w", err)
	}

	outChan := make(chan DiscoveredPeer)

	go func() {
		defer close(outChan)
		defer cancel()

		for {
			select {
			case <-searchCtx.Done():
				return
			case peerInfo, ok := <-peerChan:
				if !ok {
					return
				}
				if peerInfo.ID == d.host.ID() {
					continue
				}
				if len(peerInfo.Addrs) == 0 {
					continue
				}

				outChan <- DiscoveredPeer{
					PeerID: peerInfo.ID,
					Addrs:  peerInfo.Addrs,
				}
			}
		}
	}()

	return outChan, nil
}

// FindPeer looks up a peer's addresses in the DHT.
func (d *DHTService) FindPeer(peerID peer.ID) (peer.AddrInfo, error) {
	ctx, cancel := context.WithTimeout(d.ctx, 30*time.Second)
	defer cancel()

	peerInfo, err := d.dht.FindPeer(ctx, peerID)
	if err != nil {
		return peer.AddrInfo{}, fmt.Errorf("failed to find peer: %w", err)
	}

	return peerInfo, nil
}

// Close shuts down the DHT service.
func (d *DHTService) Close() error {
	d.cancel()
	return d.dht.Close()
}
