package httpx

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCheckIP(t *testing.T) {
	public := []string{
		"8.8.8.8", "1.1.1.1", "172.32.0.1", "100.128.0.1", "192.169.0.1",
		"2606:4700::1111", "2001:4860:4860::8888",
	}
	// Blocked by default, allowed with allow_private.
	private := []string{
		"127.0.0.1", "127.1.2.3", // loopback
		"10.0.0.1", "10.255.255.255", "172.16.0.1", "172.31.255.255", "192.168.1.1", // RFC1918
		"100.64.0.1", "100.127.255.255", // CGNAT
		"192.0.0.8", "198.18.0.1", // IETF / benchmarking
		"::1",                     // IPv6 loopback
		"fc00::1", "fd12:3456::1", // ULA
		"fe80::1",          // IPv6 link-local
		"::ffff:127.0.0.1", // IPv4-mapped loopback
		"::ffff:10.0.0.1",  // IPv4-mapped RFC1918
		"64:ff9b::a00:1",   // NAT64 of 10.0.0.1
		"2002:7f00:1::",    // 6to4 of 127.0.0.1
	}
	// Blocked even with allow_private.
	always := []string{
		"0.0.0.0", "0.1.2.3", // "this network": 0.0.0.0 reaches localhost on Linux
		"169.254.169.254", "169.254.1.1", // link-local / cloud metadata
		"::ffff:169.254.169.254",
		"fd00:ec2::254",                           // AWS IMDS over IPv6
		"::",                                      // unspecified
		"224.0.0.1", "239.255.255.250", "ff02::1", // multicast
		"255.255.255.255", "240.0.0.1", // broadcast / reserved
		"64:ff9b::a9fe:a9fe", // NAT64 of 169.254.169.254
	}

	check := func(t *testing.T, addr string, allowPrivate, wantBlocked bool) {
		t.Helper()
		ip := netip.MustParseAddr(addr)
		err := checkIP(ip, allowPrivate)
		if wantBlocked && !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("checkIP(%s, allowPrivate=%v) = %v, want ErrBlockedAddress", addr, allowPrivate, err)
		}
		if !wantBlocked && err != nil {
			t.Errorf("checkIP(%s, allowPrivate=%v) = %v, want nil", addr, allowPrivate, err)
		}
	}
	for _, a := range public {
		t.Run("public/"+a, func(t *testing.T) {
			check(t, a, false, false)
			check(t, a, true, false)
		})
	}
	for _, a := range private {
		t.Run("private/"+a, func(t *testing.T) {
			check(t, a, false, true)
			check(t, a, true, false)
		})
	}
	for _, a := range always {
		t.Run("always/"+a, func(t *testing.T) {
			check(t, a, false, true)
			check(t, a, true, true)
		})
	}
}

func TestDialGuardRejectsNonTCPAndBadAddresses(t *testing.T) {
	guard := dialGuard(true)
	for _, tc := range []struct{ network, address string }{
		{"udp", "8.8.8.8:53"},
		{"unix", "/tmp/sock"},
		{"tcp", "not-an-address"},
	} {
		if err := guard(tc.network, tc.address, nil); err == nil {
			t.Errorf("dialGuard(%q, %q) = nil, want an error", tc.network, tc.address)
		}
	}
	if err := guard("tcp4", "8.8.8.8:443", nil); err != nil {
		t.Errorf("dialGuard(tcp4, public) = %v, want nil", err)
	}
	if err := guard("tcp6", "[2606:4700::1111]:443", nil); err != nil {
		t.Errorf("dialGuard(tcp6, public) = %v, want nil", err)
	}
}

// countingServer is an httptest server that records how often it was hit.
func countingServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func serverPort(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	return strconv.Itoa(srv.Listener.Addr().(*net.TCPAddr).Port)
}

func get(t *testing.T, c *http.Client, rawURL string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err == nil {
		t.Cleanup(func() { _ = resp.Body.Close() })
	}
	return resp, err
}

// fakeResolver returns scripted answers per host; the n-th lookup of a host
// gets answers[host][n], repeating the last entry once exhausted.
type fakeResolver struct {
	mu      sync.Mutex
	answers map[string][][]netip.Addr
	calls   map[string]int
}

func newFakeResolver(answers map[string][][]netip.Addr) *fakeResolver {
	return &fakeResolver{answers: answers, calls: map[string]int{}}
}

func (f *fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	seq, ok := f.answers[host]
	if !ok || len(seq) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	n := f.calls[host]
	f.calls[host]++
	return seq[min(n, len(seq)-1)], nil
}

func addrs(s ...string) []netip.Addr {
	out := make([]netip.Addr, len(s))
	for i, a := range s {
		out[i] = netip.MustParseAddr(a)
	}
	return out
}

