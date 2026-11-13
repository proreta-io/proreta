package domains

import (
	"context"
	"errors"
	"testing"
	"time"
)

const (
	testHost   = "status.acme.com"
	testTarget = "customers.proreta.example.net"
	testToken  = "0123456789abcdef0123456789abcdef"
)

func TestProbe(t *testing.T) {
	txt := map[string][]string{TXTName(testHost): {"v=spf1 -all", TXTValue(testToken)}}
	tests := []struct {
		name string
		r    *Fake
		want Facts
	}{
		{"both right", &Fake{TXT: txt, CNAME: map[string]string{testHost: testTarget}}, Facts{Present, Present}},
		{"cname chain", &Fake{TXT: txt, CNAME: map[string]string{testHost: "edge.cdn.net", "edge.cdn.net": testTarget}}, Facts{Present, Present}},
		{"target in other case and with a dot", &Fake{TXT: txt, CNAME: map[string]string{testHost: "Customers.Proreta.Example.Net."}}, Facts{Present, Present}},
		{"no records at all", &Fake{}, Facts{Missing, Missing}},
		{"txt for another token", &Fake{TXT: map[string][]string{TXTName(testHost): {TXTValue("ffffffffffffffffffffffffffffffff")}}}, Facts{Missing, Missing}},
		{"stale cname to us without the token", &Fake{CNAME: map[string]string{testHost: testTarget}}, Facts{Missing, Present}},
		{"cname elsewhere", &Fake{TXT: txt, CNAME: map[string]string{testHost: "other.example.org"}}, Facts{Present, Missing}},
		{"cname loop is unknown", &Fake{TXT: txt, CNAME: map[string]string{testHost: "a.net", "a.net": testHost}}, Facts{Present, Unknown}},
		{"chain over eight hops is unknown", &Fake{TXT: txt, CNAME: chain(testHost, 10, testTarget)}, Facts{Present, Unknown}},
		{"servfail on txt", &Fake{Fail: map[string]bool{TXTName(testHost): true}, CNAME: map[string]string{testHost: testTarget}}, Facts{Unknown, Present}},
		{"servfail on both", &Fake{Fail: map[string]bool{TXTName(testHost): true, testHost: true}}, Facts{Unknown, Unknown}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Probe(context.Background(), tc.r, testHost, testToken, testTarget); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestProbeNoTargetNeverMatches(t *testing.T) {
	r := &Fake{CNAME: map[string]string{testHost: "x.net"}}
	if got := Probe(context.Background(), r, testHost, testToken, ""); got.CNAME != Missing {
		t.Fatalf("%+v", got)
	}
}

func TestProbeSlowAnswerIsUnknown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r := &Fake{Delay: time.Second, CNAME: map[string]string{testHost: testTarget}}
	if got := Probe(ctx, r, testHost, testToken, testTarget); got != (Facts{Unknown, Unknown}) {
		t.Fatalf("%+v", got)
	}
}

func TestFakeNotFound(t *testing.T) {
	r := &Fake{}
	if _, err := r.LookupTXT(context.Background(), "x.net"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := r.LookupCNAME(context.Background(), "x.net"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func chain(from string, n int, last string) map[string]string {
	m := map[string]string{}
	cur := from
	for i := 0; i < n; i++ {
		next := "hop" + string(rune('a'+i)) + ".net"
		m[cur] = next
		cur = next
	}
	m[cur] = last
	return m
}
