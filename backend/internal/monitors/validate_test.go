package monitors

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/safehttp"
)

func TestValidateURL(t *testing.T) {
	cases := []struct {
		in   string
		ok   bool
		want string // normalised URL when ok
	}{
		{"https://example.com", true, "https://example.com"},
		{"https://example.com/health?x=1#top", true, "https://example.com/health?x=1#top"},
		{"  https://example.com/  ", true, "https://example.com/"},
		{"HTTPS://Example.COM/Path", true, "https://example.com/Path"},
		{"https://example.com:443/", true, "https://example.com:443/"},
		{"https://example.com./", true, "https://example.com./"},
		{"https://xn--exmple-cua.com/", true, "https://xn--exmple-cua.com/"},
		{"https://8.8.8.8/", true, "https://8.8.8.8/"},
		{"https://[2001:4860:4860::8888]/", true, "https://[2001:4860:4860::8888]/"},
		{"https://under_score.example.com/", true, "https://under_score.example.com/"},

		// Scheme, port, credentials, shape.
		{"", false, ""},
		{"http://example.com", false, ""},
		{"ftp://example.com", false, ""},
		{"example.com", false, ""},
		{"https:example.com", false, ""},
		{"https:///path", false, ""},
		{"https://example.com:8443/", false, ""},
		{"https://example.com:80/", false, ""},
		{"https://user:pass@example.com/", false, ""},
		{"https://user@example.com/", false, ""},
		{"https://example.com\x00/", false, ""},
		{"https://exa mple.com/", false, ""},
		{"https://" + strings.Repeat("a", 2040) + ".com/", false, ""},

		// Internal names.
		{"https://localhost/", false, ""},
		{"https://LOCALHOST./", false, ""},
		{"https://app.localhost/", false, ""},
		{"https://metadata.google.internal/", false, ""},
		{"https://printer.local/", false, ""},
		{"https://proreta-api.proreta.svc/", false, ""},
		{"https://kubernetes.default.svc.cluster.local/", false, ""},
		{"https://intranet/", false, ""},
		{"https://%6c%6f%63%61%6c%68%6f%73%74/", false, ""},

		// IPv4 literals in blocked ranges and non-canonical forms.
		{"https://127.0.0.1/", false, ""},
		{"https://127.0.0.1./", false, ""},
		{"https://10.1.2.3/", false, ""},
		{"https://172.16.0.1/", false, ""},
		{"https://192.168.1.1/", false, ""},
		{"https://169.254.169.254/", false, ""},
		{"https://100.64.0.1/", false, ""},
		{"https://0.0.0.0/", false, ""},
		{"https://0.1.2.3/", false, ""},
		{"https://224.0.0.1/", false, ""},
		{"https://255.255.255.255/", false, ""},
		{"https://2130706433/", false, ""}, // decimal 127.0.0.1
		{"https://0177.0.0.1/", false, ""}, // octal
		{"https://0x7f.0.0.1/", false, ""}, // hex octet
		{"https://0x7f000001/", false, ""}, // hex
		{"https://127.1/", false, ""},      // short form
		{"https://134744072/", false, ""},  // decimal 8.8.8.8: public, but not canonical
		{"https://08.8.8.8/", false, ""},   // leading zero
		{"https://%31%32%37.0.0.1/", false, ""},

		// IPv6 literals.
		{"https://[::1]/", false, ""},
		{"https://[::]/", false, ""},
		{"https://[fc00::1]/", false, ""},
		{"https://[fd12:3456::1]/", false, ""},
		{"https://[fe80::1]/", false, ""},
		{"https://[fe80::1%25eth0]/", false, ""},
		{"https://[::ffff:127.0.0.1]/", false, ""},
		{"https://[::ffff:8.8.8.8]/", false, ""}, // any IPv4-mapped address
		{"https://[::127.0.0.1]/", false, ""},    // IPv4-compatible
		{"https://[64:ff9b::a00:1]/", false, ""}, // NAT64 of 10.0.0.1
		{"https://[ff02::1]/", false, ""},
		{"https://[::1]:443/", false, ""},
		// Ranges the validator shares with the connect-time guard (safehttp.Allowed).
		{"https://192.0.2.1/", false, ""},         // documentation
		{"https://198.18.0.1/", false, ""},        // benchmarking
		{"https://[2001:db8::1]/", false, ""},     // documentation
		{"https://[2002:808:808::1]/", false, ""}, // 6to4
		{"https://[4000::1]/", false, ""},         // IPv6 outside 2000::/3
		{"https://[2606:4700:4700::1111]/", true, "https://[2606:4700:4700::1111]/"},

		// Unicode hosts (homographs, fullwidth digits) must use their xn-- form.
		{"https://exämple.com/", false, ""},
		{"https://ｌｏｃａｌｈｏｓｔ/", false, ""},
		{"https://１２７.0.0.1/", false, ""},
	}
	for _, c := range cases {
		got, msg := ValidateURL(c.in)
		if c.ok && (msg != "" || got != c.want) {
			t.Errorf("ValidateURL(%q) = %q, %q; want %q accepted", c.in, got, msg, c.want)
		}
		if !c.ok && msg == "" {
			t.Errorf("ValidateURL(%q) = %q accepted; want rejected", c.in, got)
		}
	}
}

