package main

import (
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/testsupport/cmdtest"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

func jobsEnv(t *testing.T, dbURL string) cmdtest.Env {
	keys, id := cmdtest.Keys()
	return cmdtest.Env{
		"PRORETA_DATABASE_URL":             dbURL,
		"PRORETA_ENCRYPTION_KEYS":          keys,
		"PRORETA_ENCRYPTION_ACTIVE_KEY_ID": id,
		"PRORETA_PUBLIC_URL":               "http://127.0.0.1",
		"PRORETA_ADMIN_ADDR":               cmdtest.FreeAddr(t),
	}
}

func TestRunServesAndStops(t *testing.T) {
	env := pgtest.New(t, "cmd_jobs_smoke")
	e := jobsEnv(t, env.WorkerURL)
	cmdtest.Serves(t, run, e, e["PRORETA_ADMIN_ADDR"], "jobs")
}

// The configuration is rejected before anything is opened, so no database is needed.
func TestRunFailsFast(t *testing.T) {
	err := cmdtest.Fails(t, run, cmdtest.Env{})
	for _, name := range []string{"PRORETA_DATABASE_URL", "PRORETA_ENCRYPTION_KEYS", "PRORETA_PUBLIC_URL"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
}
