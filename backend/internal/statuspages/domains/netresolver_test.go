package domains

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// dnsServer is a tiny authoritative UDP server: TXT records, CNAME aliases with an address at the
// end of the chain, SERVFAIL names and names that never answer.
type dnsServer struct {
	addr     string
	txt      map[string][]string
	cname    map[string]string // name -> target
	addrs    map[string]bool   // names that have an A record
	servfail map[string]bool
	silent   map[string]bool
	queries  atomic.Int32
}

func newDNSServer(t *testing.T) *dnsServer {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &dnsServer{addr: pc.LocalAddr().String(), txt: map[string][]string{}, cname: map[string]string{}, addrs: map[string]bool{},
		servfail: map[string]bool{}, silent: map[string]bool{}}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if resp := s.answer(buf[:n]); resp != nil {
				_, _ = pc.WriteTo(resp, from)
			}
		}
	}()
	return s
}

func (s *dnsServer) answer(req []byte) []byte {
	var m dnsmessage.Message
	if m.Unpack(req) != nil || len(m.Questions) != 1 {
		return nil
	}
	s.queries.Add(1)
	q := m.Questions[0]
	name := strings.TrimSuffix(strings.ToLower(q.Name.String()), ".")
	if s.silent[name] {
		return nil
	}
	h := dnsmessage.Header{ID: m.ID, Response: true, Authoritative: true, RecursionAvailable: true}
	resp := dnsmessage.Message{Header: h, Questions: m.Questions}
	rr := func(n string, b dnsmessage.ResourceBody) dnsmessage.Resource {
		return dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(n + "."), Class: dnsmessage.ClassINET, TTL: 60}, Body: b}
	}
	if s.servfail[name] {
		resp.RCode = dnsmessage.RCodeServerFailure
		out, _ := resp.Pack()
		return out
	}
	switch q.Type {
	case dnsmessage.TypeTXT:
		vals, ok := s.txt[name]
		if !ok {
			resp.RCode = dnsmessage.RCodeNameError
		}
		for _, v := range vals {
			resp.Answers = append(resp.Answers, rr(name, &dnsmessage.TXTResource{TXT: []string{v}}))
		}
	case dnsmessage.TypeA, dnsmessage.TypeAAAA:
		cur, known := name, false
		for range 10 {
			if next, ok := s.cname[cur]; ok {
				resp.Answers = append(resp.Answers, rr(cur, &dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName(next + ".")}))
				cur, known = next, true
			} else {
				break
			}
		}
		if s.addrs[cur] && q.Type == dnsmessage.TypeA {
			resp.Answers = append(resp.Answers, rr(cur, &dnsmessage.AResource{A: [4]byte{192, 0, 2, 1}}))
			known = true
		}
		if !known && !s.addrs[cur] {
			resp.RCode = dnsmessage.RCodeNameError
		}
	}
	out, _ := resp.Pack()
	return out
}

func newTestResolver(t *testing.T, s *dnsServer, mod func(*NetResolverConfig)) *NetResolver {
	t.Helper()
	cfg := NetResolverConfig{Server: s.addr, Timeout: 500 * time.Millisecond}
	if mod != nil {
		mod(&cfg)
	}
	r, err := NewNetResolver(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestNetResolverTXT(t *testing.T) {
	s := newDNSServer(t)
	long := strings.Repeat("x", 200)
	s.txt["_proreta-status.status.acme.com"] = []string{"v=spf1 -all", TXTValue(testToken), long}
	s.txt["many.acme.com"] = func() []string {
		var v []string
		for i := range 20 {
			v = append(v, strings.Repeat("a", i+1))
		}
		return v
	}()
	s.servfail["broken.acme.com"] = true
	s.silent["slow.acme.com"] = true
	r := newTestResolver(t, s, func(c *NetResolverConfig) { c.MaxTXTLength = 100 })
	ctx := context.Background()

	got, err := r.LookupTXT(ctx, "_proreta-status.status.acme.com")
	if err != nil || len(got) != 2 || got[1] != TXTValue(testToken) {
		t.Fatalf("txt: %v %v (the 200 character string must be dropped)", got, err)
	}
	if _, err := r.LookupTXT(ctx, "none.acme.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nxdomain: %v", err)
	}
	if _, err := r.LookupTXT(ctx, "many.acme.com"); !errors.Is(err, ErrAnswerTooLarge) {
		t.Fatalf("20 answers: %v", err)
	}
	for _, name := range []string{"broken.acme.com", "slow.acme.com"} {
		start := time.Now()
		_, err := r.LookupTXT(ctx, name)
		if err == nil || errors.Is(err, ErrNotFound) {
			t.Fatalf("%s must be an error that is not ErrNotFound: %v", name, err)
		}
		if time.Since(start) > 3*time.Second {
			t.Fatalf("%s took %v, the per query timeout is 500ms", name, time.Since(start))
		}
	}
}

func TestNetResolverCNAMEAndProbe(t *testing.T) {
	s := newDNSServer(t)
	s.cname["status.acme.com"] = "edge.cdn.net"
	s.cname["edge.cdn.net"] = testTarget
	s.addrs[testTarget] = true
	s.addrs["plain.acme.com"] = true
	s.cname["other.acme.com"] = "elsewhere.example.org"
	s.addrs["elsewhere.example.org"] = true
	s.txt[TXTName("status.acme.com")] = []string{TXTValue(testToken)}
	s.servfail["down.acme.com"] = true
	r := newTestResolver(t, s, nil)
	ctx := context.Background()

	if got, err := r.LookupCNAME(ctx, "status.acme.com"); err != nil || got != testTarget {
		t.Fatalf("chain: %q %v", got, err)
	}
	if _, err := r.LookupCNAME(ctx, "plain.acme.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a name without alias must be ErrNotFound: %v", err)
	}
	if _, err := r.LookupCNAME(ctx, "none.acme.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nxdomain: %v", err)
	}
	if _, err := r.LookupCNAME(ctx, "down.acme.com"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("servfail must not be ErrNotFound: %v", err)
	}
	// Through Probe: right, wrong target, and a dead resolver that must read as unknown.
	if got := Probe(ctx, r, "status.acme.com", testToken, testTarget); got != (Facts{Present, Present}) {
		t.Fatalf("probe: %+v", got)
	}
	if got := Probe(ctx, r, "other.acme.com", testToken, testTarget); got != (Facts{Missing, Missing}) {
		t.Fatalf("probe elsewhere: %+v", got)
	}
	dead := newDNSServer(t)
	dead.silent["status.acme.com"], dead.silent[TXTName("status.acme.com")] = true, true
	rd := newTestResolver(t, dead, nil)
	if got := Probe(ctx, rd, "status.acme.com", testToken, testTarget); got != (Facts{Unknown, Unknown}) {
		t.Fatalf("a resolver that never answers must give unknown, got %+v", got)
	}
}

func TestNetResolverInjectedDialAndServerForms(t *testing.T) {
	s := newDNSServer(t)
	s.txt["a.acme.com"] = []string{"hello"}
	var dialed atomic.Int32
	r, err := NewNetResolver(NetResolverConfig{Server: "192.0.2.53", Timeout: time.Second, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		dialed.Add(1)
		if address != "192.0.2.53:53" {
			t.Errorf("address %q: the pinned server with the default port expected", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, s.addr)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.LookupTXT(context.Background(), "a.acme.com"); err != nil || len(got) != 1 || dialed.Load() == 0 {
		t.Fatalf("injected dialer: %v %v dialed=%d", got, err, dialed.Load())
	}
}
