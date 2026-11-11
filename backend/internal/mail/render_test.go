package mail

import (
	"strings"
	"testing"
)

func TestRenderTest(t *testing.T) {
	m, err := Render(KindTest, "https://proreta.example.com", "alice@example.org", nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.To != "alice@example.org" || m.Subject != "Proreta test message" ||
		!strings.Contains(m.Text, "Sent by proreta.example.com") || !strings.Contains(m.HTML, "<title>Proreta test message</title>") {
		t.Fatalf("message = %+v", m)
	}
}

func TestRenderUnknownKind(t *testing.T) {
	if _, err := Render("nope", "https://proreta.example.com", "a@example.org", nil); err == nil {
		t.Fatal("unknown kind rendered")
	}
}

func TestCleanParam(t *testing.T) {
	cases := map[string]string{
		"Acme\r\nBcc: x":            "Acme Bcc: x",
		"a\u202eb\u200bc":           "abc",
		"  many   spaces\t here ":   "many spaces here",
		strings.Repeat("x", 300):    strings.Repeat("x", 200) + "…",
		"<script>alert(1)</script>": "<script>alert(1)</script>",
	}
	for in, want := range cases {
		if got := cleanParam(in); got != want {
			t.Errorf("cleanParam(%q) = %q, want %q", in, got, want)
		}
	}
}
