package domains

import (
	"errors"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	rules := NewRules([]string{"Proreta.Example.Net."}, []string{"corp.example.org", ""})
	long63 := strings.Repeat("a", 63)
	tests := []struct {
		name   string
		in     string
		want   string
		reason Reason
	}{
		{"plain", "status.acme.com", "status.acme.com", ""},
		{"uppercase", "Status.ACME.com", "status.acme.com", ""},
		{"trailing dot", "status.acme.com.", "status.acme.com", ""},
		{"spaces around", "  status.acme.com ", "status.acme.com", ""},
		{"digits and hyphen", "status-2.acme.co.uk", "status-2.acme.co.uk", ""},
		{"deep", "a.b.c.acme.com", "a.b.c.acme.com", ""},
		{"63 character label", long63 + ".acme.com", long63 + ".acme.com", ""},
		{"punycode label", "xn--80ak6aa92e.acme.com", "xn--80ak6aa92e.acme.com", ""},
		{"private suffix below a registrable name", "status.user.github.io", "status.user.github.io", ""},
		{"unknown suffix", "status.acme.zzzz", "status.acme.zzzz", ""},

		{"empty", "", "", ReasonInvalid},
		{"two trailing dots", "status.acme.com..", "", ReasonInvalid},
		{"scheme", "https://status.acme.com", "", ReasonInvalid},
		{"port", "status.acme.com:8443", "", ReasonInvalid},
		{"path", "status.acme.com/status", "", ReasonInvalid},
		{"user info", "me@status.acme.com", "", ReasonInvalid},
		{"query", "status.acme.com?x=1", "", ReasonInvalid},
		{"space inside", "sta tus.acme.com", "", ReasonInvalid},
		{"underscore", "_proreta-status.acme.com", "", ReasonInvalid},
		{"underscore inside", "my_status.acme.com", "", ReasonInvalid},
		{"empty label", "status..acme.com", "", ReasonInvalid},
		{"leading dot", ".status.acme.com", "", ReasonInvalid},
		{"hyphen first", "-status.acme.com", "", ReasonInvalid},
		{"hyphen last", "status-.acme.com", "", ReasonInvalid},
		{"64 character label", strings.Repeat("a", 64) + ".acme.com", "", ReasonInvalid},
		{"over 253 characters", strings.Repeat(strings.Repeat("a", 60)+".", 5) + "acme.com", "", ReasonInvalid},
		{"single label", "status", "", ReasonInvalid},
		{"too short", "a.b", "", ReasonInvalid},
		{"broken punycode", "xn--.acme.com", "", ReasonInvalid},
		{"bad punycode", "xn--a-.acme.com", "", ReasonInvalid},

		{"homograph cyrillic a", "аcme.com", "", ReasonNonASCII},
		{"unicode label", "státus.acme.com", "", ReasonNonASCII},
		{"ideographic dot", "status。acme.com", "", ReasonNonASCII},

		{"wildcard", "*.status.acme.com", "", ReasonWildcard},
		{"wildcard in label", "st*.acme.com", "", ReasonWildcard},

		{"ipv4", "192.168.1.10", "", ReasonIP},
		{"ipv4 loopback", "127.0.0.1", "", ReasonIP},
		{"ipv4 short form", "127.1", "", ReasonIP},
		{"ipv4 decimal", "2130706433", "", ReasonInvalid}, // a single label
		{"ipv4 decimal with label", "a.2130706433", "", ReasonIP},
		{"ipv4 hex", "0x7f.0.0.1", "", ReasonIP},
		{"ipv4 hex last", "a.0x7f000001", "", ReasonIP},
		{"ipv6", "::1", "", ReasonInvalid},
		{"ipv6 brackets", "[::1]", "", ReasonInvalid},
		{"ipv6 mapped", "::ffff:1.2.3.4", "", ReasonInvalid},

		{"localhost", "localhost", "", ReasonInvalid},
		{"localhost subdomain", "status.localhost", "", ReasonReserved},
		{"internal", "status.internal", "", ReasonReserved},
		{"internal below a name", "status.acme.internal", "", ReasonReserved},
		{"local", "printer.local", "", ReasonReserved},
		{"lan", "nas.lan", "", ReasonReserved},
		{"home arpa", "nas.home.arpa", "", ReasonReserved},
		{"test", "status.test", "", ReasonReserved},
		{"invalid", "status.invalid", "", ReasonReserved},
		{"onion", "abc.onion", "", ReasonReserved},
		{"example", "status.example", "", ReasonReserved},
		{"arpa", "1.0.0.127.in-addr.arpa", "", ReasonReserved},
		{"localdomain", "host.localdomain", "", ReasonReserved},
		{"suffix lookalike is fine", "status.acme.localhost.com", "status.acme.localhost.com", ""},

		{"public suffix", "co.uk", "", ReasonPublicSuffix},
		{"tld", "com", "", ReasonInvalid},
		{"private public suffix", "github.io", "", ReasonPublicSuffix},
		{"registrable", "acme.com", "", ReasonRegistrable},
		{"registrable under country suffix", "acme.co.uk", "", ReasonRegistrable},
		{"registrable under private suffix", "user.github.io", "", ReasonRegistrable},

		{"installation host", "proreta.example.net", "", ReasonInstallation},
		{"installation host uppercase", "PRORETA.example.net", "", ReasonInstallation},
		{"below the installation host", "status.proreta.example.net", "", ReasonInstallation},
		{"operator suffix itself", "corp.example.org", "", ReasonInstallation},
		{"below the operator suffix", "status.corp.example.org", "", ReasonInstallation},
		{"sibling of the operator suffix", "status.other.example.org", "status.other.example.org", ""},
		{"suffix as a string suffix only", "status.mycorp.example.org", "status.mycorp.example.org", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rules.Validate(tc.in)
			if tc.reason == "" {
				if err != nil || got != tc.want {
					t.Fatalf("Validate(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
				}
				return
			}
			var he *HostError
			if !errors.As(err, &he) || he.Reason != tc.reason || !errors.Is(err, ErrHost) {
				t.Fatalf("Validate(%q) = %q, %v; want reason %q", tc.in, got, err, tc.reason)
			}
			if he.Message() == "" {
				t.Fatal("empty message")
			}
		})
	}
}

func TestDisplay(t *testing.T) {
	if got := Display("status.acme.com"); got != "status.acme.com" {
		t.Fatalf("ASCII name changed: %q", got)
	}
	if got := Display("xn--80ak6aa92e.com"); got != "аррӏе.com" {
		t.Fatalf("Display = %q", got)
	}
}

func TestToken(t *testing.T) {
	a, err := NewToken()
	if err != nil || !ValidToken(a) {
		t.Fatalf("NewToken = %q, %v", a, err)
	}
	if b, _ := NewToken(); a == b {
		t.Fatal("two tokens are equal")
	}
	for _, bad := range []string{"", "abc", strings.Repeat("A", 32), strings.Repeat("g", 32), strings.Repeat("a", 33)} {
		if ValidToken(bad) {
			t.Fatalf("ValidToken(%q)", bad)
		}
	}
	if TXTName("status.acme.com") != "_proreta-status.status.acme.com" || TXTValue(a) != "proreta-verify="+a {
		t.Fatal("record name or value")
	}
}
