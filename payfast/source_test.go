package payfast

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/17xande-dev/payment"
)

const (
	dnsOnlyIP  = "198.51.100.42"
	otherDNSIP = "203.0.113.21"
)

type dnsAnswer struct {
	addrs []netip.Addr
	err   error
}

type fakeSourceResolver struct {
	mu      sync.Mutex
	answers map[string]dnsAnswer
	calls   []string
}

func (r *fakeSourceResolver) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, host)
	if network != "ip" {
		return nil, errors.New("expected lookup of both IPv4 and IPv6")
	}
	a := r.answers[host]
	return a.addrs, a.err
}

func (r *fakeSourceResolver) set(host string, answer dnsAnswer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.answers == nil {
		r.answers = make(map[string]dnsAnswer)
	}
	r.answers[host] = answer
}

func (r *fakeSourceResolver) assertCalls(t *testing.T, want ...string) {
	t.Helper()
	r.mu.Lock()
	got := slices.Clone(r.calls)
	r.mu.Unlock()
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("DNS lookups = %v, want %v", got, want)
	}
}

func sourceAnswer(ips ...string) dnsAnswer {
	a := dnsAnswer{}
	for _, ip := range ips {
		a.addrs = append(a.addrs, netip.MustParseAddr(ip))
	}
	return a
}

func assertSource(t *testing.T, g *Gateway, ip string, accepted bool) {
	t.Helper()
	err := g.checkSourceIP(t.Context(), ip)
	if (accepted && err != nil) || (!accepted && !errors.Is(err, ErrSourceIP)) {
		t.Errorf("source %q: accepted wanted %v, got error %v", ip, accepted, err)
	}
}

func TestSourceDNS_LiveHosts(t *testing.T) {
	r := &fakeSourceResolver{answers: map[string]dnsAnswer{
		"www.payfast.co.za":                 sourceAnswer("2001:db8::4%eth0"),
		"w1w.payfast.co.za":                 sourceAnswer("::ffff:" + dnsOnlyIP),
		"w2w.payfast.co.za":                 sourceAnswer("198.51.100.43"),
		"sandbox.payfast.co.za":             sourceAnswer("203.0.113.22"),
		"w1w.payfast.co.za.example.invalid": sourceAnswer("203.0.113.23"),
		"unrelated.example.invalid":         sourceAnswer("203.0.113.24"),
	}}
	g := testGateway(t, func(c *Config) {
		c.Sandbox = false
		c.Resolver = r
	})
	for _, ip := range []string{dnsOnlyIP, "::ffff:" + dnsOnlyIP, "2001:db8::4", "2001:db8::4%eth1", "198.51.100.43"} {
		assertSource(t, g, ip, true)
	}
	for _, ip := range []string{otherDNSIP, "203.0.113.22", "203.0.113.23", "203.0.113.24"} {
		assertSource(t, g, ip, false)
	}
	r.assertCalls(t, "www.payfast.co.za", "w1w.payfast.co.za", "w2w.payfast.co.za")
}

func TestSourceDNS_SandboxHosts(t *testing.T) {
	r := &fakeSourceResolver{answers: map[string]dnsAnswer{
		"sandbox.payfast.co.za": sourceAnswer(otherDNSIP),
		"w1w.payfast.co.za":     sourceAnswer(dnsOnlyIP),
	}}
	g := testGateway(t, func(c *Config) {
		c.Sandbox = true
		c.Resolver = r
	})
	assertSource(t, g, dnsOnlyIP, false)
	assertSource(t, g, otherDNSIP, true)
	assertSource(t, g, validIP, true)
	r.assertCalls(t, "sandbox.payfast.co.za")
}

func TestSourceDNS_OverridesAndBypasses(t *testing.T) {
	r := &fakeSourceResolver{answers: map[string]dnsAnswer{
		"notify.example.invalid": sourceAnswer(dnsOnlyIP),
	}}
	hosts := []string{"notify.example.invalid"}
	g := testGateway(t, func(c *Config) {
		c.SourceHosts = hosts
		c.Resolver = r
		c.AllowedCIDRs = []string{otherDNSIP + "/32"}
	})
	hosts[0] = "replaced.example.invalid"
	assertSource(t, g, otherDNSIP, true)
	r.assertCalls(t)
	assertSource(t, g, dnsOnlyIP, true)
	assertSource(t, g, validIP, false)
	r.assertCalls(t, "notify.example.invalid")

	g = testGateway(t, func(c *Config) {
		c.SourceHosts = []string{}
		c.Resolver = r
	})
	assertSource(t, g, dnsOnlyIP, false)
	assertSource(t, g, validIP, true)

	g = testGateway(t, func(c *Config) {
		c.AllowAnySourceIP = true
		c.Resolver = r
	})
	assertSource(t, g, dnsOnlyIP, true)
	assertSource(t, g, "not-an-ip", true)
	r.assertCalls(t, "notify.example.invalid")
}

