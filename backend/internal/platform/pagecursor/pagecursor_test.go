package pagecursor

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRoundTripAndTamper(t *testing.T) {
	p := Position{At: time.Date(2026, 11, 14, 9, 30, 1, 123456000, time.UTC), ID: uuid.New()}
	got, err := Decode(Encode(p))
	if err != nil || !got.At.Equal(p.At) || got.ID != p.ID {
		t.Fatalf("round trip: %v %v", got, err)
	}
	for _, bad := range []string{"", "!!!", "YWJj", Encode(p) + "=", Encode(p) + "A", Encode(p)[1:]} {
		if _, err := Decode(bad); err != ErrInvalid {
			t.Errorf("Decode(%q) = %v, want ErrInvalid", bad, err)
		}
	}
}

func TestTrim(t *testing.T) {
	key := func(i int) Position { return Position{At: time.UnixMicro(int64(i)), ID: uuid.Nil} }
	rows, next := Trim(Page{Limit: 2}, []int{1, 2, 3}, key)
	if len(rows) != 2 || next == "" {
		t.Fatalf("full page: %v %q", rows, next)
	}
	if pos, _ := Decode(next); pos.At.UnixMicro() != 2 {
		t.Fatalf("cursor points at %v, want the last kept row", pos)
	}
	if rows, next = Trim(Page{Limit: 2}, []int{1, 2}, key); len(rows) != 2 || next != "" {
		t.Fatalf("last page: %v %q", rows, next)
	}
	if rows, next = Trim(Page{Limit: All}, []int{1, 2, 3}, key); len(rows) != 3 || next != "" {
		t.Fatalf("all: %v %q", rows, next)
	}
	if rows, next = Trim(Page{}, []int{}, key); len(rows) != 0 || next != "" {
		t.Fatalf("empty: %v %q", rows, next)
	}
}
