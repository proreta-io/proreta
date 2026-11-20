// Package cmdtest runs the run function of a cmd/* binary the way main does, for the smoke tests
// of the entry points.
package cmdtest

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
)

// RunFunc is the signature every cmd/* run function has.
type RunFunc func(ctx context.Context, lookup func(string) (string, bool)) error

// Env is a set of environment variables.
type Env map[string]string

// Lookup is the lookup function run receives.
func (e Env) Lookup(k string) (string, bool) { v, ok := e[k]; return v, ok }

// With returns a copy of e with the extra pairs added.
func (e Env) With(kv ...string) Env {
	out := Env{}
	for k, v := range e {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

// Keys is a valid PRORETA_ENCRYPTION_KEYS / PRORETA_ENCRYPTION_ACTIVE_KEY_ID pair.
func Keys() (keys, activeID string) {
	return `{"k1":"` + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))) + `"}`, "k1"
}

// AdminHash is an argon2id hash at the lowest cost the configuration accepts.
func AdminHash(t testing.TB) string {
	t.Helper()
	h, err := crypto.HashPassword("pw-for-smoke-tests", crypto.Argon2Params{Memory: 19 * 1024, Iterations: 2, Parallelism: 1, SaltLength: 16, KeyLength: 32})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// FreeAddr returns a loopback address whose port was free a moment ago.
func FreeAddr(t testing.TB) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// Fails runs run with env and returns its error; it fails the test if run does not return one
// quickly (a binary that accepted the environment would block serving).
func Fails(t testing.TB, run RunFunc, env Env) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := run(ctx, env.Lookup)
	if err == nil {
		t.Fatal("run succeeded, want an error")
	}
	return err
}

// Serves runs run with env until http://adminAddr/readyz answers 200, checks that /metrics names
// the service, cancels the context and requires run to return nil: the graceful stop.
func Serves(t testing.TB, run RunFunc, env Env, adminAddr, service string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, env.Lookup) }()

	get := func(path string) (int, string, error) {
		resp, err := http.Get("http://" + adminAddr + path)
		if err != nil {
			return 0, "", err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), nil
	}
	// The listener opens a moment after run starts; this is a bounded wait on a real socket, not on a clock.
	deadline := time.After(30 * time.Second)
	for ready := false; !ready; {
		select {
		case err := <-done:
			t.Fatalf("run returned before it was ready: %v", err)
		case <-deadline:
			t.Fatal("not ready within 30s")
		case <-time.After(50 * time.Millisecond):
			code, _, err := get("/readyz")
			ready = err == nil && code == http.StatusOK
		}
	}
	if _, body, err := get("/metrics"); err != nil || !strings.Contains(body, `service="`+service+`"`) {
		t.Errorf("metrics do not carry service=%q (err %v)", service, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("stop returned %v, want nil", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("run did not stop after the context was cancelled")
	}
}
