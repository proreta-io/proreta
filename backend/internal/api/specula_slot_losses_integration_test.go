package api_test

import (
	"net/http"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

func TestIntegrationSpeculaSlotLossesAPI(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apispeculaloss"))
	c := newServer(t, env, auth.Options{})
	cookie, csrf := c.login()
	admin := env.AdminConn(t)

	r := c.do(http.MethodPost, "/api/v1/specula", `{"name":"Prod"}`, withCSRF(cookie, csrf))
	if r.code != http.StatusCreated {
		t.Fatalf("create: %d %s", r.code, r.raw)
	}
	id := r.body["specula"].(map[string]any)["id"].(string)
	path := "/api/v1/specula/" + id + "/slot-losses"

	// Nothing reported, and the Specula does not announce schedule/2.
	r = c.do(http.MethodGet, path, "", cookie)
	if r.code != http.StatusOK || r.body["reporting"] != false || r.body["range"] != "24h" || len(r.body["totals"].([]any)) != 0 || len(r.body["buckets"].([]any)) != 0 {
		t.Fatalf("empty: %d %s", r.code, r.raw)
	}

	pgtest.MustExec(t, admin,
		`UPDATE specula_agents SET capabilities = '{tcp,schedule/1,schedule/2}' WHERE id = '`+id+`'`,
		`INSERT INTO specula_slot_losses (tenant_id, specula_id, hour, kind, reason, count)
		 SELECT tenant_id, id, date_trunc('hour', now() - h, 'UTC'), k, r, n FROM specula_agents,
		 (VALUES (interval '30 minutes', 'missed', 'busy', 5), (interval '30 minutes', 'dropped', 'tooOld', 2),
		         (interval '3 days', 'missed', 'busy', 7), (interval '3 days', 'missed', 'late', 1), (interval '20 days', 'dropped', 'rejected', 4)) AS v(h, k, r, n)
		 WHERE id = '`+id+`'`)

	total := func(body map[string]any, kind, reason string) float64 {
		for _, x := range body["totals"].([]any) {
			m := x.(map[string]any)
			if m["kind"] == kind && m["reason"] == reason {
				return m["count"].(float64)
			}
		}
		return 0
	}
	for _, c2 := range []struct {
		rng             string
		busy, late, rej float64
		buckets         int
	}{{"", 5, 0, 0, 1}, {"24h", 5, 0, 0, 1}, {"7d", 12, 1, 0, 2}, {"30d", 12, 1, 4, 3}} {
		q := path
		if c2.rng != "" {
			q += "?range=" + c2.rng
		}
		r = c.do(http.MethodGet, q, "", cookie)
		if r.code != http.StatusOK || r.body["reporting"] != true ||
			total(r.body, "missed", "busy") != c2.busy || total(r.body, "missed", "late") != c2.late || total(r.body, "dropped", "rejected") != c2.rej ||
			len(r.body["buckets"].([]any)) != c2.buckets {
			t.Errorf("range %q: %d %s", c2.rng, r.code, r.raw)
		}
	}
	// Missed come before dropped, in the documented order of the reasons.
	r = c.do(http.MethodGet, path+"?range=30d", "", cookie)
	first := r.body["totals"].([]any)[0].(map[string]any)
	if first["kind"] != "missed" || first["reason"] != "busy" {
		t.Errorf("order: %s", r.raw)
	}

	for q, want := range map[string]int{"?range=1y": http.StatusBadRequest} {
		if r := c.do(http.MethodGet, path+q, "", cookie); r.code != want {
			t.Errorf("%s: %d, want %d", q, r.code, want)
		}
	}
	if r := c.do(http.MethodGet, "/api/v1/specula/01920000-0000-7000-8000-0000000000ff/slot-losses", "", cookie); r.code != http.StatusNotFound {
		t.Errorf("unknown Specula: %d", r.code)
	}
}
