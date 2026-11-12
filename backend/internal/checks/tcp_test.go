package checks

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/proreta-io/proreta/backend/internal/safehttp"
)

func listener(t *testing.T) (host string, port int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	a := l.Addr().(*net.TCPAddr)
	return a.IP.String(), a.Port
}

func plainDial(ctx context.Context, network, addr string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

func TestTCPProberUpWhenTheHandshakeCompletes(t *testing.T) {
	host, port := listener(t)
	res := NewTCPProber(plainDial).Probe(context.Background(), Request{TCP: true, Host: host, Port: port})
	if !res.OK || res.ErrorKind != "" || res.StatusCode != 0 || res.TLSNotAfter != nil || res.CheckedAt.IsZero() {
		t.Fatalf("result = %+v", res)
	}
}

func TestTCPProberFailureKinds(t *testing.T) {
	// A port nothing listens on: closed, then probed.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	blocked := func(context.Context, string, string) (net.Conn, error) { return nil, safehttp.ErrBlockedAddress }
	hang := func(ctx context.Context, _, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }
	for name, tc := range map[string]struct {
		dial DialFunc
		port int
		want string
	}{
		"refused": {plainDial, closed, KindConnect},
		"blocked": {blocked, 5432, KindBlockedAddress},
		"timeout": {hang, 5432, KindTimeout},
	} {
		res := NewTCPProber(tc.dial).Probe(context.Background(), Request{TCP: true, Host: "127.0.0.1", Port: tc.port, Timeout: 100 * time.Millisecond})
		if res.OK || res.ErrorKind != tc.want || res.Cancelled {
			t.Errorf("%s: result = %+v, want kind %s", name, res, tc.want)
		}
	}
}

func TestTCPProberDialsHostAndPort(t *testing.T) {
	var got string
	dial := func(_ context.Context, network, addr string) (net.Conn, error) {
		got = network + " " + addr
		c, _ := net.Pipe()
		return c, nil
	}
	NewTCPProber(dial).Probe(context.Background(), Request{TCP: true, Host: "::1", Port: 25})
	if got != "tcp [::1]:25" {
		t.Fatalf("dialed %q", got)
	}
}

func TestTCPProberShutdownIsNotAResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := NewTCPProber(func(ctx context.Context, _, _ string) (net.Conn, error) { return nil, ctx.Err() }).Probe(ctx, Request{TCP: true, Host: "h", Port: 1})
	if !res.Cancelled || res.OK || res.ErrorKind != "" {
		t.Fatalf("result = %+v", res)
	}
}

func TestMuxRoutesByKind(t *testing.T) {
	var calls []string
	stub := func(name string) Prober {
		return proberFunc(func(Request) Result { calls = append(calls, name); return Result{} })
	}
	m := Mux{HTTP: stub("http"), TCP: stub("tcp")}
	m.Probe(context.Background(), Request{URL: "https://example.com"})
	m.Probe(context.Background(), Request{TCP: true, Host: "h", Port: 80})
	if len(calls) != 2 || calls[0] != "http" || calls[1] != "tcp" {
		t.Fatalf("calls = %v", calls)
	}
}

type proberFunc func(Request) Result

func (f proberFunc) Probe(_ context.Context, r Request) Result { return f(r) }