func TestSourceDNS_FailureDiscardsPartialAnswers(t *testing.T) {
	errDNS := errors.New("DNS unavailable")
	r := &fakeSourceResolver{answers: map[string]dnsAnswer{
		"partial.example.invalid": {addrs: sourceAnswer(dnsOnlyIP).addrs, err: errDNS},
		"working.example.invalid": sourceAnswer(otherDNSIP),
	}}
	g := testGateway(t, func(c *Config) {
		c.SourceHosts = []string{"partial.example.invalid", "working.example.invalid"}
		c.Resolver = r
	})
	assertSource(t, g, validIP, true)
	assertSource(t, g, "13.245.74.88", true)
	r.assertCalls(t) // Even an outage cannot affect a static hit.
	assertSource(t, g, dnsOnlyIP, false)
	assertSource(t, g, otherDNSIP, false)
	assertSource(t, g, "198.51.100.1", false)
	assertSource(t, g, "13.245.74.88", true)
}

func TestSourceDNS_StaleAfterFailedRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const host = "sandbox.payfast.co.za"
		r := &fakeSourceResolver{answers: map[string]dnsAnswer{host: sourceAnswer(dnsOnlyIP)}}
		g := testGateway(t, func(c *Config) { c.Resolver = r })
		assertSource(t, g, dnsOnlyIP, true)

		r.set(host, dnsAnswer{addrs: sourceAnswer(otherDNSIP).addrs, err: errors.New("DNS unavailable")})
		time.Sleep(5 * time.Minute)
		assertSource(t, g, dnsOnlyIP, true)
		assertSource(t, g, otherDNSIP, false)
		r.assertCalls(t, host, host) // No per-request retry during an outage.

		time.Sleep(54 * time.Minute)
		assertSource(t, g, dnsOnlyIP, true)
		r.assertCalls(t, host, host, host)
		time.Sleep(time.Minute) // 1 hour from success, not the latest failure.
		assertSource(t, g, dnsOnlyIP, false)

		r.set(host, sourceAnswer(otherDNSIP))
		time.Sleep(5 * time.Minute)
		assertSource(t, g, otherDNSIP, true)
		assertSource(t, g, dnsOnlyIP, false)
	})
}

func TestSourceDNS_TTLAndGatewayIsolation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const host = "sandbox.payfast.co.za"
		r := &fakeSourceResolver{answers: map[string]dnsAnswer{host: sourceAnswer(dnsOnlyIP)}}
		g := testGateway(t, func(c *Config) { c.Resolver = r })
		assertSource(t, g, dnsOnlyIP, true)
		r.set(host, sourceAnswer(otherDNSIP))

		time.Sleep(5*time.Minute - time.Second)
		assertSource(t, g, dnsOnlyIP, true)
		assertSource(t, g, otherDNSIP, false)
		r.assertCalls(t, host)

		second := testGateway(t, func(c *Config) { c.Resolver = r })
		assertSource(t, second, otherDNSIP, true)
		assertSource(t, g, otherDNSIP, false)
		time.Sleep(time.Second)
		assertSource(t, g, dnsOnlyIP, false)
		assertSource(t, g, otherDNSIP, true)
		r.assertCalls(t, host, host, host)
	})
}

type resolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (f resolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return f(ctx, network, host)
}

func TestSourceDNS_SingleFlightAndCanceledWaiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		release := make(chan struct{})
		g := testGateway(t, func(c *Config) {
			c.Resolver = resolverFunc(func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
				calls.Add(1)
				select {
				case <-release:
					return sourceAnswer(dnsOnlyIP).addrs, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})
		})

		ctx, cancel := context.WithCancel(t.Context())
		first := make(chan error, 1)
		go func() { first <- g.checkSourceIP(ctx, dnsOnlyIP) }()
		synctest.Wait() // The first request has started the flight.
		const waiters = 24
		results := make(chan error, waiters)
		for range waiters {
			go func() { results <- g.checkSourceIP(t.Context(), dnsOnlyIP) }()
		}
		synctest.Wait() // All waiters have reached the in-flight refresh.
		cancel()
		if err := <-first; !errors.Is(err, ErrSourceIP) {
			t.Errorf("canceled waiter: error = %v, want ErrSourceIP", err)
		}
		close(release)
		for range waiters {
			if err := <-results; err != nil {
				t.Errorf("a remaining waiter was rejected: %v", err)
			}
		}
		if got := calls.Load(); got != 1 {
			t.Errorf("simultaneous cache misses called resolver %d times, want 1", got)
		}
	})
}

