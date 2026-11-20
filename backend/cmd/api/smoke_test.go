package main

import (
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/testsupport/cmdtest"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

func apiEnv(t *testing.T, dbURL string) cmdtest.Env {
	keys, id := cmdtest.Keys()
	return cmdtest.Env{
		"PRORETA_DATABASE_URL":             dbURL,
		"PRORETA_ENCRYPTION_KEYS":          keys,
		"PRORETA_ENCRYPTION_ACTIVE_KEY_ID": id,
		"PRORETA_ADMIN_LOGIN":              "admin",
		"PRORETA_ADMIN_PASSWORD_HASH":      cmdtest.AdminHash(t),
		"PRORETA_PUBLIC_URL":               "http://127.0.0.1",
		"PRORETA_COOKIE_SECURE":            "false",
		"PRORETA_HTTP_ADDR":                cmdtest.FreeAddr(t),
		"PRORETA_ADMIN_ADDR":               cmdtest.FreeAddr(t),
	}
}

func TestRunServesAndStops(t *testing.T) {
	env := pgtest.New(t, "cmd_api_smoke")
	e := apiEnv(t, env.APIURL)
	cmdtest.Serves(t, run, e, e["PRORETA_ADMIN_ADDR"], "api")
}

// The configuration is rejected before anything is opened, so no database is needed.
func TestRunFailsFast(t *testing.T) {
	err := cmdtest.Fails(t, run, cmdtest.Env{})
	for _, name := range []string{"PRORETA_DATABASE_URL", "PRORETA_ENCRYPTION_KEYS", "PRORETA_ADMIN_PASSWORD_HASH"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
}

// A role that may not read the tables the api needs must stop it at startup, not later.
func TestRunRejectsWrongDatabaseRole(t *testing.T) {
	env := pgtest.New(t, "cmd_api_role")
	e := apiEnv(t, env.StatuspageURL)
	_ = cmdtest.Fails(t, run, e)
}
