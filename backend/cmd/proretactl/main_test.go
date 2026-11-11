package main

import (
	"bytes"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/testsupport/smtptest"
)

func pipeStdin(t *testing.T, s string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = io.WriteString(w, s)
		_ = w.Close()
	}()
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestHashpwMemoryCap(t *testing.T) {
	var out bytes.Buffer
	err := hashpw([]string{"-m", "262145", "-t", "1", "-p", "1"}, pipeStdin(t, "pw\n"), &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "256 MiB") {
		t.Fatalf("err = %v, want the 256 MiB cap", err)
	}

	out.Reset()
	if err := hashpw([]string{"-m", "64", "-t", "1", "-p", "1"}, pipeStdin(t, "pw\n"), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if ok, err := crypto.VerifyPassword("pw", strings.TrimSpace(out.String())); err != nil || !ok {
		t.Fatalf("hash does not verify: ok=%v err=%v", ok, err)
	}
}

func TestMailTest(t *testing.T) {
	srv := smtptest.New(t, smtptest.Options{Mode: smtptest.Plain})
	env := map[string]string{
		"PRORETA_SMTP_HOST": srv.Host(), "PRORETA_SMTP_PORT": strconv.Itoa(srv.Port()), "PRORETA_SMTP_TLS": "none",
		"PRORETA_SMTP_FROM": "proreta@example.com", "PRORETA_PUBLIC_URL": "https://proreta.example.com",
	}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }

	var out bytes.Buffer
	if err := mailTest([]string{"-to", "ops@example.org"}, lookup, &out); err != nil {
		t.Fatal(err)
	}
	got := srv.Messages()
	if len(got) != 1 || got[0].To[0] != "ops@example.org" || !strings.Contains(out.String(), "accepted") {
		t.Fatalf("messages %+v, output %q", got, out.String())
	}

	srv.Reply("RCPT", "550 5.1.1 mailbox ops@example.org unknown")
	err := mailTest([]string{"-to", "ops@example.org"}, lookup, &out)
	if err == nil || !strings.Contains(err.Error(), "recipient_rejected 550") || strings.Contains(err.Error(), "unknown") {
		t.Fatalf("err = %v, want the failing step without the server's text", err)
	}
	if err := mailTest(nil, lookup, &out); err == nil || !strings.Contains(err.Error(), "-to") {
		t.Fatalf("missing -to: %v", err)
	}
	delete(env, "PRORETA_SMTP_HOST")
	if err := mailTest([]string{"-to", "a@example.org"}, lookup, &out); err == nil {
		t.Fatal("email not configured must fail")
	}
}
