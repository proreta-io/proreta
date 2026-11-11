package jobs

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/mail"
	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
)

// MailSender sends one message; *mail.SMTPSender implements it. Failures should be
// *mail.SendError; anything else is retried as "other".
type MailSender interface {
	Send(ctx context.Context, m mail.Message) error
}

// MailSettings is where the connection comes from: the email settings the platform admin edits in
// the admin panel (*mail.Settings). They are read again for every message, so a change applies
// to the next one without a restart.
type MailSettings interface {
	Connection(ctx context.Context) (mail.Connection, mail.ConnectionVersion, bool, error)
	RecordTest(ctx context.Context, version mail.ConnectionVersion, sendErr error) (bool, error)
}

// settingsTTL is how long the loop trusts "no server configured" before asking again. While it is
// true nothing is claimed, so mail queued before the admin finishes the setup waits.
const settingsTTL = 5 * time.Second

// Defaults of MailConfig.
const (
	DefaultMailPoll        = 2 * time.Second
	DefaultMailBatch       = 10
	DefaultMailConcurrency = 4
	// DefaultMailInterval spaces message starts in one jobs process (4 per second); an SMTP
	// provider's own rate limit is usually stricter than a burst of registrations.
	DefaultMailInterval = 250 * time.Millisecond
	// MailRetention is how long finished rows (already emptied of address and token) are kept
	// for troubleshooting.
	MailRetention = 14 * 24 * time.Hour
)

// MailMaxAttempts is how many sends a message gets before it is marked failed. claim_mail
// counts a lease expiry as an attempt with the same number in SQL; the integration test fails
// if they drift apart.
const MailMaxAttempts = 5

// mailRetryDelays is the backoff after the n-th failed attempt (index n-1). Mail problems are
// mostly a server that is down or greylisting, which minutes fix, so the delays are longer
// than for Slack.
var mailRetryDelays = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute}

// MailConfig tunes the MailDeliverer; zero values take the defaults.
type MailConfig struct {
	PollInterval time.Duration
	Batch        int
	// Concurrency caps sends in flight in this process.
	Concurrency   int
	Interval      time.Duration
	DrainTimeout  time.Duration
	GaugeInterval time.Duration
	// PublicURL is PRORETA_PUBLIC_URL, the base of every link in a message.
	PublicURL string
}

