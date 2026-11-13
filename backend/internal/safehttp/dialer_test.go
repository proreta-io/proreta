package safehttp

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

type fixedResolver map[string][]netip.Addr

func (r fixedResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	return r[host], nil
}

// The dialer keeps every address rule of the HTTP client and lifts only the port rule.
func TestDialerBlocksInternalAddressesOnAnyPort(t *testing.T) {
	var dialed []string
	d := NewDialer(Options{
		Resolver: fixedResolver{"internal.example.com": {netip.MustParseAddr("10.1.2.3")}, "mixed.example.com": {netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("127.0.0.1")}},
		dialOverride: func(_ context.Context, _, addr string) (net.Conn, error) {
			dialed = append(dialed, addr)
			return nil, errors.New("not dialed for real")
		},
	})
	for _, addr := range []string{
		"10.0.0.1:5432", "127.0.0.1:6379", "[::1]:25", "169.254.169.254:80", "100.64.0.1:22",
		"internal.example.com:5432", "mixed.example.com:5432",
	} {
		if _, err := d(context.Background(), "tcp", addr); !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("%s: err = %v, want ErrBlockedAddress", addr, err)
		}
	}
	if len(dialed) != 0 {
		t.Fatalf("a blocked destination was dialed: %v", dialed)
	}
	for _, addr := range []string{"93.184.216.34:0", "93.184.216.34:70000", "93.184.216.34:x"} {
		if _, err := d(context.Background(), "tcp", addr); !errors.Is(err, ErrBadPort) {
			t.Errorf("%s: err = %v, want ErrBadPort", addr, err)
		}
	}
	// A public address on a port other than 443 reaches the socket, on that port.
	_, _ = d(context.Background(), "tcp", "93.184.216.34:5432")
	if len(dialed) != 1 || dialed[0] != "93.184.216.34:5432" {
		t.Fatalf("dialed %v", dialed)
	}
}

// The control hook checks the real socket too: a loopback listener is refused without an override.
func TestDialerControlRefusesLoopback(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := NewDialer(Options{})(context.Background(), "tcp", l.Addr().String()); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v", err)
	}
}

func TestPrivateDialerReachesLoopback(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	c, err := NewPrivateDialer(0)(context.Background(), "tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
}

// A first address that never answers (a blackholed AAAA) must not use up the budget of the second.
func TestDialerTriesNextAddressWithinBudget(t *testing.T) {
	var dialed []string
	d := NewDialer(Options{
		Resolver: fixedResolver{"dual.example.com": {netip.MustParseAddr("2606:4700:4700::1111"), netip.MustParseAddr("93.184.216.34")}},
		dialOverride: func(ctx context.Context, _, addr string) (net.Conn, error) {
			dialed = append(dialed, addr)
			if addr[0] == '[' {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			c, _ := net.Pipe()
			return c, nil
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	c, err := d(ctx, "tcp", "dual.example.com:443")
	if err != nil {
		t.Fatalf("err = %v after %v, dialed %v", err, time.Since(start), dialed)
	}
	_ = c.Close()
	if len(dialed) != 2 || time.Since(start) > 1500*time.Millisecond {
		t.Fatalf("dialed %v in %v", dialed, time.Since(start))
	}
}
