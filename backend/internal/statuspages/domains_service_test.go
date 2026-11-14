package statuspages

import (
	"context"
	"slices"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/statuspages/domains"
)

func TestLastError(t *testing.T) {
	p, m, u := domains.Present, domains.Missing, domains.Unknown
	tests := []struct {
		name  string
		f     domains.Facts
		state domains.State
		want  string // "" means nil
	}{
		{"all good", domains.Facts{TXT: p, CNAME: p}, domains.Active, ""},
		{"serving, TXT gone, CNAME right", domains.Facts{TXT: m, CNAME: p}, domains.Active, ""},
		{"pending, CNAME alone", domains.Facts{TXT: m, CNAME: p}, domains.Pending, "txt-missing"},
		{"TXT right, CNAME missing", domains.Facts{TXT: p, CNAME: m}, domains.Verified, "cname-missing"},
		{"TXT right, CNAME lookup failed", domains.Facts{TXT: p, CNAME: u}, domains.Verified, "dns-error"},
		{"TXT lookup failed", domains.Facts{TXT: u, CNAME: m}, domains.Pending, "dns-error"},
		{"nothing", domains.Facts{TXT: m, CNAME: m}, domains.Pending, "txt-missing"},
	}
	for _, tc := range tests {
		got := lastError(tc.f, tc.state)
		switch {
		case tc.want == "" && got != nil, tc.want != "" && (got == nil || *got != tc.want):
			t.Errorf("%s: %v, want %q", tc.name, got, tc.want)
		}
	}
}

// Only well-formed proof values count as tokens found in DNS; the holder's token is compared in the
// database, never here.
func TestTokensInDNS(t *testing.T) {
	a, b := "0123456789abcdef0123456789abcdef", "fedcba9876543210fedcba9876543210"
	fake := &domains.Fake{TXT: map[string][]string{domains.TXTName("status.acme.com"): {
		" " + domains.TXTValue(a) + " ", "v=spf1 -all", domains.TXTValue("short"), domains.TXTValue(b),
	}}}
	s := &Service{dom: DomainOptions{Resolver: fake}}
	got := s.tokensInDNS(context.Background(), "status.acme.com")
	if !slices.Equal(got, []string{a, b}) {
		t.Fatalf("tokens: %v", got)
	}
	if got := s.tokensInDNS(context.Background(), "other.acme.com"); got != nil {
		t.Fatalf("tokens of a host without records: %v", got)
	}
}

func TestProofText(t *testing.T) {
	for p, want := range map[domains.Proof]string{domains.Present: "present", domains.Missing: "missing", domains.Unknown: "unknown"} {
		if got := proofText(p); got != want {
			t.Errorf("%d: %s", p, got)
		}
	}
}
