package payfast

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	sourceTTL        = 5 * time.Minute
	sourceMaxAge     = time.Hour
	sourceDNSTimeout = 2 * time.Second
)

// Resolver resolves notification hostnames. Implementations must support
// concurrent calls and honor context cancellation.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type systemResolver struct{}

func (systemResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	// Use an absolute DNS name so local search domains cannot widen the allowlist.
	return net.DefaultResolver.LookupNetIP(ctx, network, strings.TrimSuffix(host, ".")+".")
}

type sourceDNS struct {
	hosts    []string
	resolver Resolver

	mu           sync.Mutex
	inFlight     chan struct{}
	addrs        map[netip.Addr]struct{}
	refreshAfter time.Time
	usableUntil  time.Time
}

func newSourceDNS(cfg Config) *sourceDNS {
	hosts := slices.Clone(cfg.SourceHosts)
	if hosts == nil {
		hosts = []string{"www.payfast.co.za", "w1w.payfast.co.za", "w2w.payfast.co.za"}
		if cfg.Sandbox {
			hosts = []string{"sandbox.payfast.co.za"}
		}
	}
	if len(hosts) == 0 || cfg.AllowAnySourceIP {
		return nil
	}
	resolver := cfg.Resolver
	if resolver == nil {
		resolver = systemResolver{}
	}
	return &sourceDNS{hosts: hosts, resolver: resolver}
}

func (s *sourceDNS) contains(ctx context.Context, addr netip.Addr) bool {
	s.mu.Lock()
	if now := time.Now(); now.Before(s.refreshAfter) {
		ok := s.containsCached(now, addr)
		s.mu.Unlock()
		return ok
	}
	if ctx.Err() != nil {
		s.mu.Unlock()
		return false
	}
	done := s.inFlight
	if done == nil {
		done = make(chan struct{})
		s.inFlight = done
		// The flight is shared: canceling one ITN only stops that ITN from
		// waiting, not the refresh or any of the other waiters.
		dnsCtx, cancel := context.WithTimeout(context.Background(), sourceDNSTimeout)
		go s.refresh(dnsCtx, cancel, done)
	}
	s.mu.Unlock()

	select {
	case <-done:
	case <-ctx.Done():
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.containsCached(time.Now(), addr)
}

// containsCached requires mu. A failed refresh never extends usableUntil.
func (s *sourceDNS) containsCached(now time.Time, addr netip.Addr) bool {
	_, ok := s.addrs[addr]
	return ok && now.Before(s.usableUntil)
}

func (s *sourceDNS) refresh(ctx context.Context, cancel context.CancelFunc, done chan struct{}) {
	defer cancel()
	addrs := s.lookup(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if addrs != nil && ctx.Err() == nil {
		s.addrs = addrs
		s.usableUntil = now.Add(sourceMaxAge)
	} else if !now.Before(s.usableUntil) {
		s.addrs = nil
	}
	// Back off after errors too, so an outage doesn't trigger DNS on every ITN.
	s.refreshAfter = now.Add(sourceTTL)
	s.inFlight = nil
	close(done)
}

func (s *sourceDNS) lookup(ctx context.Context) map[netip.Addr]struct{} {
	type result struct {
		addrs []netip.Addr
		err   error
	}
	// Query in parallel: the two-second deadline bounds the whole refresh,
	// not just each hostname. The buffer lets lookups return after a failure.
	results := make(chan result, len(s.hosts))
	for _, host := range s.hosts {
		go func() {
			addrs, err := s.resolver.LookupNetIP(ctx, "ip", host)
			results <- result{addrs, err}
		}()
	}

	addrs := make(map[netip.Addr]struct{})
	for range s.hosts {
		select {
		case <-ctx.Done():
			return nil
		case res := <-results:
			if res.err != nil {
				return nil
			}
			for _, addr := range res.addrs {
				addr = addr.Unmap().WithZone("")
				if addr.IsGlobalUnicast() && !addr.IsPrivate() {
					addrs[addr] = struct{}{}
				}
			}
		}
	}
	return addrs
}