// The validator and the guard must agree on every literal address.
func TestValidateURLAgreesWithGuard(t *testing.T) {
	for _, a := range []string{"8.8.8.8", "10.0.0.1", "192.0.2.1", "203.0.113.9", "100.64.1.1", "240.0.0.1",
		"2606:4700:4700::1111", "2001:db8::1", "64:ff9b::808:808", "fc00::1", "4000::1", "2002::1"} {
		addr := netip.MustParseAddr(a)
		host := a
		if addr.Is6() {
			host = "[" + a + "]"
		}
		_, msg := ValidateURL("https://" + host + "/")
		if (msg == "") != safehttp.Allowed(addr) {
			t.Errorf("%s: validator accepted=%v, guard allowed=%v", a, msg == "", safehttp.Allowed(addr))
		}
	}
}

func TestValidateName(t *testing.T) {
	if got, msg := ValidateName("  Website  "); msg != "" || got != "Website" {
		t.Fatalf("got %q, %q", got, msg)
	}
	if _, msg := ValidateName(strings.Repeat("ж", 80)); msg != "" {
		t.Fatalf("80 characters rejected: %s", msg)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("ж", 81), "a\nb", "a\x00b"} {
		if _, msg := ValidateName(bad); msg == "" {
			t.Errorf("ValidateName(%q) accepted", bad)
		}
	}
}

func TestValidateNameFormatCharacters(t *testing.T) {
	cases := []struct {
		name, in string
		ok       bool
	}{
		{"right-to-left override", "pay\u202Egnp.exe", false},
		{"left-to-right embedding", "a\u202Ab", false},
		{"pop directional formatting", "a\u202Cb", false},
		{"right-to-left isolate", "a\u2067b\u2069", false},
		{"left-to-right mark", "a\u200Eb", false},
		{"zero-width space", "Web\u200Bsite", false},
		{"zero-width joiner", "a\u200Db", false},
		{"zero-width non-joiner", "a\u200Cb", false},
		{"word joiner", "a\u2060b", false},
		{"byte order mark", "\uFEFFWebsite", false},
		{"soft hyphen", "Web\u00ADsite", false},
		{"Arabic letter mark", "a\u061Cb", false},
		{"tag character", "a\U000E0041b", false},
		{"plain Cyrillic", "Сайт клієнта", true},
		{"Arabic letters", "موقع", true},
		{"emoji with variation selector", "Shop \u2764\uFE0F", true},
		{"punctuation", "API (EU) - v2 / prod #1", true},
	}
	for _, c := range cases {
		_, msg := ValidateName(c.in)
		if (msg == "") != c.ok {
			t.Errorf("%s: ValidateName(%q) msg=%q, want ok=%t", c.name, c.in, msg, c.ok)
		}
	}
}

