package migrations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// The email settings are reachable only through the definer functions: the runtime roles have no
// table privilege, only the platform admin changes them, a changed connection forgets the test and
// turns the switches off, and the switches need a test of the current settings.
func TestIntegrationEmailSettings(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("emailsettings"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	api := env.Conn(t, env.APIURL)
	worker := env.Conn(t, env.WorkerURL)
	seedPlatform(t, admin)

	for name, conn := range map[string]*pgx.Conn{"api": api, "worker": worker} {
		for _, stmt := range []string{`SELECT 1 FROM email_settings`, `UPDATE email_settings SET host = 'x'`, `SELECT password_sealed FROM email_settings`} {
			if _, err := conn.Exec(ctx, stmt); err == nil || !strings.Contains(err.Error(), "permission denied") {
				t.Errorf("%s: %s: err = %v, want permission denied", name, stmt, err)
			}
		}
	}
	// A regular user is not a platform admin; the system admin is.
	if _, err := api.Exec(ctx, `SELECT * FROM admin_email_settings_get($1)`, userOwnerA); sqlState(err) != "PRA01" {
		t.Fatalf("get as a regular user: err = %v, want PRA01", err)
	}

	type features struct{ enabled, inv, ver, reset bool }
	readFeatures := func() features {
		var f features
		if err := api.QueryRow(ctx, `SELECT enabled, send_invitations, send_email_verification, allow_password_reset FROM email_settings_features()`).Scan(&f.enabled, &f.inv, &f.ver, &f.reset); err != nil {
			t.Fatal(err)
		}
		return f
	}
	if f := readFeatures(); f != (features{}) {
		t.Fatalf("fresh installation: features = %+v, want all off", f)
	}
	var n int
	if err := worker.QueryRow(ctx, `SELECT count(*) FROM email_settings_for_jobs()`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("for_jobs before configuration: n = %d, err = %v; want no row", n, err)
	}

	update := func(host, username string, password []byte) error {
		_, err := api.Exec(ctx, `SELECT * FROM admin_email_settings_update($1, $2, 587, 'starttls', 'Proreta <p@example.com>', $3, $4, 'k1', gen_random_uuid(), NULL)`,
			userPlatform, host, username, password)
		return err
	}
	if err := update("smtp.example.com", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := update("smtp.example.com", "login", nil); sqlState(err) != "PRA13" {
		t.Fatalf("username without any password: err = %v, want PRA13", err)
	}
	if err := update("smtp.example.com", "login", []byte("sealed-1")); err != nil {
		t.Fatal(err)
	}
	if err := update("other.example.com", "login", nil); sqlState(err) != "PRA09" {
		t.Fatalf("host change keeping the password: err = %v, want PRA09", err)
	}
	if err := update("smtp.example.com", "login", nil); err != nil {
		t.Fatalf("same connection keeping the password: %v", err)
	}

	var passwordSet bool
	var tested bool
	getRow := func() (string, string) {
		var state, key string
		if err := api.QueryRow(ctx, `SELECT password_set, tested, test_state FROM admin_email_settings_get($1)`, userPlatform).Scan(&passwordSet, &tested, &state); err != nil {
			t.Fatal(err)
		}
		_ = key
		return state, ""
	}
	if state, _ := getRow(); !passwordSet || tested || state != "none" {
		t.Fatalf("after configuring: passwordSet=%t tested=%t state=%s", passwordSet, tested, state)
	}

	// Switches need a test.
	setFeatures := func(inv, ver, reset bool) error {
		_, err := api.Exec(ctx, `SELECT admin_email_settings_set_features($1, $2, $3, $4, gen_random_uuid(), NULL)`, userPlatform, inv, ver, reset)
		return err
	}
	if err := setFeatures(true, false, false); sqlState(err) != "PRA10" {
		t.Fatalf("switch on before a test: err = %v, want PRA10", err)
	}
	var version string
	if err := api.QueryRow(ctx, `SELECT admin_email_test_request($1, 'example.com', gen_random_uuid(), NULL)::text`, userPlatform).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, err := api.Exec(ctx, `SELECT admin_email_test_request($1, 'example.com', gen_random_uuid(), NULL)`, userPlatform); sqlState(err) != "PRA12" {
		t.Fatalf("second test while pending: err = %v, want PRA12", err)
	}
	if state, _ := getRow(); state != "pending" {
		t.Fatalf("state = %s, want pending", state)
	}
	// A result for another connection is dropped.
	var recorded bool
	if err := worker.QueryRow(ctx, `SELECT email_settings_record_test(gen_random_uuid(), true, NULL, 0)`).Scan(&recorded); err != nil || recorded {
		t.Fatalf("stale result recorded = %t, err = %v", recorded, err)
	}
	if err := worker.QueryRow(ctx, `SELECT email_settings_record_test($1, false, 'auth', 535)`, version).Scan(&recorded); err != nil || !recorded {
		t.Fatalf("failed result recorded = %t, err = %v", recorded, err)
	}
	if state, _ := getRow(); state != "failed" || tested {
		t.Fatalf("after a failure: state=%s tested=%t", state, tested)
	}
	if err := setFeatures(true, false, false); sqlState(err) != "PRA10" {
		t.Fatalf("switch on after a failed test: err = %v, want PRA10", err)
	}
	if err := worker.QueryRow(ctx, `SELECT email_settings_record_test($1, true, NULL, 0)`, version).Scan(&recorded); err != nil || !recorded {
		t.Fatalf("result recorded = %t, err = %v", recorded, err)
	}
	if f := readFeatures(); !f.enabled || f.inv {
		t.Fatalf("tested: features = %+v, want enabled with switches off", f)
	}
	if err := setFeatures(true, true, true); err != nil {
		t.Fatal(err)
	}
	if f := readFeatures(); f != (features{true, true, true, true}) {
		t.Fatalf("features = %+v", f)
	}
	// A failure later keeps the earlier success: flows do not flap on an outage.
	if err := worker.QueryRow(ctx, `SELECT email_settings_record_test($1, false, 'connect', 0)`, version).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if f := readFeatures(); !f.enabled || !f.inv {
		t.Fatalf("after a later failure: features = %+v", f)
	}

	// The table refuses the switches without a test even if a function is bypassed.
	if _, err := admin.Exec(ctx, `UPDATE email_settings SET connection_version = gen_random_uuid()`); err == nil || !strings.Contains(err.Error(), "check constraint") {
		t.Fatalf("version change leaving switches on: err = %v, want a check violation", err)
	}

	// Any connection change forgets the test and turns everything off.
	var changed []string
	var off bool
	if err := api.QueryRow(ctx, `SELECT changed, switches_disabled FROM admin_email_settings_update($1, 'smtp.example.com', 587, 'tls', 'Proreta <noreply@example.com>', 'login', NULL, '', gen_random_uuid(), NULL)`, userPlatform).Scan(&changed, &off); err != nil {
		t.Fatal(err)
	}
	if strings.Join(changed, ",") != "tls,from" || !off {
		t.Fatalf("changed = %v, switchesDisabled = %t", changed, off)
	}
	if f := readFeatures(); f != (features{}) {
		t.Fatalf("after a change: features = %+v, want all off", f)
	}
	if err := worker.QueryRow(ctx, `SELECT email_settings_record_test($1, true, NULL, 0)`, version).Scan(&recorded); err != nil || recorded {
		t.Fatalf("result of the old connection recorded = %t, err = %v", recorded, err)
	}

	// The audit rows carry no secret and no address.
	var audits int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action IN ('admin.email_settings_updated', 'admin.email_test_sent')
		AND metadata::text !~* '(sealed|smtp[.]|@)'`).Scan(&audits); err != nil || audits != 5 {
		t.Fatalf("audit rows = %d, err = %v; want 5 (2 updates, test, switches, update)", audits, err)
	}

	if err := api.QueryRow(ctx, `SELECT admin_email_settings_clear($1, gen_random_uuid(), NULL)`, userPlatform).Scan(&recorded); err != nil || !recorded {
		t.Fatalf("clear = %t, err = %v", recorded, err)
	}
	if err := worker.QueryRow(ctx, `SELECT count(*) FROM email_settings_for_jobs()`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("for_jobs after clearing: n = %d, err = %v", n, err)
	}
	var sealed int
	if err := admin.QueryRow(ctx, `SELECT octet_length(password_sealed) FROM email_settings`).Scan(&sealed); err != nil || sealed != 0 {
		t.Fatalf("password after clearing: %d bytes, err = %v", sealed, err)
	}
}
