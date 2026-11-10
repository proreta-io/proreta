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

func TestRenderVerifyEmail(t *testing.T) {
	const token = "tok_ABC-123_xyz"
	m, err := Render(KindVerifyEmail, "https://proreta.example.com", "alice@example.org",
		map[string]string{"token": token, "email": "alice@example.org", "expires": "24 hours"})
	if err != nil {
		t.Fatal(err)
	}
	link := "https://proreta.example.com/verify-email#" + token
	if m.Subject != "Confirm your email address for Proreta" || !strings.Contains(m.Text, link+"\n") || !strings.Contains(m.HTML, `href="`+link+`"`) ||
		!strings.Contains(m.Text, "expires in 24 hours") {
		t.Fatalf("message = %+v", m)
	}
	for _, body := range []string{m.Text, m.HTML} {
		if strings.Count(body, token) < 1 || strings.Contains(body, "?token") || strings.Contains(body, "<img") {
			t.Errorf("unexpected body: %s", body)
		}
	}
}

func TestRenderEscapesParameters(t *testing.T) {
	m, err := Render(KindVerifyEmail, "https://proreta.example.com", "a@example.org",
		map[string]string{"token": "t", "email": `"><script>alert(1)</script>@x.org`, "expires": "24 hours"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.HTML, "<script>") || !strings.Contains(m.HTML, "&lt;script&gt;") {
		t.Fatalf("html not escaped: %s", m.HTML)
	}
}

func TestRenderRejectsBadTokens(t *testing.T) {
	for _, tok := range []string{"", "a#b", "a b", "a/b", "a?b", "a\nb"} {
		if _, err := Render(KindVerifyEmail, "https://proreta.example.com", "a@example.org", map[string]string{"token": tok, "email": "a@example.org", "expires": "1 hour"}); err == nil {
			t.Errorf("token %q rendered", tok)
		}
	}
}
