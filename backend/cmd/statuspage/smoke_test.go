package main

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/testsupport/cmdtest"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

func TestRunServesAndStops(t *testing.T) {
	env := pgtest.New(t, "cmd_statuspage_smoke")
	e := cmdtest.Env{
		"PRORETA_DATABASE_URL":          env.StatuspageURL,
		"PRORETA_PUBLIC_URL":            "http://127.0.0.1",
		"PRORETA_COOKIE_SECURE":         "false",
		"PRORETA_STATUSPAGE_COOKIE_KEY": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("c", 32))),
		"PRORETA_HTTP_ADDR":             cmdtest.FreeAddr(t),
		"PRORETA_ADMIN_ADDR":            cmdtest.FreeAddr(t),
	}
	cmdtest.Serves(t, run, e, e["PRORETA_ADMIN_ADDR"], "statuspage")
}

// The configuration is rejected before anything is opened, so no database is needed.
func TestRunFailsFast(t *testing.T) {
	err := cmdtest.Fails(t, run, cmdtest.Env{})
	for _, name := range []string{"PRORETA_DATABASE_URL", "PRORETA_STATUSPAGE_COOKIE_KEY"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
}
