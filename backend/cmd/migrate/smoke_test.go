package main

import (
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/testsupport/cmdtest"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

func TestRunAppliesAndRepeats(t *testing.T) {
	env := pgtest.New(t, "cmd_migrate_smoke")
	e := cmdtest.Env{"PRORETA_MIGRATE_DATABASE_URL": env.OwnerURL}
	// The test database is already migrated, so run only has to find nothing left to do, twice.
	for range 2 {
		if err := run(t.Context(), e.Lookup); err != nil {
			t.Fatal(err)
		}
	}
}

// The configuration is rejected before anything is opened, so no database is needed.
func TestRunFailsFast(t *testing.T) {
	err := cmdtest.Fails(t, run, cmdtest.Env{})
	for _, name := range []string{"PRORETA_MIGRATE_DATABASE_URL"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
}
