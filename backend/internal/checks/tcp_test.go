package checks

import (
	"bytes"
	"context"
	"fmt"
	"io"
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

// serve accepts connections and runs handle on each.
func serve(t *testing.T, handle func(net.Conn)) (host string, port int) {
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
			go func() { defer c.Close(); handle(c) }()
		}
	}()
	a := l.Addr().(*net.TCPAddr)
	return a.IP.String(), a.Port
}

// frame is a length-prefixed message, the shape of the NestJS TCP transport.
func frame(s string) []byte {
	return append([]byte(fmt.Sprintf("%d#", len(s))), s...)
}

func TestTCPProberSendAndExpect(t *testing.T) {
	request := frame(`{"pattern":"v1/healthcheck","data":{"probe_type":"readiness"}}`)
	healthy := func(c net.Conn) {
		got := make([]byte, len(request))
		if _, err := io.ReadFull(c, got); err != nil || !bytes.Equal(got, request) {
			return // a wrong request gets no answer
		}
		_, _ = c.Write(frame(`{"response":{"status":"ok"},"isDisposed":true}`))
	}
	sick := func(c net.Conn) {
		_, _ = io.ReadFull(c, make([]byte, len(request)))
		_, _ = c.Write(frame(`{"response":{"status":"error"}}`))
	}
	silent := func(c net.Conn) { time.Sleep(2 * time.Second) }
	closing := func(c net.Conn) {}
	flood := func(c net.Conn) { _, _ = c.Write(bytes.Repeat([]byte("x"), 10000)) }
	tests := []struct {
		name   string
		handle func(net.Conn)
		req    Request
		want   string
	}{
		{"match contains", healthy, Request{Send: request, ExpectMode: "contains", ExpectValue: `"status":"ok"`}, ""},
		{"match regex", healthy, Request{Send: request, ExpectMode: "regex", ExpectValue: `"status":\s*"(ok|ready)"`}, ""},
		{"banner without send", func(c net.Conn) { _, _ = c.Write([]byte("SSH-2.0-x\r\n")) }, Request{ExpectMode: "contains", ExpectValue: "SSH-2.0"}, ""},
		{"reply does not match", sick, Request{Send: request, ExpectMode: "contains", ExpectValue: `"status":"ok"`}, KindUnexpectedResponse},
		{"no reply in time", silent, Request{Send: request, ExpectMode: "contains", ExpectValue: "ok", ReadTimeout: 200 * time.Millisecond}, KindNoResponse},
		{"closed without a reply", closing, Request{Send: request, ExpectMode: "contains", ExpectValue: "ok"}, KindNoResponse},
		{"limit reached before a match", flood, Request{ExpectMode: "contains", ExpectValue: "ok", MaxResponse: 100}, KindUnexpectedResponse},
		{"match inside the limit", flood, Request{ExpectMode: "contains", ExpectValue: "xxx", MaxResponse: 100}, ""},
		{"wrong request gets no answer", healthy, Request{Send: frame("nope"), ExpectMode: "contains", ExpectValue: "ok", ReadTimeout: 200 * time.Millisecond}, KindNoResponse},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			host, port := serve(t, tc.handle)
			tc.req.TCP, tc.req.Host, tc.req.Port = true, host, port
			res := NewTCPProber(plainDial).Probe(context.Background(), tc.req)
			if res.ErrorKind != tc.want || res.OK != (tc.want == "") {
				t.Fatalf("result = %+v, want kind %q", res, tc.want)
			}
		})
	}
}

func TestTCPProberReplyLatencyIncludesTheAnswerTime(t *testing.T) {
	host, port := serve(t, func(c net.Conn) { time.Sleep(150 * time.Millisecond); _, _ = c.Write([]byte("ok")) })
	res := NewTCPProber(plainDial).Probe(context.Background(), Request{TCP: true, Host: host, Port: port, ExpectMode: "contains", ExpectValue: "ok"})
	if !res.OK || res.LatencyMs < 150 {
		t.Fatalf("result = %+v", res)
	}
}

func TestTCPProberCancelWhileWaitingForTheReply(t *testing.T) {
	host, port := serve(t, func(c net.Conn) { time.Sleep(2 * time.Second) })
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	res := NewTCPProber(plainDial).Probe(ctx, Request{TCP: true, Host: host, Port: port, ExpectMode: "contains", ExpectValue: "ok"})
	if !res.Cancelled {
		t.Fatalf("result = %+v", res)
	}
}
