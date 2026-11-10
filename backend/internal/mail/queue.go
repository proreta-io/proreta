package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/platform/uuidv7"
)

// ErrDisabled is returned by Enqueue when email is not configured. Callers check Enabled first
// and keep their no-email behaviour; this is the guard for a caller that forgot to.
var ErrDisabled = errors.New("mail: email is not configured")

// maxParamBytes bounds the stored parameters of one message.
const maxParamBytes = 2048

// Execer is the part of a pgx transaction the queue needs.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Request is one message to queue.
type Request struct {
	Kind Kind
	// To is the recipient's bare address.
	To string
	// Params are the values the template shows, plus "token" for kinds with a link.
	Params map[string]string
	// ExpiresAt is when the message stops being worth sending: the lifetime of the token it
	// carries. A message still queued then is dropped, never delivered late.
	ExpiresAt time.Time
}

// payload is what is encrypted into mail_outbox.payload. The address, the parameters and above
// all the token never sit in the database in the clear.
type payload struct {
	To     string            `json:"to"`
	Params map[string]string `json:"params,omitempty"`
}

// Queue writes messages to the mail_outbox in the caller's transaction, so a message exists
// exactly when the state change it announces is committed. The zero value and nil are disabled.
type Queue struct {
	keys     *crypto.Keyring
	enqueued *prometheus.CounterVec
}

// NewQueue returns a Queue; a nil keyring (email off) gives a disabled one. reg may be nil.
func NewQueue(keys *crypto.Keyring, reg prometheus.Registerer) *Queue {
	q := &Queue{keys: keys}
	q.enqueued = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proreta_mail_enqueued_total", Help: "Messages queued in the mail outbox, by kind.",
	}, []string{"kind"})
	for kind := range specs {
		q.enqueued.WithLabelValues(string(kind))
	}
	if reg != nil {
		reg.MustRegister(q.enqueued)
	}
	return q
}

// Enabled reports whether messages are queued. Every flow that can send mail asks first and
// otherwise behaves exactly as it did before email existed.
func (q *Queue) Enabled() bool { return q != nil && q.keys != nil }

// Enqueue inserts the message. The caller's transaction decides whether it survives.
func (q *Queue) Enqueue(ctx context.Context, tx Execer, r Request) error {
	if !q.Enabled() {
		return ErrDisabled
	}
	if !Known(r.Kind) {
		return fmt.Errorf("mail: unknown kind %q", r.Kind)
	}
	if _, err := parseBare(r.To); err != nil {
		return err
	}
	if !r.ExpiresAt.After(time.Now()) {
		return errors.New("mail: expiry is in the past")
	}
	if specs[r.Kind].linkPath != "" && r.Params["token"] == "" {
		return errors.New("mail: kind needs a token")
	}
	raw, err := json.Marshal(payload{To: r.To, Params: r.Params})
	if err != nil {
		return fmt.Errorf("mail: encode: %w", err)
	}
	if len(raw) > maxParamBytes {
		return errors.New("mail: message parameters too large")
	}
	id := uuidv7.New()
	sealed, err := q.keys.Encrypt(raw, payloadAAD(id))
	if err != nil {
		return fmt.Errorf("mail: encrypt: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mail_outbox (id, kind, payload, expires_at) VALUES ($1, $2, $3, $4)`,
		id, string(r.Kind), sealed, r.ExpiresAt); err != nil {
		return fmt.Errorf("mail: insert outbox: %w", err)
	}
	q.enqueued.WithLabelValues(string(r.Kind)).Inc()
	return nil
}

// Open decrypts a claimed outbox payload into the recipient and the parameters.
func Open(keys *crypto.Keyring, id uuid.UUID, sealed []byte) (to string, params map[string]string, err error) {
	raw, err := keys.Decrypt(sealed, payloadAAD(id))
	if err != nil {
		return "", nil, err
	}
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", nil, errors.New("mail: malformed payload")
	}
	return p.To, p.Params, nil
}

// payloadAAD binds the ciphertext to its row, so a payload copied to another row does not open.
func payloadAAD(id uuid.UUID) []byte { return []byte("mail_outbox:" + id.String()) }
