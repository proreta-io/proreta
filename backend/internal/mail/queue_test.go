package mail_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/proreta-io/proreta/backend/internal/mail"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

func TestQueueDisabled(t *testing.T) {
	var nilQueue *mail.Queue
	if nilQueue.Enabled() || mail.NewQueue(nil, nil).Enabled() {
		t.Fatal("a queue without keys is enabled")
	}
	if err := mail.NewQueue(nil, nil).Enqueue(context.Background(), nil, mail.Request{}); !errors.Is(err, mail.ErrDisabled) {
		t.Fatalf("err = %v", err)
	}
}

func TestIntegrationQueue(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("mailqueue"))
	ctx := context.Background()
	keys, err := crypto.NewKeyring(map[string][]byte{"k1": bytes.Repeat([]byte{7}, 32)}, "k1")
	if err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewRegistry()
	q := mail.NewQueue(keys, reg)
	if !q.Enabled() {
		t.Fatal("queue with keys is disabled")
	}
	api := env.Conn(t, env.APIURL)
	admin := env.AdminConn(t)
	soon := time.Now().Add(time.Hour)

	t.Run("a committed message is sealed and opens only for its row", func(t *testing.T) {
		tx, _ := api.Begin(ctx)
		if err := q.Enqueue(ctx, tx, mail.Request{Kind: mail.KindTest, To: "alice@example.org", Params: map[string]string{"note": "n"}, ExpiresAt: soon}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		var id uuid.UUID
		var kind string
		var sealed []byte
		if err := admin.QueryRow(ctx, `SELECT id, kind, payload FROM mail_outbox`).Scan(&id, &kind, &sealed); err != nil {
			t.Fatal(err)
		}
		if kind != "test" || strings.Contains(string(sealed), "alice") {
			t.Fatalf("kind=%s sealed=%q", kind, sealed)
		}
		to, params, err := mail.Open(keys, id, sealed)
		if err != nil || to != "alice@example.org" || params["note"] != "n" {
			t.Fatalf("open: %q %v %v", to, params, err)
		}
		if _, _, err := mail.Open(keys, uuid.New(), sealed); err == nil {
			t.Fatal("payload opened under another row id")
		}
		if v := counter(t, reg, "test"); v != 1 {
			t.Fatalf("enqueued metric = %v", v)
		}
	})

	t.Run("a rolled-back transaction leaves nothing", func(t *testing.T) {
		tx, _ := api.Begin(ctx)
		if err := q.Enqueue(ctx, tx, mail.Request{Kind: mail.KindTest, To: "bob@example.org", ExpiresAt: soon}); err != nil {
			t.Fatal(err)
		}
		_ = tx.Rollback(ctx)
		var n int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM mail_outbox`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("rows = %d, %v", n, err)
		}
	})

	t.Run("invalid requests are refused before the database", func(t *testing.T) {
		for name, r := range map[string]mail.Request{
			"unknown kind":     {Kind: "nope", To: "a@example.org", ExpiresAt: soon},
			"no recipient":     {Kind: mail.KindTest, ExpiresAt: soon},
			"header injection": {Kind: mail.KindTest, To: "a@example.org\r\nBcc: b@example.org", ExpiresAt: soon},
			"display name":     {Kind: mail.KindTest, To: "A <a@example.org>", ExpiresAt: soon},
			"already expired":  {Kind: mail.KindTest, To: "a@example.org", ExpiresAt: time.Now().Add(-time.Minute)},
			"huge params":      {Kind: mail.KindTest, To: "a@example.org", Params: map[string]string{"x": strings.Repeat("y", 4000)}, ExpiresAt: soon},
		} {
			if err := q.Enqueue(ctx, nil, r); err == nil {
				t.Errorf("%s: accepted", name)
			}
		}
	})
}

func counter(t *testing.T, reg *prometheus.Registry, kind string) float64 {
	t.Helper()
	fams, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() != "proreta_mail_enqueued_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			if m.GetLabel()[0].GetValue() == kind {
				return m.GetCounter().GetValue()
			}
		}
	}
	t.Fatal("metric missing")
	return 0
}
