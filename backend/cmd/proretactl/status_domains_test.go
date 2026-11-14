package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

func sampleRows() []domainRow {
	v := time.Date(2026, 11, 20, 10, 30, 0, 0, time.UTC)
	return []domainRow{
		{Host: "status.acme.com", State: "active", Primary: true, Org: "acme", Page: "acme-status", VerifiedAt: &v},
		{Host: "status.beta.io", State: "verified", Disabled: true, Org: "beta", Page: "beta-status"},
	}
}

func render(t *testing.T, format string, rows []domainRow, served bool) string {
	t.Helper()
	var b bytes.Buffer
	if err := writeDomains(&b, format, rows, served); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestWriteDomainsJSON(t *testing.T) {
	want := `{
  "domains": [
    {
      "host": "status.acme.com",
      "state": "active",
      "primary": true,
      "disabled": false,
      "organization": "acme",
      "page": "acme-status",
      "verifiedAt": "2026-11-20T10:30:00Z"
    },
    {
      "host": "status.beta.io",
      "state": "verified",
      "primary": false,
      "disabled": true,
      "organization": "beta",
      "page": "beta-status",
      "verifiedAt": null
    }
  ]
}
`
	if got := render(t, "json", sampleRows(), true); got != want {
		t.Fatalf("json:\n%s\nwant:\n%s", got, want)
	}
	if got := render(t, "json", nil, true); got != "{\n  \"domains\": []\n}\n" {
		t.Fatalf("empty json = %q (an array, never null)", got)
	}
}

func TestWriteDomainsText(t *testing.T) {
	want := "HOST             STATE     DISABLED  PRIMARY  ORGANIZATION  STATUS PAGE\n" +
		"status.acme.com  active    no        yes      acme          acme-status\n" +
		"status.beta.io   verified  yes       no       beta          beta-status\n" +
		"2 served hosts\n"
	if got := render(t, "text", sampleRows(), true); got != want {
		t.Fatalf("text:\n%s\nwant:\n%s", got, want)
	}
	if got := render(t, "text", nil, false); !strings.HasSuffix(got, "0 domains\n") {
		t.Fatalf("empty text = %q", got)
	}
}

func TestWriteDomainsCaddy(t *testing.T) {
	got := render(t, "caddy", sampleRows(), true)
	for _, want := range []string{
		"2 host(s) served now",
		"status.acme.com, status.beta.io {\n\treverse_proxy {$PRORETA_STATUSPAGE_UPSTREAM}\n}\n",
		"#\t\task {$PRORETA_TLS_ASK_URL}",
		"#\t\ton_demand\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("caddy output lacks %q:\n%s", want, got)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "token=") && !strings.HasPrefix(line, "#") {
			t.Errorf("a token placeholder outside a comment: %q", line)
		}
	}
	empty := render(t, "caddy", nil, true)
	if strings.Contains(empty, "reverse_proxy {$PRORETA_STATUSPAGE_UPSTREAM}\n}") || !strings.Contains(empty, "0 host(s)") {
		t.Fatalf("no hosts must give no site block:\n%s", empty)
	}
}

func TestStatusDomainsFlags(t *testing.T) {
	env := func(string) (string, bool) { return "", false }
	for _, args := range [][]string{nil, {"show"}, {"list", "-format", "xml"}, {"list", "-format", "caddy", "-all"}} {
		if err := statusDomains(args, env, &bytes.Buffer{}); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
	// Valid flags need the database URL.
	if err := statusDomains([]string{"list"}, env, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "PRORETA_DATABASE_URL") {
		t.Errorf("without a URL: %v", err)
	}
}

// The command reads through the api's role: only served hosts by default, every unreleased
// domain with -all.
func TestIntegrationStatusDomainsList(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("proretactldomains"))
	admin := env.AdminConn(t)
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name) VALUES ('01920000-0000-7000-8000-0000000000a1', 'acme', 'Acme'), ('01920000-0000-7000-8000-0000000000b1', 'beta', 'Beta')`,
		`INSERT INTO status_pages (id, tenant_id, slug, title, published) VALUES
			('01920000-0000-7000-8000-0000000000a2', '01920000-0000-7000-8000-0000000000a1', 'acme-status', 'Acme', true),
			('01920000-0000-7000-8000-0000000000b2', '01920000-0000-7000-8000-0000000000b1', 'beta-status', 'Beta', true)`)
	pgtest.MustExec(t, admin,
		`INSERT INTO status_page_domains (id, tenant_id, page_id, host, state, token, disabled_at, released_at) VALUES
		 ('01920000-0000-7000-8000-00000000c010', '01920000-0000-7000-8000-0000000000a1', '01920000-0000-7000-8000-0000000000a2', 'status.acme.com', 'active', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', NULL, NULL),
		 ('01920000-0000-7000-8000-00000000c020', '01920000-0000-7000-8000-0000000000b1', '01920000-0000-7000-8000-0000000000b2', 'status.beta.io', 'verified', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', now(), NULL),
		 ('01920000-0000-7000-8000-00000000c030', '01920000-0000-7000-8000-0000000000b1', '01920000-0000-7000-8000-0000000000b2', 'wait.beta.io', 'pending', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', NULL, NULL),
		 ('01920000-0000-7000-8000-00000000c040', '01920000-0000-7000-8000-0000000000b1', '01920000-0000-7000-8000-0000000000b2', 'gone.beta.io', 'active', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', NULL, now())`)
	lookup := func(k string) (string, bool) {
		if k == "PRORETA_DATABASE_URL" {
			return env.APIURL, true
		}
		return "", false
	}
	var served, all bytes.Buffer
	if err := statusDomains([]string{"list", "-format", "caddy"}, lookup, &served); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(served.String(), "\nstatus.acme.com {\n") || strings.Contains(served.String(), "beta.io {") {
		t.Fatalf("served hosts only (disabled, pending and released are not):\n%s", served.String())
	}
	if err := statusDomains([]string{"list", "-all"}, lookup, &all); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"status.acme.com", "status.beta.io", "wait.beta.io", "3 domains"} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("-all lacks %q:\n%s", want, all.String())
		}
	}
	if strings.Contains(all.String(), "gone.beta.io") {
		t.Errorf("a released domain is listed:\n%s", all.String())
	}
}
