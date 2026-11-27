package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

func TestIntegrationTCPMonitorAPI(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apitcp"))
	c := newServer(t, env, auth.Options{})
	cookie, csrf := c.login()
	unsafe := withCSRF(cookie, csrf)

	r := c.do(http.MethodPost, "/api/v1/monitors", `{"type":"tcp","name":"Database","tcp":{"host":"DB.Example.com","port":5432}}`, unsafe)
	if r.code != http.StatusCreated {
		t.Fatalf("create: %d %s", r.code, r.raw)
	}
	id, _ := r.body["id"].(string)
	tcp, _ := r.body["tcp"].(map[string]any)
	for k, v := range map[string]any{
		"host": "db.example.com", "port": 5432.0, "intervalSeconds": 60.0, "timeoutSeconds": 10.0, "failureThreshold": 2.0, "lastResponseMs": nil,
	} {
		if tcp[k] != v {
			t.Errorf("tcp.%s = %v, want %v", k, tcp[k], v)
		}
	}
	if _, ok := r.body["http"]; ok || r.body["type"] != "tcp" || r.body["status"] != "unknown" {
		t.Errorf("a tcp monitor has exactly the tcp sub-object: %s", r.raw)
	}
	if loc, _ := r.body["location"].(map[string]any); loc["type"] != "cloud" {
		t.Errorf("location = %v", r.body["location"])
	}

	// The list carries the same sub-object, and the monitor counts toward the monitor quota.
	l := c.do(http.MethodGet, "/api/v1/monitors", "", cookie)
	if !strings.Contains(l.raw, `"tcp":{"failureThreshold":2,"host":"db.example.com"`) || l.code != http.StatusOK {
		t.Fatalf("list: %d %s", l.code, l.raw)
	}
	if n := countRows(t, env, `SELECT count(*) FROM monitors WHERE type = 'tcp' AND config = '{"host":"db.example.com","port":5432}'`); n != 1 {
		t.Fatalf("stored config rows = %d", n)
	}

	t.Run("validation", func(t *testing.T) {
		for body, field := range map[string]string{
			`{"type":"tcp","name":"x"}`:                                                                                 "tcp",
			`{"type":"tcp","name":"x","tcp":{"host":"10.0.0.5","port":22}}`:                                             "tcp.host",
			`{"type":"tcp","name":"x","tcp":{"host":"localhost","port":22}}`:                                            "tcp.host",
			`{"type":"tcp","name":"x","tcp":{"host":"https://db.example.com","port":22}}`:                               "tcp.host",
			`{"type":"tcp","name":"x","tcp":{"host":"db.example.com","port":22},"http":{"url":"https://example.com"}}`:  "http",
			`{"type":"http","name":"x","http":{"url":"https://example.com"},"tcp":{"host":"db.example.com","port":22}}`: "tcp",
			`{"type":"heartbeat","name":"x","heartbeat":{},"tcp":{"host":"db.example.com","port":22}}`:                  "tcp",
		} {
			if v := c.do(http.MethodPost, "/api/v1/monitors", body, unsafe); v.code != http.StatusUnprocessableEntity || !strings.Contains(v.raw, `"field":"`+field+`"`) {
				t.Errorf("%s: %d %s", body, v.code, v.raw)
			}
		}
		// Out of range ports and thresholds are refused by the contract before the service.
		for _, body := range []string{
			`{"type":"tcp","name":"x","tcp":{}}`,
			`{"type":"tcp","name":"x","tcp":{"host":"db.example.com","port":0}}`,
			`{"type":"tcp","name":"x","tcp":{"host":"db.example.com","port":65536}}`,
			`{"type":"tcp","name":"x","tcp":{"host":"db.example.com","port":22,"intervalSeconds":30}}`,
		} {
			if v := c.do(http.MethodPost, "/api/v1/monitors", body, unsafe); v.code != http.StatusBadRequest && v.code != http.StatusUnprocessableEntity {
				t.Errorf("%s: %d %s", body, v.code, v.raw)
			}
		}
	})

	t.Run("update", func(t *testing.T) {
		p := c.do(http.MethodPatch, "/api/v1/monitors/"+id, `{"tcp":{"port":6432,"failureThreshold":3}}`, unsafe)
		tcp, _ := p.body["tcp"].(map[string]any)
		if p.code != http.StatusOK || tcp["port"] != 6432.0 || tcp["host"] != "db.example.com" || tcp["failureThreshold"] != 3.0 {
			t.Fatalf("patch: %d %s", p.code, p.raw)
		}
		// The sub-object of another type is refused, as is a private host from the cloud.
		for body, field := range map[string]string{
			`{"http":{"url":"https://example.com"}}`: "http",
			`{"tcp":{"host":"192.168.1.1"}}`:         "tcp.host",
		} {
			if v := c.do(http.MethodPatch, "/api/v1/monitors/"+id, body, unsafe); v.code != http.StatusUnprocessableEntity || !strings.Contains(v.raw, `"field":"`+field+`"`) {
				t.Errorf("%s: %d %s", body, v.code, v.raw)
			}
		}
		// A new target restarts the monitor: it is due again and its status is unknown.
		if n := countRows(t, env, `SELECT count(*) FROM monitor_location_state WHERE monitor_id = '`+id+`' AND next_run_at <= now() + interval '1 minute'`); n != 1 {
			t.Fatalf("schedule rows = %d", n)
		}
	})
}

