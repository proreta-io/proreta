package dbbootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileSecrets(t *testing.T) {
	ctx := context.Background()
	f := FileSecrets{Path: filepath.Join(t.TempDir(), "db.secret")}

	if _, found, err := f.Get(ctx, "", ""); err != nil || found {
		t.Fatalf("missing file: found=%v err=%v", found, err)
	}
	if err := f.Create(ctx, "", "", map[string][]byte{KeyAPIPassword: []byte("a-b_c"), KeyAPIURL: []byte("postgres://u:p@h:1/d?sslmode=disable")}); err != nil {
		t.Fatal(err)
	}
	if err := f.Create(ctx, "", "", map[string][]byte{"x": []byte("y")}); !errors.Is(err, ErrSecretExists) {
		t.Fatalf("second create = %v, want ErrSecretExists", err)
	}
	if err := f.Update(ctx, "", "", map[string][]byte{KeyAPIPassword: []byte("new"), KeyWorkerPassword: []byte("w")}); err != nil {
		t.Fatal(err)
	}
	got, found, err := f.Get(ctx, "", "")
	if err != nil || !found {
		t.Fatalf("get: found=%v err=%v", found, err)
	}
	if string(got[KeyAPIPassword]) != "new" || string(got[KeyWorkerPassword]) != "w" || string(got[KeyAPIURL]) != "postgres://u:p@h:1/d?sslmode=disable" || len(got) != 3 {
		t.Fatalf("data = %v", got)
	}
	if st, err := os.Stat(f.Path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", st.Mode(), err)
	}
	bad := FileSecrets{Path: filepath.Join(t.TempDir(), "bad")}
	if err := bad.Create(ctx, "", "", map[string][]byte{"k": []byte("a\nb")}); err == nil {
		t.Fatal("newline accepted")
	}
	if _, err := os.Stat(bad.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial file left behind: %v", err)
	}
}