func TestSourceDNS_TimeoutBoundsEntireRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		g := testGateway(t, func(c *Config) {
			c.Sandbox = false
			c.Resolver = resolverFunc(func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
				calls.Add(1)
				<-ctx.Done()
				// A late answer must not make its way into the cache.
				time.Sleep(time.Second)
				return sourceAnswer(dnsOnlyIP).addrs, nil
			})
		})
		start := time.Now()
		assertSource(t, g, dnsOnlyIP, false)
		if elapsed := time.Since(start); elapsed != 2*time.Second {
			t.Errorf("DNS refresh took %v, want 2s", elapsed)
		}
		if got := calls.Load(); got != 3 {
			t.Errorf("lookups started = %d, want 3", got)
		}
		time.Sleep(2 * time.Second)
		assertSource(t, g, dnsOnlyIP, false)
	})
}

func TestSourceDNS_UnusableAnswers(t *testing.T) {
	unusable := []string{
		"127.0.0.1", "10.1.2.3", "169.254.2.3", "::1", "0.0.0.0",
		"172.16.1.2", "192.168.1.2", "::ffff:10.1.2.3", "fc00::1", "fe80::1%eth0",
		"224.1.2.3", "ff02::1", "::", "255.255.255.255",
	}
	r := &fakeSourceResolver{answers: map[string]dnsAnswer{
		"sandbox.payfast.co.za": sourceAnswer(append(slices.Clone(unusable), dnsOnlyIP)...),
	}}
	g := testGateway(t, func(c *Config) { c.Resolver = r })
	assertSource(t, g, dnsOnlyIP, true)
	for _, ip := range unusable {
		assertSource(t, g, ip, false)
	}
	r.assertCalls(t, "sandbox.payfast.co.za")
}

type sourceTestTransport func(*http.Request) (*http.Response, error)

func (f sourceTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestITN_DNSOnlySourceSettles(t *testing.T) {
	const reference = "order-42"
	r := &fakeSourceResolver{answers: map[string]dnsAnswer{
		"w1w.payfast.co.za": sourceAnswer(dnsOnlyIP),
	}}
	var confirmed []byte
	var logs bytes.Buffer
	g := testGateway(t, func(c *Config) {
		c.Sandbox = false
		c.Resolver = r
		c.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		c.HTTPClient = &http.Client{Transport: sourceTestTransport(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost || req.URL.String() != "https://www.payfast.co.za/eng/query/validate" {
				t.Errorf("unexpected validation request: %s %s", req.Method, req.URL)
			}
			defer req.Body.Close()
			var err error
			confirmed, err = io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("VALID"))}, nil
		})}
	})
	body := encodeITN(itnFieldsFor(reference, "299.00"), g.cfg.Passphrase)
	if _, err := g.ParseCallback(t.Context(), notify(body, otherDNSIP)); !errors.Is(err, ErrSourceIP) {
		t.Errorf("unknown source: error = %v, want ErrSourceIP", err)
	}
	if confirmed != nil {
		t.Error("unknown source reached server-to-server validation")
	}
	if log := logs.String(); !strings.Contains(log, "source_ip="+otherDNSIP) ||
		strings.Contains(log, g.cfg.Passphrase) || strings.Contains(log, g.cfg.MerchantKey) {
		t.Error("rejection log must include the IP address but not credentials")
	}
	cb, err := g.ParseCallback(t.Context(), notify(body, dnsOnlyIP))
	if err != nil || cb.Outcome != payment.OutcomePaid || cb.Reference != reference || cb.AmountCents != 29900 {
		t.Fatalf("DNS-only source did not settle: outcome=%v reference=%q cents=%d error=%v", cb.Outcome, cb.Reference, cb.AmountCents, err)
	}
	if !bytes.Equal(confirmed, body) {
		t.Error("validation did not receive the original notification body")
	}
}
