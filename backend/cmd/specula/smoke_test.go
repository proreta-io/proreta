package main

import (
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/testsupport/cmdtest"
)

func speculaEnv(t *testing.T) cmdtest.Env {
	return cmdtest.Env{
		"PRORETA_URL":           "http://127.0.0.1:1", // nothing listens: the executor retries and keeps serving
		"PRORETA_SPECULA_TOKEN": "spc_smoke",
		"PRORETA_ADMIN_ADDR":    cmdtest.FreeAddr(t),
	}
}

func TestRunServesAndStops(t *testing.T) {
	e := speculaEnv(t)
	cmdtest.Serves(t, run, e, e["PRORETA_ADMIN_ADDR"], "specula")
}

// The configuration is rejected before anything is opened, so no database is needed.
func TestRunFailsFast(t *testing.T) {
	err := cmdtest.Fails(t, run, cmdtest.Env{})
	for _, name := range []string{"PRORETA_URL", "PRORETA_SPECULA_TOKEN"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
}

func TestRunRejectsForeignToken(t *testing.T) {
	err := cmdtest.Fails(t, run, speculaEnv(t).With("PRORETA_SPECULA_TOKEN", "not-a-specula-token"))
	if !strings.Contains(err.Error(), "spc_") {
		t.Errorf("error = %v", err)
	}
}