func TestValidateExpectedStatus(t *testing.T) {
	for _, ok := range []string{"2xx", "200", "204", "299", "400", "418", "503", "599"} {
		if msg := ValidateExpectedStatus(ok); msg != "" {
			t.Errorf("%q rejected: %s", ok, msg)
		}
	}
	for _, bad := range []string{"", "2XX", "3xx", "99", "600", "099", "20", "2000", " 200", "200 ", "100", "101", "199", "300", "301", "304", "399", "4xx"} {
		if msg := ValidateExpectedStatus(bad); msg == "" {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestValidatePrivateURL(t *testing.T) {
	good := [][2]string{
		{"http://orders.shop.svc.cluster.local:8080/health", "http://orders.shop.svc.cluster.local:8080/health"},
		{"HTTP://Orders:8080", "http://orders:8080"},
		{"https://10.1.2.3/ping", "https://10.1.2.3/ping"},
		{"http://[fd00::1]:9000/", "http://[fd00::1]:9000/"},
		{"http://localhost:3000", "http://localhost:3000"},
		{"https://example.com/x?token=1", "https://example.com/x?token=1"},
	}
	for _, c := range good {
		in, want := c[0], c[1]
		if got, msg := ValidatePrivateURL(in); msg != "" || got != want {
			t.Errorf("%s: got %q, %q; want %q", in, got, msg, want)
		}
	}
	for in, want := range map[string]string{
		"":                     "is required",
		"ftp://svc":            "must use http or https",
		"http://":              "must have a host",
		"http://user:pw@svc/":  "must not contain a user name or password",
		"http://svc:0/":        "has an invalid port",
		"http://svc:70000/":    "has an invalid port",
		"http://bad host/":     "is not a valid URL",
		"http://2130706433/":   "IP addresses must be written as four decimal numbers (a.b.c.d)",
		"http://0x7f.1/":       "IP addresses must be written as four decimal numbers (a.b.c.d)",
		"http://[::1%25eth0]/": "host is not a valid IPv6 address",
		"http://" + strings.Repeat("a", 64) + ".example.com/": "host is not a valid domain name",
	} {
		if _, msg := ValidatePrivateURL(in); msg != want {
			t.Errorf("%q: message %q, want %q", in, msg, want)
		}
	}
}

func TestValidateHost(t *testing.T) {
	for _, tc := range []struct {
		in       string
		specula  bool
		want     string
		wantFail bool
	}{
		{"DB.Example.com", false, "db.example.com", false},
		{" 93.184.216.34 ", false, "93.184.216.34", false},
		{"2606:4700::1111", false, "2606:4700::1111", false},
		{"db.example.com.", false, "db.example.com.", false},
		{"postgres", true, "postgres", false},
		{"10.0.0.5", true, "10.0.0.5", false},
		{"db.svc.cluster.local", true, "db.svc.cluster.local", false},
		{"[fd00::1]", true, "fd00::1", false},
		{"postgres", false, "", true},
		{"10.0.0.5", false, "", true},
		{"localhost", false, "", true},
		{"::1", false, "", true},
		{"", true, "", true},
		{"db.example.com:5432", false, "", true},
		{"https://db.example.com", false, "", true},
		{"user@db.example.com", false, "", true},
		{"db.example.com/x", true, "", true},
		{"db example.com", true, "", true},
		{"bücher.example", true, "", true},
	} {
		got, msg := ValidateHost(tc.in, tc.specula)
		if (msg != "") != tc.wantFail || got != tc.want {
			t.Errorf("ValidateHost(%q, %v) = %q, %q; want %q, fail=%v", tc.in, tc.specula, got, msg, tc.want, tc.wantFail)
		}
	}
}

func TestValidatePort(t *testing.T) {
	for n, ok := range map[int]bool{0: false, -1: false, 1: true, 443: true, 65535: true, 65536: false} {
		if (ValidatePort(n) == "") != ok {
			t.Errorf("ValidatePort(%d) = %q", n, ValidatePort(n))
		}
	}
}
