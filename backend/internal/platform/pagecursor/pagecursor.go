// Package pagecursor is the keyset position of the cursor-paged lists: the (timestamp, id) of the
// last row of a page. Lists are ordered by that pair (the id breaks ties), so a page never skips
// or repeats a row while rows are added or removed between requests.
package pagecursor

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// DefaultLimit and MaxLimit bound a page; they are the default and maximum of the `limit` parameter.
	DefaultLimit = 100
	MaxLimit     = 200
	// All is the Limit of a caller that needs the whole list (the admin lists, not yet paged).
	All = -1
)

// ErrInvalid means a cursor this API did not issue (the handler answers 400).
var ErrInvalid = errors.New("invalid cursor")

// Position is the sort key of the last row of a page.
type Position struct {
	// Rank is the leading sort key of a list ordered by a class first (monitors: down, active,
	// paused); 0 for the lists that have none.
	Rank int
	At   time.Time
	ID   uuid.UUID
}

// Page is a request: Limit rows (0 means DefaultLimit, All means no limit) after After (nil: from the start).
type Page struct {
	Limit int
	After *Position
}

// Size is the number of rows to read: one more than the page, so the caller knows whether another follows.
// Zero means no limit.
func (p Page) Size() int {
	switch {
	case p.Limit < 0:
		return 0
	case p.Limit == 0:
		return DefaultLimit + 1
	}
	return min(p.Limit, MaxLimit) + 1
}

// Trim cuts rows (read with Size) to the page and returns the cursor of the next one, "" on the last page.
func Trim[T any](p Page, rows []T, key func(T) Position) ([]T, string) {
	if p.Size() == 0 || len(rows) < p.Size() {
		return rows, ""
	}
	rows = rows[:p.Size()-1]
	return rows, Encode(key(rows[len(rows)-1]))
}

// Encode is the opaque cursor of a position. Microseconds, because that is what timestamptz keeps.
// The rank is written only when it is not 0, so cursors of unranked lists keep their form.
func Encode(p Position) string {
	raw := strconv.FormatInt(p.At.UnixMicro(), 10) + "." + p.ID.String()
	if p.Rank != 0 {
		raw = strconv.Itoa(p.Rank) + "." + raw
	}
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// Decode parses a cursor; anything Encode would not produce is ErrInvalid.
func Decode(s string) (Position, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Position{}, ErrInvalid
	}
	parts := strings.Split(string(raw), ".")
	rank := 0
	if len(parts) == 3 {
		r, err := strconv.Atoi(parts[0])
		if err != nil {
			return Position{}, ErrInvalid
		}
		rank, parts = r, parts[1:]
	}
	if len(parts) != 2 {
		return Position{}, ErrInvalid
	}
	us, id := parts[0], parts[1]
	n, err := strconv.ParseInt(us, 10, 64)
	if err != nil {
		return Position{}, ErrInvalid
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return Position{}, ErrInvalid
	}
	p := Position{Rank: rank, At: time.UnixMicro(n).UTC(), ID: u}
	if Encode(p) != s {
		return Position{}, ErrInvalid
	}
	return p, nil
}