// MailDeps are the MailDeliverer's collaborators.
type MailDeps struct {
	Pool     *db.Pool
	Keys     *crypto.Keyring
	Settings MailSettings
	// NewSender builds the sender for a connection; nil means mail.NewSMTPSender.
	NewSender func(mail.Connection) MailSender
	Metrics   *Metrics
	Log       *slog.Logger
	Clock     clock.Clock
	// Sleep waits d or until ctx ends; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// MailDeliverer delivers the mail outbox: the email side of the transactional outbox. It never
// logs an address, a body or a token; a failure is recorded as a kind and an SMTP code.
type MailDeliverer struct {
	cfg      MailConfig
	d        MailDeps
	mu       sync.Mutex
	next     time.Time
	slots    chan struct{}
	inflight sync.WaitGroup

	checkedAt  time.Time
	configured bool
}

type mailRow struct {
	ID       uuid.UUID
	Kind     string
	Payload  []byte
	Attempts int
	Token    uuid.UUID
}

// NewMailDeliverer returns a MailDeliverer.
func NewMailDeliverer(cfg MailConfig, d MailDeps) *MailDeliverer {
	cfg.PollInterval = orDefault(cfg.PollInterval, DefaultMailPoll)
	cfg.Batch = orDefault(cfg.Batch, DefaultMailBatch)
	cfg.Concurrency = orDefault(cfg.Concurrency, DefaultMailConcurrency)
	cfg.Interval = orDefault(cfg.Interval, DefaultMailInterval)
	cfg.DrainTimeout = orDefault(cfg.DrainTimeout, DefaultDrainTimeout)
	cfg.GaugeInterval = orDefault(cfg.GaugeInterval, DefaultGaugeInterval)
	if d.Clock == nil {
		d.Clock = clock.System{}
	}
	if d.Sleep == nil {
		d.Sleep = sleep
	}
	if d.NewSender == nil {
		d.NewSender = func(c mail.Connection) MailSender { return mail.NewSMTPSender(c) }
	}
	return &MailDeliverer{cfg: cfg, d: d, slots: make(chan struct{}, cfg.Concurrency)}
}

// Run claims and delivers every PollInterval until ctx ends, then waits up to DrainTimeout for
// sends in flight. A send still running after that is cancelled; its lease (2 minutes) expires
// and the message is delivered again (at-least-once).
func (r *MailDeliverer) Run(ctx context.Context) error {
	sendCtx, abandon := context.WithCancel(context.WithoutCancel(ctx))
	defer abandon()

	r.d.Log.Info("mail delivery loop started", "concurrency", r.cfg.Concurrency)
	tick := time.NewTicker(r.cfg.PollInterval)
	defer tick.Stop()
	gauge := time.NewTicker(r.cfg.GaugeInterval)
	defer gauge.Stop()
	r.refreshPending(ctx)
	for {
		r.tick(ctx, sendCtx)
		select {
		case <-ctx.Done():
		case <-gauge.C:
			r.refreshPending(ctx)
			continue
		case <-tick.C:
			continue
		}
		break
	}
	done := make(chan struct{})
	go func() { r.inflight.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(r.cfg.DrainTimeout):
		r.d.Log.Warn("abandoning mail in flight; its leases will expire")
		abandon()
		<-done
	}
	r.d.Log.Info("mail delivery loop stopped")
	return nil
}

// RunOnce claims once and waits for those messages to finish (tests and tools).
func (r *MailDeliverer) RunOnce(ctx context.Context) int {
	n := r.tick(ctx, ctx)
	r.inflight.Wait()
	return n
}

func (r *MailDeliverer) tick(ctx, sendCtx context.Context) int {
	free := cap(r.slots) - len(r.slots)
	if free <= 0 || ctx.Err() != nil || !r.serverConfigured(ctx) {
		return 0
	}
	claimed, err := r.claim(ctx, min(free, r.cfg.Batch))
	if err != nil {
		r.d.Metrics.mailClaimErrors.Inc()
		r.d.Log.ErrorContext(ctx, "claim_mail failed", "error", err)
		return 0
	}
	for _, row := range claimed {
		r.slots <- struct{}{}
		r.inflight.Add(1)
		go func() {
			defer func() { <-r.slots; r.inflight.Done() }()
			r.deliver(sendCtx, row)
		}()
	}
	return len(claimed)
}

// serverConfigured is false while no SMTP server is set up; the answer is kept for settingsTTL.
func (r *MailDeliverer) serverConfigured(ctx context.Context) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.d.Clock.Now()
	if !r.checkedAt.IsZero() && now.Sub(r.checkedAt) < settingsTTL {
		return r.configured
	}
	r.checkedAt = now
	_, _, found, err := r.d.Settings.Connection(ctx)
	if err != nil {
		r.configured = false
		if ctx.Err() == nil {
			r.d.Log.WarnContext(ctx, "reading the email settings failed", "error", err)
		}
		return false
	}
	r.configured = found
	return found
}