func TestClientBlocksLoopbackLiteral(t *testing.T) {
	srv, hits := countingServer(t)
	c := NewClient(Options{})

	_, err := get(t, c, srv.URL)
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("GET %s: err = %v, want ErrBlockedAddress", srv.URL, err)
	}
	if hits.Load() != 0 {
		t.Errorf("server was hit %d times, want 0", hits.Load())
	}
}

func TestClientBlocksMetadataEvenWithAllowPrivate(t *testing.T) {
	// The guard runs before connect(), so this never touches the network.
	c := NewClient(Options{AllowPrivate: true})
	for _, u := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://[fd00:ec2::254]/latest/meta-data/",
		"http://0.0.0.0:80/",
	} {
		if _, err := get(t, c, u); !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("GET %s: err = %v, want ErrBlockedAddress", u, err)
		}
	}
}

func TestClientBlocksHostnameResolvingToPrivate(t *testing.T) {
	srv, hits := countingServer(t)
	res := newFakeResolver(map[string][][]netip.Addr{
		"api.rebind.test": {addrs("127.0.0.1")},
	})
	c := NewClient(Options{Resolver: res})

	_, err := get(t, c, "http://api.rebind.test:"+serverPort(t, srv)+"/")
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, want ErrBlockedAddress", err)
	}
	if hits.Load() != 0 {
		t.Errorf("server was hit %d times, want 0", hits.Load())
	}
}

func TestClientBlocksLocalhostWithSystemResolver(t *testing.T) {
	srv, hits := countingServer(t)
	c := NewClient(Options{})
	_, err := get(t, c, "http://localhost:"+serverPort(t, srv)+"/")
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, want ErrBlockedAddress", err)
	}
	if hits.Load() != 0 {
		t.Errorf("server was hit %d times, want 0", hits.Load())
	}
}

func TestClientDNSRebindingIsCheckedAtDialTime(t *testing.T) {
	// A validator that resolved the name earlier would have seen a public IP.
	// The client must check the address it actually dials, not a cached or
	// earlier answer.
	srv, hits := countingServer(t)
	res := newFakeResolver(map[string][][]netip.Addr{
		"rebind.test": {addrs("93.184.216.34"), addrs("127.0.0.1")},
	})
	first, err := res.LookupNetIP(t.Context(), "ip", "rebind.test")
	if err != nil || checkIP(first[0], false) != nil {
		t.Fatalf("precondition: first answer should be public, got %v %v", first, err)
	}

	c := NewClient(Options{Resolver: res})
	_, err = get(t, c, "http://rebind.test:"+serverPort(t, srv)+"/")
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, want ErrBlockedAddress", err)
	}
	if hits.Load() != 0 {
		t.Errorf("server was hit %d times, want 0", hits.Load())
	}
}

func TestClientBlocksWhenEveryResolvedAddressIsPrivate(t *testing.T) {
	srv, hits := countingServer(t)
	res := newFakeResolver(map[string][][]netip.Addr{
		"multi.test": {addrs("10.0.0.1", "::1", "127.0.0.1")},
	})
	c := NewClient(Options{Resolver: res})
	_, err := get(t, c, "http://multi.test:"+serverPort(t, srv)+"/")
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, want ErrBlockedAddress", err)
	}
	if hits.Load() != 0 {
		t.Errorf("server was hit %d times, want 0", hits.Load())
	}
}

func TestClientAllowPrivateReachesLoopback(t *testing.T) {
	srv, hits := countingServer(t)
	c := NewClient(Options{AllowPrivate: true})
	resp, err := get(t, c, srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if hits.Load() != 1 {
		t.Errorf("server was hit %d times, want 1", hits.Load())
	}
}

func TestClientUsesResolverAndKeepsHost(t *testing.T) {
	var gotHost string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
	}))
	t.Cleanup(srv.Close)
	res := newFakeResolver(map[string][][]netip.Addr{
		"internal.test": {addrs("127.0.0.1")},
	})
	c := NewClient(Options{AllowPrivate: true, Resolver: res})

	host := "internal.test:" + serverPort(t, srv)
	if _, err := get(t, c, "http://"+host+"/"); err != nil {
		t.Fatalf("GET: %v", err)
	}
	if gotHost != host {
		t.Errorf("Host = %q, want %q", gotHost, host)
	}
}

func TestClientUnknownHostFails(t *testing.T) {
	c := NewClient(Options{AllowPrivate: true, Resolver: newFakeResolver(nil)})
	if _, err := get(t, c, "http://nowhere.test/"); err == nil {
		t.Fatal("expected a DNS error")
	}
}
