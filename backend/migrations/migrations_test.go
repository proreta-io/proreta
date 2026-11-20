package migrations

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

var namePattern = regexp.MustCompile(`^(\d{14})_[a-z0-9_]+\.sql$`)

// TestMigrationFiles checks the file-level rules that do not need a database.
func TestMigrationFiles(t *testing.T) {
	entries, err := fs.ReadDir(FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no migrations embedded")
	}
	seen := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		m := namePattern.FindStringSubmatch(name)
		if m == nil {
			t.Errorf("%s: name must be YYYYMMDDHHMMSS_snake_case.sql", name)
			continue
		}
		if _, err := time.Parse("20060102150405", m[1]); err != nil {
			t.Errorf("%s: prefix is not a valid UTC timestamp", name)
		}
		if other, dup := seen[m[1]]; dup {
			t.Errorf("%s: duplicate timestamp with %s", name, other)
		}
		seen[m[1]] = name

		body, err := fs.ReadFile(FS, name)
		if err != nil {
			t.Fatal(err)
		}
		checkBody(t, name, string(body))
	}
}

func checkBody(t *testing.T, name, body string) {
	t.Helper()
	if strings.Contains(body, "+goose Down") {
		t.Errorf("%s: migrations are forward-only, remove the +goose Down section", name)
	}
	noTx := strings.Contains(body, "-- +goose NO TRANSACTION")
	want := "SET LOCAL lock_timeout = '5s';"
	if noTx {
		want = "SET lock_timeout = '5s';"
	}
	sc := bufio.NewScanner(strings.NewReader(body))
	afterUp := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "-- +goose Up"):
			afterUp = true
			continue
		case strings.HasPrefix(line, "--"):
			continue
		}
		if !afterUp {
			t.Errorf("%s: statement before -- +goose Up", name)
			return
		}
		if line != want {
			t.Errorf("%s: first statement must be %q, got %q", name, want, line)
		}
		return
	}
	t.Errorf("%s: no statements", name)
}

// TestLatest checks that Latest is the version of the newest embedded file.
func TestLatest(t *testing.T) {
	entries, err := fs.ReadDir(FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	newest := entries[len(entries)-1].Name()
	got, err := Latest()
	if err != nil || !strings.HasPrefix(newest, fmt.Sprint(got)+"_") {
		t.Fatalf("Latest() = %d, %v; newest file %s", got, err, newest)
	}
}

// TestReleasedMigrationsUnchanged pins the migrations of published releases (released.sha256):
// an installed database never reruns them, so an edit would silently split fresh and installed
// schemas. A migration that is not pinned must also sort after every pinned one, or goose rejects
// it as out of order on installed databases.
func TestReleasedMigrationsUnchanged(t *testing.T) {
	raw, err := os.ReadFile("released.sha256")
	if err != nil {
		t.Fatal(err)
	}
	pinned := map[string]bool{}
	newest := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 {
			t.Fatalf("released.sha256: want '<sha256>  <file>', got %q", line)
		}
		body, err := fs.ReadFile(FS, f[1])
		if err != nil {
			t.Errorf("%s is pinned as released but is missing: a released migration is never renamed or removed", f[1])
			continue
		}
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != f[0] {
			t.Errorf("%s was edited after its release: put the change in a new migration", f[1])
		}
		pinned[f[1]] = true
		newest = max(newest, f[1])
	}
	entries, err := fs.ReadDir(FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !pinned[e.Name()] && e.Name() < newest {
			t.Errorf("%s is older than the released %s: use a newer timestamp", e.Name(), newest)
		}
	}
}