func TestIntegrationTCPSendExpectAPI(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apitcpsend"))
	c := newServerKeys(t, env, auth.Options{}, requestKeyring(t))
	cookie, csrf := c.login()
	unsafe := withCSRF(cookie, csrf)

	create := `{"type":"tcp","name":"Svc","tcp":{"host":"svc.example.com","port":3000,` +
		`"send":{"encoding":"hex","value":"0000001a7b7d"},"expect":{"mode":"contains","value":"\"status\":\"ok\""}}}`
	r := c.do(http.MethodPost, "/api/v1/monitors", create, unsafe)
	if r.code != http.StatusCreated {
		t.Fatalf("create: %d %s", r.code, r.raw)
	}
	if strings.Contains(r.raw, "0000001a7b7d") {
		t.Fatalf("the bytes to send are write-only: %s", r.raw)
	}
	id, _ := r.body["id"].(string)
	tcp, _ := r.body["tcp"].(map[string]any)
	if tcp["sendSet"] != true || tcp["readTimeoutSeconds"] != 5.0 || tcp["maxResponseBytes"] != 4096.0 {
		t.Errorf("tcp = %v", tcp)
	}
	if e, _ := tcp["expect"].(map[string]any); e["mode"] != "contains" || e["value"] != `"status":"ok"` {
		t.Errorf("expect = %v", tcp["expect"])
	}
	if l := c.do(http.MethodGet, "/api/v1/monitors", "", cookie); !strings.Contains(l.raw, `"sendSet":true`) {
		t.Errorf("the list carries sendSet: %s", l.raw)
	}

	for name, body := range map[string]string{
		"send without expect": `{"tcp":{"expect":{"mode":"contains","value":""}}}`,
		"bad regex":           `{"tcp":{"expect":{"mode":"regex","value":"("}}}`,
		"host moved":          `{"tcp":{"host":"other.example.com"}}`,
	} {
		if u := c.do(http.MethodPatch, "/api/v1/monitors/"+id, body, unsafe); u.code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, u.code, u.raw)
		}
	}
	// Moving the host with a new send, then removing both rules, leaves a plain check.
	u := c.do(http.MethodPatch, "/api/v1/monitors/"+id, `{"tcp":{"host":"other.example.com","send":{"encoding":"text","value":"ping"}}}`, unsafe)
	if u.code != http.StatusOK {
		t.Fatalf("move: %d %s", u.code, u.raw)
	}
	u = c.do(http.MethodPatch, "/api/v1/monitors/"+id, `{"tcp":{"send":{"encoding":"text","value":""},"expect":{"mode":"contains","value":""}}}`, unsafe)
	tcp, _ = u.body["tcp"].(map[string]any)
	if u.code != http.StatusOK || tcp["sendSet"] != false || tcp["expect"] != nil || tcp["readTimeoutSeconds"] != nil {
		t.Errorf("remove: %d %s", u.code, u.raw)
	}
}
