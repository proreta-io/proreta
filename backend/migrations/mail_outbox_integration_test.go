package migrations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// TestIntegrationMailOutbox checks the grants around mail_outbox and claim_mail, finish_mail,
// mail_pending and purge_mail_outbox: the api can only insert, the worker only calls the
// functions, a finished row keeps no payload, leases expire, and an expired message is dropped.
func TestIntegrationMailOutbox(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("mailoutbox"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	api := env.Conn(t, env.APIURL)
	worker := env.Conn(t, env.WorkerURL)

	insert := func(conn *pgx.Conn, expires string) (string, error) {
		id := uuid.Must(uuid.NewV7()).String()
		_, err := conn.Exec(ctx, `INSERT INTO mail_outbox (id, kind, payload, expires_at) VALUES ($1, 'test', '\x0102', `+expires+`)`, id)
		return id, err
	}

	t.Run("the api can only insert", func(t *testing.T) {
		if _, err := insert(api, "now() + interval '1 hour'"); err != nil {
			t.Fatalf("api insert: %v", err)
		}
		for _, stmt := range []string{
			`SELECT count(*) FROM mail_outbox`, `UPDATE mail_outbox SET status = 'sent'`, `DELETE FROM mail_outbox`,
			`SELECT claim_mail(10)`, `SELECT purge_mail_outbox('0 seconds')`,
		} {
			if _, err := api.Exec(ctx, stmt); err == nil || !strings.Contains(err.Error(), "permission denied") {
				t.Errorf("api %q: err = %v, want permission denied", stmt, err)
			}
		}
	})

	t.Run("the worker role has no direct access", func(t *testing.T) {
		for _, stmt := range []string{
			`SELECT count(*) FROM mail_outbox`, `INSERT INTO mail_outbox (id, kind, payload, expires_at) VALUES (gen_random_uuid(), 'test', '\x01', now() + interval '1 hour')`,
			`UPDATE mail_outbox SET status = 'sent'`, `DELETE FROM mail_outbox`,
		} {
			if _, err := worker.Exec(ctx, stmt); err == nil || !strings.Contains(err.Error(), "permission denied") {
				t.Errorf("worker %q: err = %v, want permission denied", stmt, err)
			}
		}
	})

	t.Run("table constraints", func(t *testing.T) {
		for name, stmt := range map[string]string{
			"empty payload while pending": `INSERT INTO mail_outbox (id, kind, payload, expires_at) VALUES (gen_random_uuid(), 'test', '', now() + interval '1 hour')`,
			"bad kind":                    `INSERT INTO mail_outbox (id, kind, payload, expires_at) VALUES (gen_random_uuid(), 'Test!', '\x01', now() + interval '1 hour')`,
			"expiry in the past":          `INSERT INTO mail_outbox (id, kind, payload, expires_at) VALUES (gen_random_uuid(), 'test', '\x01', now() - interval '1 hour')`,
			"sent row still holding data": `INSERT INTO mail_outbox (id, kind, payload, status, finished_at, expires_at) VALUES (gen_random_uuid(), 'test', '\x01', 'sent', now(), now() + interval '1 hour')`,
			"in progress without a lease": `INSERT INTO mail_outbox (id, kind, payload, status, expires_at) VALUES (gen_random_uuid(), 'test', '\x01', 'in_progress', now() + interval '1 hour')`,
		} {
			if _, err := admin.Exec(ctx, stmt); err == nil || !strings.Contains(err.Error(), "violates check constraint") {
				t.Errorf("%s: err = %v, want a check violation", name, err)
			}
		}
	})

	pgtest.MustExec(t, admin, `DELETE FROM mail_outbox`)
	if _, err := insert(admin, "now() + interval '1 hour'"); err != nil {
		t.Fatal(err)
	}
	b, _ := insert(admin, "now() + interval '1 hour'")
	type claimed struct {
		id, token string
		attempts  int
		payload   []byte
	}
	claim := func(n int) []claimed {
		t.Helper()
		rows, _ := worker.Query(ctx, `SELECT m.id::text, m.claim_token::text, m.attempts, m.payload FROM claim_mail($1) AS m ORDER BY m.id`, n)
		out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (claimed, error) {
			var c claimed
			return c, r.Scan(&c.id, &c.token, &c.attempts, &c.payload)
		})
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		return out
	}
	finish := func(id, token, status, errText, retry string) bool {
		t.Helper()
		var ok bool
		if err := worker.QueryRow(ctx, `SELECT finish_mail($1, $2, $3, $4, `+retry+`)`, id, token, status, errText).Scan(&ok); err != nil {
			t.Fatalf("finish %s: %v", status, err)
		}
		return ok
	}
	row := func(id string) (status string, attempts int, payloadLen int, lastErr *string, finished bool) {
		t.Helper()
		if err := admin.QueryRow(ctx, `SELECT status, attempts, octet_length(payload), last_error, finished_at IS NOT NULL FROM mail_outbox WHERE id = $1`, id).
			Scan(&status, &attempts, &payloadLen, &lastErr, &finished); err != nil {
			t.Fatal(err)
		}
		return
	}

	t.Run("claim, retry, send", func(t *testing.T) {
		var pending int
		if err := worker.QueryRow(ctx, `SELECT mail_pending()`).Scan(&pending); err != nil || pending != 2 {
			t.Fatalf("mail_pending = %d, %v; want 2", pending, err)
		}
		got := claim(1)
		if len(got) != 1 || len(got[0].payload) != 2 {
			t.Fatalf("claim(1) = %+v", got)
		}
		first := got[0]
		if again := claim(10); len(again) != 1 || again[0].id == first.id {
			t.Fatalf("second claim = %+v; the claimed row must not come back", again)
		}
		if finish(first.id, uuid.NewString(), "sent", "", "NULL") {
			t.Fatal("finish with a wrong token recorded an outcome")
		}
		if !finish(first.id, first.token, "pending", "connect", "now() + interval '1 hour'") {
			t.Fatal("finish pending returned false")
		}
		if st, att, plen, lastErr, fin := row(first.id); st != "pending" || att != 1 || plen != 2 || lastErr == nil || fin {
			t.Fatalf("after retry: %s attempts=%d payload=%d err=%v finished=%t", st, att, plen, lastErr, fin)
		}
		pgtest.MustExec(t, admin, `UPDATE mail_outbox SET next_attempt_at = now() WHERE id = '`+first.id+`'`)
		re := claim(10)
		if len(re) != 1 || re[0].id != first.id || re[0].attempts != 1 {
			t.Fatalf("retry claim = %+v", re)
		}
		if !finish(re[0].id, re[0].token, "sent", "", "NULL") {
			t.Fatal("finish sent returned false")
		}
		if finish(re[0].id, re[0].token, "sent", "", "NULL") {
			t.Fatal("finishing twice recorded a second outcome")
		}
		if st, att, plen, lastErr, fin := row(first.id); st != "sent" || att != 2 || plen != 0 || lastErr != nil || !fin {
			t.Fatalf("after send: %s attempts=%d payload=%d err=%v finished=%t", st, att, plen, lastErr, fin)
		}
	})

	t.Run("a dead claimer's lease expires and counts as an attempt", func(t *testing.T) {
		other := b
		var held bool
		if err := admin.QueryRow(ctx, `SELECT status = 'in_progress' FROM mail_outbox WHERE id = $1`, other).Scan(&held); err != nil || !held {
			t.Fatalf("second row not in progress: %v %v", held, err)
		}
		pgtest.MustExec(t, admin, `UPDATE mail_outbox SET lease_until = now() - interval '1 second' WHERE id = '`+other+`'`)
		got := claim(10)
		if len(got) != 1 || got[0].id != other || got[0].attempts != 1 {
			t.Fatalf("claim = %+v, want the expired row again with attempts=1", got)
		}
		// Four more lease expiries end it as failed with the payload emptied.
		for i := 0; i < 4; i++ {
			pgtest.MustExec(t, admin, `UPDATE mail_outbox SET lease_until = now() - interval '1 second' WHERE id = '`+other+`' AND status = 'in_progress'`)
			claim(10)
		}
		if st, att, plen, lastErr, fin := row(other); st != "failed" || att != 5 || plen != 0 || lastErr == nil || *lastErr != "lease_expired" || !fin {
			t.Fatalf("poison message: %s attempts=%d payload=%d err=%v finished=%t", st, att, plen, lastErr, fin)
		}
	})

	t.Run("an expired message is failed, not delivered", func(t *testing.T) {
		id, err := insert(admin, "now() + interval '1 hour'")
		if err != nil {
			t.Fatal(err)
		}
		pgtest.MustExec(t, admin, `UPDATE mail_outbox SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 hour' WHERE id = '`+id+`'`)
		if got := claim(10); len(got) != 0 {
			t.Fatalf("claimed an expired message: %+v", got)
		}
		if st, _, plen, lastErr, fin := row(id); st != "failed" || plen != 0 || lastErr == nil || *lastErr != "expired" || !fin {
			t.Fatalf("expired: %s payload=%d err=%v finished=%t", st, plen, lastErr, fin)
		}
	})

	t.Run("finish validates its arguments", func(t *testing.T) {
		if _, err := worker.Exec(ctx, `SELECT finish_mail(gen_random_uuid(), gen_random_uuid(), 'bogus', NULL, NULL)`); err == nil || !strings.Contains(err.Error(), "invalid status") {
			t.Errorf("bogus status: %v", err)
		}
		if _, err := worker.Exec(ctx, `SELECT finish_mail(gen_random_uuid(), gen_random_uuid(), 'pending', 'x', NULL)`); err == nil || !strings.Contains(err.Error(), "retry_at is required") {
			t.Errorf("pending without retry_at: %v", err)
		}
	})

	t.Run("purge deletes only old finished rows", func(t *testing.T) {
		pgtest.MustExec(t, admin, `UPDATE mail_outbox SET finished_at = now() - interval '40 days' WHERE status = 'sent'`)
		fresh, _ := insert(admin, "now() + interval '1 hour'")
		var n int
		if err := worker.QueryRow(ctx, `SELECT purge_mail_outbox('30 days')`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("purge = %d, %v; want 1", n, err)
		}
		var left int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM mail_outbox WHERE id = $1 OR status = 'failed'`, fresh).Scan(&left); err != nil || left != 3 {
			t.Fatalf("rows left = %d, %v; want the fresh row and two failed", left, err)
		}
	})
}
