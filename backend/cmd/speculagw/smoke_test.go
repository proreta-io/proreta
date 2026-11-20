package main

import (
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/testsupport/cmdtest"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

func gatewayEnv(t *testing.T, dbURL string) cmdtest.Env {
	return cmdtest.Env{
		"PRORETA_DATABASE_URL": dbURL,
		"PRORETA_HTTP_ADDR":    cmdtest.FreeAddr(t),
		"PRORETA_ADMIN_ADDR":   cmdtest.FreeAddr(t),
	}
}

func TestRunServesAndStops(t *testing.T) {
	env := pgtest.New(t, "cmd_speculagw_smoke")
	e := gatewayEnv(t, env.APIURL)
	cmdtest.Serves(t, run, e, e["PRORETA_ADMIN_ADDR"], "speculagw")
}

// The configuration is rejected before anything is opened, so no database is needed.
func TestRunFailsFast(t *testing.T) {
	err := cmdtest.Fails(t, run, cmdtest.Env{})
	for _, name := range []string{"PRORETA_DATABASE_URL"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
}
