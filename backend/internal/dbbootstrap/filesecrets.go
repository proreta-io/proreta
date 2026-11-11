package dbbootstrap

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
)

// FileSecrets is a SecretStore backed by one file of `key=value` lines (mode 0600), for a local
// development database without Kubernetes. The namespace and name arguments are ignored: the
// file is the Secret. Values must not contain newlines; the generated passwords and URLs never do.
type FileSecrets struct {
	Path string
}

// Get implements SecretStore.
func (f FileSecrets) Get(_ context.Context, _, _ string) (map[string][]byte, bool, error) {
	file, err := os.Open(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("dbbootstrap: read %s: %w", f.Path, err)
	}
	defer file.Close()
	data := map[string][]byte{}
	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok || k == "" {
			continue
		}
		data[k] = []byte(v)
	}
	if err := sc.Err(); err != nil {
		return nil, false, fmt.Errorf("dbbootstrap: read %s: %w", f.Path, err)
	}
	return data, true, nil
}

// Create implements SecretStore.
func (f FileSecrets) Create(_ context.Context, _, _ string, data map[string][]byte) error {
	file, err := os.OpenFile(f.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return ErrSecretExists
	}
	if err != nil {
		return fmt.Errorf("dbbootstrap: create %s: %w", f.Path, err)
	}
	if err := writeLines(file, data); err != nil {
		_ = file.Close()
		_ = os.Remove(f.Path)
		return fmt.Errorf("dbbootstrap: write %s: %w", f.Path, err)
	}
	return file.Close()
}

// Update implements SecretStore.
func (f FileSecrets) Update(ctx context.Context, ns, name string, data map[string][]byte) error {
	cur, found, err := f.Get(ctx, ns, name)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("dbbootstrap: %s does not exist", f.Path)
	}
	for k, v := range data {
		cur[k] = v
	}
	file, err := os.OpenFile(f.Path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("dbbootstrap: update %s: %w", f.Path, err)
	}
	if err := writeLines(file, cur); err != nil {
		_ = file.Close()
		return fmt.Errorf("dbbootstrap: write %s: %w", f.Path, err)
	}
	return file.Close()
}

func writeLines(file *os.File, data map[string][]byte) error {
	keys := make([]string, 0, len(data))
	for k, v := range data {
		if strings.ContainsAny(k, "=\n") || strings.Contains(string(v), "\n") {
			return errors.New("key or value contains a newline")
		}
		keys = append(keys, k)
	}
	slices.Sort(keys)
	w := bufio.NewWriter(file)
	for _, k := range keys {
		fmt.Fprintf(w, "%s=%s\n", k, data[k])
	}
	return w.Flush()
}
