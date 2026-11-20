package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
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
	err := hashpw([]string{"-m", "262145", "-t", "1", "-p", "1"}, pipeStdin(t, "a long enough password\n"), &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "128 MiB") {
		t.Fatalf("err = %v, want the 128 MiB cap", err)
	}

	out.Reset()
	if err := hashpw([]string{"-m", "64", "-t", "1", "-p", "1"}, pipeStdin(t, "a long enough password\n"), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if ok, err := crypto.VerifyPassword("a long enough password", strings.TrimSpace(out.String())); err != nil || !ok {
		t.Fatalf("hash does not verify: ok=%v err=%v", ok, err)
	}
}

func TestHashpwPolicy(t *testing.T) {
	for name, in := range map[string]string{
		"short":    "short\n",
		"too long": strings.Repeat("x", 129) + "\n",
		"huge":     strings.Repeat("x", 5000) + "\n",
	} {
		if err := hashpw([]string{"-m", "64", "-t", "1", "-p", "1"}, pipeStdin(t, in), io.Discard, io.Discard); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
