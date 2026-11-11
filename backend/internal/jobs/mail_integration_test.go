package jobs_test

import (
	"context"
	"crypto/tls"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/proreta-io/proreta/backend/internal/jobs"
	"github.com/proreta-io/proreta/backend/internal/mail"
	"github.com/proreta-io/proreta/backend/internal/platform/config"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
	"github.com/proreta-io/proreta/backend/internal/testsupport/smtptest"
)

const recipient = "alice@example.org"

type mailEnv struct {
	*env
	smtp   *smtptest.Server
	queue  *mail.Queue
	reg    *prometheus.Registry
	sleeps *[]time.Duration
	d      *jobs.MailDeliverer
}

func newMailEnv(t *testing.T, name string) *mailEnv {
	e := newEnv(t, name)
	srv := smtptest.New(t, smtptest.Options{Mode: smtptest.StartTLS, User: "mailer", Pass: "mailer-secret"})
	sender := mail.NewSMTPSender(config.SMTP{
		Host: srv.Host(), Port: srv.Port(), TLS: config.SMTPStartTLS, From: "Proreta <proreta@example.com>", FromAddress: "proreta@example.com",
		Username: "mailer", Password: "mailer-secret",
	})
	sender.TLSConfig = &tls.Config{RootCAs: srv.Roots, MinVersion: tls.VersionTLS12}
	sender.Timeout = 5 * time.Second
	me := &mailEnv{env: e, smtp: srv, queue: mail.NewQueue(e.keys, nil), reg: prometheus.NewRegistry(), sleeps: new([]time.Duration)}
	var mu sync.Mutex
	me.d = jobs.NewMailDeliverer(jobs.MailConfig{PublicURL: "https://proreta.example.com", PollInterval: 20 * time.Millisecond}, jobs.MailDeps{
		Pool: e.Pool(t, e.WorkerURL, 4), Keys: e.keys, Sender: sender, Metrics: jobs.NewMetrics(me.reg),
		Log: slog.New(slog.NewJSONHandler(e, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Sleep: func(_ context.Context, d time.Duration) error {
			mu.Lock()
			defer mu.Unlock()
			*me.sleeps = append(*me.sleeps, d)
			return nil
		},
	})
	return me
}

// enqueue queues a test message as the api role, in a transaction like a state change.
func (me *mailEnv) enqueue(to string) {
	me.t.Helper()
	api := me.Conn(me.t, me.APIURL)
	tx, err := api.Begin(me.ctx)
	if err != nil {
		me.t.Fatal(err)
	}
	if err := me.queue.Enqueue(me.ctx, tx, mail.Request{Kind: mail.KindTest, To: to, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		me.t.Fatal(err)
	}
	if err := tx.Commit(me.ctx); err != nil {
		me.t.Fatal(err)
	}
}

type mailRow struct {
	status    string
	attempts  int
	lastError *string
	payload   int
	retryIn   time.Duration
}

func (me *mailEnv) rows() []mailRow {
	me.t.Helper()
	rows, err := me.admin.Query(me.ctx, `SELECT status, attempts, last_error, octet_length(payload), next_attempt_at - now() FROM mail_outbox ORDER BY created_at, id`)
	if err != nil {
		me.t.Fatal(err)
	}
	defer rows.Close()
	var out []mailRow
	for rows.Next() {
		var r mailRow
		if err := rows.Scan(&r.status, &r.attempts, &r.lastError, &r.payload, &r.retryIn); err != nil {
			me.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (me *mailEnv) logText() string {
	me.logMu.Lock()
	defer me.logMu.Unlock()
	return me.logs.String()
}

func TestIntegrationMailDelivery(t *testing.T) {
	me := newMailEnv(t, "jobsmail")

	t.Run("a queued message is delivered and leaves no address behind", func(t *testing.T) {
		me.enqueue(recipient)
		var stored []byte
		if err := me.admin.QueryRow(me.ctx, `SELECT payload FROM mail_outbox`).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(stored), "alice") || strings.Contains(string(stored), "example.org") {
			t.Fatal("the recipient is stored in the clear")
		}
		if n := me.d.RunOnce(me.ctx); n != 1 {
			t.Fatalf("claimed %d", n)
		}
		got := me.smtp.Messages()
		if len(got) != 1 || got[0].To[0] != recipient || !got[0].TLS || !got[0].Authed || !strings.Contains(got[0].Data, "outgoing email is working") {
			t.Fatalf("received %+v", got)
		}
		if rows := me.rows(); len(rows) != 1 || rows[0].status != "sent" || rows[0].attempts != 1 || rows[0].payload != 0 || rows[0].lastError != nil {
			t.Fatalf("row = %+v", rows)
		}
		if v := metric(t, me.reg, "proreta_jobs_mail_total", "test", "sent"); v != 1 {
			t.Fatalf("sent metric = %v", v)
		}
		if n := me.d.RunOnce(me.ctx); n != 0 || len(me.smtp.Messages()) != 1 {
			t.Fatalf("delivered twice: claimed %d, %d messages", n, len(me.smtp.Messages()))
		}
	})

	t.Run("a transient failure is retried later with backoff", func(t *testing.T) {
		pgtest.MustExec(t, me.admin, `DELETE FROM mail_outbox`)
		me.smtp.Reply("RCPT", "451 4.7.1 greylisted, try later")
		me.enqueue(recipient)
		me.d.RunOnce(me.ctx)
		rows := me.rows()
		if len(rows) != 1 || rows[0].status != "pending" || rows[0].attempts != 1 || rows[0].lastError == nil || *rows[0].lastError != "recipient_rejected 451" ||
			rows[0].payload == 0 || rows[0].retryIn < 20*time.Second || rows[0].retryIn > time.Minute {
			t.Fatalf("after the first failure: %+v", rows)
		}
		if n := me.d.RunOnce(me.ctx); n != 0 {
			t.Fatalf("claimed a message waiting for its retry (%d)", n)
		}
		pgtest.MustExec(t, me.admin, `UPDATE mail_outbox SET next_attempt_at = now()`)
		me.d.RunOnce(me.ctx)
		if rows := me.rows(); rows[0].status != "sent" || rows[0].attempts != 2 {
			t.Fatalf("after the retry: %+v", rows)
		}
		if v := metric(t, me.reg, "proreta_jobs_mail_total", "test", "retry"); v != 1 {
			t.Fatalf("retry metric = %v", v)
		}
	})

	t.Run("a permanent rejection fails at once", func(t *testing.T) {
		pgtest.MustExec(t, me.admin, `DELETE FROM mail_outbox`)
		me.smtp.Reply("RCPT", "550 5.1.1 no such user")
		me.enqueue("nobody@example.org")
		me.d.RunOnce(me.ctx)
		rows := me.rows()
		if len(rows) != 1 || rows[0].status != "failed" || rows[0].payload != 0 || *rows[0].lastError != "recipient_rejected 550" {
			t.Fatalf("row = %+v", rows)
		}
	})

	t.Run("a message gives up after the maximum attempts", func(t *testing.T) {
		pgtest.MustExec(t, me.admin, `DELETE FROM mail_outbox`)
		me.enqueue(recipient)
		for i := 0; i < jobs.MailMaxAttempts; i++ {
			me.smtp.Reply("MAIL", "421 4.3.2 shutting down")
			me.d.RunOnce(me.ctx)
			pgtest.MustExec(t, me.admin, `UPDATE mail_outbox SET next_attempt_at = now() WHERE status = 'pending'`)
		}
		rows := me.rows()
		if len(rows) != 1 || rows[0].status != "failed" || rows[0].attempts != jobs.MailMaxAttempts {
			t.Fatalf("row = %+v", rows)
		}
	})

	t.Run("a payload that cannot be opened fails without a send", func(t *testing.T) {
		pgtest.MustExec(t, me.admin, `DELETE FROM mail_outbox`)
		before := len(me.smtp.Messages())
		pgtest.MustExec(t, me.admin, `INSERT INTO mail_outbox (id, kind, payload, expires_at) VALUES (gen_random_uuid(), 'test', '\x0102030405', now() + interval '1 hour')`)
		me.d.RunOnce(me.ctx)
		rows := me.rows()
		if len(rows) != 1 || rows[0].status != "failed" || *rows[0].lastError != "undecryptable" || len(me.smtp.Messages()) != before {
			t.Fatalf("row = %+v", rows)
		}
	})

	t.Run("sends are spaced", func(t *testing.T) {
		pgtest.MustExec(t, me.admin, `DELETE FROM mail_outbox`)
		*me.sleeps = nil
		for range 3 {
			me.enqueue(recipient)
		}
		me.d.RunOnce(me.ctx)
		// Sleeps are recorded, not slept, so the bookings pile up: each send starts one interval
		// after the previous one.
		w := *me.sleeps
		if len(w) < 2 || len(w) > 3 {
			t.Fatalf("waits = %v, want one per message after the first", w)
		}
		slices.Sort(w)
		for i := 1; i < len(w); i++ {
			if d := w[i] - w[i-1]; d < jobs.DefaultMailInterval-10*time.Millisecond || d > jobs.DefaultMailInterval+10*time.Millisecond {
				t.Fatalf("waits = %v; consecutive sends must be %v apart", w, jobs.DefaultMailInterval)
			}
		}
	})

	t.Run("nothing sensitive is logged", func(t *testing.T) {
		logs := me.logText()
		for _, secret := range []string{"alice", "example.org", "mailer-secret", "outgoing email is working", "greylisted", "no such user"} {
			if strings.Contains(logs, secret) {
				t.Errorf("logs contain %q", secret)
			}
		}
		if !strings.Contains(logs, "mail not sent") {
			t.Error("failures are not logged at all")
		}
	})
}

// TestIntegrationMailEndToEnd runs the real delivery loop: a message queued by the api role in a
// transaction is picked up, sent over STARTTLS with a login, and finished; a message queued in a
// rolled-back transaction never exists.
func TestIntegrationMailEndToEnd(t *testing.T) {
	me := newMailEnv(t, "jobsmailloop")
	ctx, stop := context.WithCancel(me.ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = me.d.Run(ctx) }()
	defer func() { stop(); <-done }()

	api := me.Conn(t, me.APIURL)
	tx, err := api.Begin(me.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := me.queue.Enqueue(me.ctx, tx, mail.Request{Kind: mail.KindTest, To: "rolled-back@example.org", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(me.ctx); err != nil {
		t.Fatal(err)
	}
	me.enqueue(recipient)

	deadline := time.Now().Add(10 * time.Second)
	for len(me.smtp.Messages()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	msgs := me.smtp.Messages()
	if len(msgs) != 1 || msgs[0].To[0] != recipient {
		t.Fatalf("messages = %+v", msgs)
	}
	for time.Now().Before(deadline) {
		if rows := me.rows(); len(rows) == 1 && rows[0].status == "sent" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("row never finished: %+v", me.rows())
}

// A lease that expires (a jobs process that died mid-send) puts the message back, and the claim
// token fences the process that comes back late.
func TestIntegrationMailLeaseAndFence(t *testing.T) {
	me := newMailEnv(t, "jobsmaillease")
	me.enqueue(recipient)
	worker := me.Conn(t, me.WorkerURL)
	var id, token uuid.UUID
	if err := worker.QueryRow(me.ctx, `SELECT id, claim_token FROM claim_mail(1)`).Scan(&id, &token); err != nil {
		t.Fatal(err)
	}
	pgtest.MustExec(t, me.admin, `UPDATE mail_outbox SET lease_until = now() - interval '1 second'`)
	if n := me.d.RunOnce(me.ctx); n != 1 || len(me.smtp.Messages()) != 1 {
		t.Fatalf("claimed %d, delivered %d", n, len(me.smtp.Messages()))
	}
	var ok bool
	if err := worker.QueryRow(me.ctx, `SELECT finish_mail($1, $2, 'sent', NULL, NULL)`, id, token).Scan(&ok); err != nil || ok {
		t.Fatalf("a stale claim recorded an outcome: %v %v", ok, err)
	}
}

// claim_mail's inline attempt limit and jobs.MailMaxAttempts must agree.
func TestIntegrationMailPoisonFailsAfterMaxAttempts(t *testing.T) {
	me := newMailEnv(t, "jobsmailpoison")
	me.enqueue(recipient)
	worker := me.Conn(t, me.WorkerURL)
	for i := range jobs.MailMaxAttempts {
		var n int
		if err := worker.QueryRow(me.ctx, `SELECT count(*) FROM claim_mail(1)`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("claim %d: n=%d err=%v", i, n, err)
		}
		pgtest.MustExec(t, me.admin, `UPDATE mail_outbox SET lease_until = now() - interval '1 second' WHERE status = 'in_progress'`)
	}
	var n int
	if err := worker.QueryRow(me.ctx, `SELECT count(*) FROM claim_mail(1)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("poison message claimed again: %d %v", n, err)
	}
	if rows := me.rows(); rows[0].status != "failed" || rows[0].attempts != jobs.MailMaxAttempts || *rows[0].lastError != "lease_expired" {
		t.Fatalf("row = %+v", rows)
	}
}

func TestIntegrationMailPurgeTask(t *testing.T) {
	me := newMailEnv(t, "jobsmailpurge")
	pool := me.Pool(t, me.WorkerURL, 2)
	tasks := jobs.DefaultTasks(jobs.NewMetrics(prometheus.NewRegistry()), slog.New(slog.DiscardHandler), nil)
	var purge jobs.Task
	for _, tk := range tasks {
		if tk.Name == "purge_mail_outbox" {
			purge = tk
		}
	}
	if purge.Name == "" {
		t.Fatal("no purge_mail_outbox task")
	}
	me.enqueue(recipient)
	me.d.RunOnce(me.ctx)
	pgtest.MustExec(t, me.admin, `UPDATE mail_outbox SET finished_at = now() - interval '15 days'`)
	h := jobs.NewHousekeeper(pool, tasks, jobs.NewMetrics(prometheus.NewRegistry()), slog.New(slog.DiscardHandler), nil)
	if got := h.RunTask(me.ctx, purge); got != "ok" {
		t.Fatalf("task = %s", got)
	}
	if rows := me.rows(); len(rows) != 0 {
		t.Fatalf("rows left: %+v", rows)
	}
}