func (r *MailDeliverer) claim(ctx context.Context, n int) ([]mailRow, error) {
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	var out []mailRow
	err := db.WithTx(ctx, r.d.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, kind, payload, attempts, claim_token FROM claim_mail($1)`, n)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (mailRow, error) {
			var m mailRow
			var attempts int32
			err := row.Scan(&m.ID, &m.Kind, &m.Payload, &attempts, &m.Token)
			m.Attempts = int(attempts)
			return m, err
		})
		return err
	})
	return out, err
}

func (r *MailDeliverer) deliver(ctx context.Context, row mailRow) {
	log := r.d.Log.With("mail_id", row.ID, "kind", row.Kind)
	to, params, err := mail.Open(r.d.Keys, row.ID, row.Payload)
	if err != nil {
		r.finish(ctx, log, row, mailOutcome{status: statusFailed, errKind: "undecryptable"})
		return
	}
	msg, err := mail.Render(mail.Kind(row.Kind), r.cfg.PublicURL, to, params)
	if err != nil {
		r.finish(ctx, log, row, mailOutcome{status: statusFailed, errKind: "bad_payload"})
		return
	}
	if wait := r.reserve(r.d.Clock.Now()); wait > 0 {
		if err := r.d.Sleep(ctx, wait); err != nil {
			return // abandoned at shutdown; the lease expires
		}
	}
	conn, version, found, err := r.d.Settings.Connection(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		r.d.Log.WarnContext(ctx, "reading the email settings failed", "error", err)
		r.finish(ctx, log, row, decideMail(row.Attempts, &mail.SendError{Kind: "settings"}, r.d.Clock.Now()))
		return
	}
	if !found {
		r.finish(ctx, log, row, decideMail(row.Attempts, &mail.SendError{Kind: "not_configured"}, r.d.Clock.Now()))
		return
	}
	start := time.Now()
	sendErr := r.d.NewSender(conn).Send(ctx, msg)
	r.d.Metrics.mailDuration.Observe(time.Since(start).Seconds())
	if sendErr != nil && ctx.Err() != nil {
		return // abandoned at shutdown: not a real failure
	}
	if mail.Kind(row.Kind) == mail.KindTest {
		// A test is sent once, and its result belongs to the settings it used.
		if _, err := r.d.Settings.RecordTest(ctx, version, sendErr); err != nil {
			r.d.Log.ErrorContext(ctx, "recording the email test failed", "error", err)
		}
		o := mailOutcome{status: statusSent}
		if sendErr != nil {
			o = mailOutcome{status: statusFailed, errKind: mail.ErrorKind(sendErr)}
		}
		r.finish(ctx, log, row, o)
		return
	}
	r.finish(ctx, log, row, decideMail(row.Attempts, sendErr, r.d.Clock.Now()))
}

// reserve books the next send slot, Interval after the previous one, and returns the wait.
func (r *MailDeliverer) reserve(now time.Time) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	at := now
	if r.next.After(now) {
		at = r.next
	}
	r.next = at.Add(r.cfg.Interval)
	return at.Sub(now)
}

type mailOutcome struct {
	status  string
	errKind string
	retryAt time.Time
}

// decideMail maps a send result to what finish_mail records; attempts is the number of earlier
// attempts.
func decideMail(attempts int, sendErr error, now time.Time) mailOutcome {
	if sendErr == nil {
		return mailOutcome{status: statusSent}
	}
	kind := mail.ErrorKind(sendErr)
	if mail.IsPermanent(sendErr) {
		return mailOutcome{status: statusFailed, errKind: kind}
	}
	done := attempts + 1
	if done >= MailMaxAttempts {
		return mailOutcome{status: statusFailed, errKind: kind}
	}
	return mailOutcome{status: statusPending, errKind: kind, retryAt: now.Add(mailRetryDelays[min(done, len(mailRetryDelays))-1])}
}

func (r *MailDeliverer) finish(ctx context.Context, log *slog.Logger, row mailRow, o mailOutcome) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dbTimeout)
	defer cancel()
	var errText *string
	if o.errKind != "" {
		errText = &o.errKind
	}
	var retryAt *time.Time
	if o.status == statusPending {
		retryAt = &o.retryAt
	}
	var recorded bool
	err := db.WithTx(ctx, r.d.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT finish_mail($1, $2, $3, $4, $5)`, row.ID, row.Token, o.status, errText, retryAt).Scan(&recorded)
	})
	if err != nil {
		r.d.Log.ErrorContext(ctx, "finish_mail failed; the lease will expire", "mail_id", row.ID, "error", err)
		return
	}
	if !recorded {
		log.WarnContext(ctx, "mail claim was lost before finishing (lease expired or claimed again)")
		return
	}
	label := o.status
	if label == statusPending {
		label = "retry"
	}
	r.d.Metrics.mail.WithLabelValues(row.Kind, label).Inc()
	if o.status != statusSent {
		log.WarnContext(ctx, "mail not sent", "status", o.status, "error_kind", o.errKind, "attempt", row.Attempts+1)
	}
}

func (r *MailDeliverer) refreshPending(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	var n int64
	err := db.WithTx(ctx, r.d.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT mail_pending()`).Scan(&n)
	})
	if err != nil {
		if ctx.Err() == nil {
			r.d.Log.WarnContext(ctx, "mail_pending failed", "error", err)
		}
		return
	}
	r.d.Metrics.mailPending.Set(float64(n))
}
