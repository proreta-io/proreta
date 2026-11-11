package dbbootstrap

import (
	"strings"
	"testing"
)

func TestDecide(t *testing.T) {
	all := State{SecretComplete: true, OwnerConnects: true, SchemaPresent: true}
	tests := []struct {
		name  string
		force bool
		state State
		skip  bool
		want  string
	}{
		{"ready", false, all, true, "migrated"},
		{"secret missing", false, State{OwnerConnects: true, SchemaPresent: true}, false, "Secret"},
		{"connect fails", false, State{SecretComplete: true, SchemaPresent: true}, false, "cannot connect"},
		{"goose table missing", false, State{SecretComplete: true, OwnerConnects: true}, false, "migration table"},
		{"nothing", false, State{}, false, "Secret"},
		{"force overrides a ready database", true, all, false, "forced"},
		{"force without anything", true, State{}, false, "forced"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			skip, reason := Decide(tc.force, tc.state)
			if skip != tc.skip || !strings.Contains(reason, tc.want) {
				t.Fatalf("Decide = %v, %q; want skip=%v containing %q", skip, reason, tc.skip, tc.want)
			}
		})
	}
}
