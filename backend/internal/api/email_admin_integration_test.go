package api_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proreta-io/proreta/backend/internal/admin"
	"github.com/proreta-io/proreta/backend/internal/api"
	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/incidents"
	"github.com/proreta-io/proreta/backend/internal/jobs"
	"github.com/proreta-io/proreta/backend/internal/mail"
	"github.com/proreta-io/proreta/backend/internal/monitors"
	"github.com/proreta-io/proreta/backend/internal/plans"
	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/platform/logging"
	"github.com/proreta-io/proreta/backend/internal/projects"
	"github.com/proreta-io/proreta/backend/internal/queries"
	"github.com/proreta-io/proreta/backend/internal/scheduler"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
	"github.com/proreta-io/proreta/backend/internal/testsupport/smtptest"
)

// TestIntegrationEmailSettingsEndToEnd configures email through the admin API only: nothing is
// on by default, the connection is saved, a test message goes through the real jobs delivery code
// to an in-process SMTP server, the switches turn on, an invitation and a password reset are
// emailed, and a connection change switches everything off again.
func TestIntegrationEmailSettingsEndToEnd(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apiemailadmin"))
	ctx := context.Background()
	logs := &lockedBuffer{}
	log := logging.New(logs, slog.LevelDebug)
	keys, err := crypto.NewKeyring(map[string][]byte{"k1": bytes.Repeat([]byte{7}, 32)}, "k1")
	if err != nil {
		t.Fatal(err)
	}
	pool := env.Pool(t, env.APIURL, 8)
	settings := mail.NewSettings(pool, keys, log)
	queue := mail.NewQueue(keys, settings, prometheus.NewRegistry())
	svc, err := auth.NewService(pool, auth.Options{SessionTTL: time.Hour, Logger: log, Mail: queue})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := crypto.HashPassword(adminPassword, testHashParams)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.BootstrapAdmin(ctx, adminLogin, hash); err != nil {
		t.Fatal(err)
	}
	pgtest.MustExec(t, env.AdminConn(t), `UPDATE users SET password_must_change = false WHERE email = '`+adminLogin+`'`)
	adminSvc, err := admin.NewService(pool, svc, admin.Options{Logger: log, Registerer: prometheus.NewRegistry(), Mail: queue, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Now())
	sched := scheduler.New()
	h, err := api.NewHandler(api.Config{Version: "test", Commit: "test", CookieSecure: true, TrustedProxyHops: 1, RegistrationEnabled: true,
		PasswordLoginEnabled: true, PublicURL: "https://proreta.example.com"},
		api.Deps{Auth: svc, Monitors: monitors.NewService(pool, sched, incidents.NewService(sched)), Queries: queries.New(pool, nil),
			Plans: plans.NewService(pool), Projects: projects.NewService(pool), Admin: adminSvc, Logger: log, Clock: clk, Pings: api.NewPingMetrics(prometheus.NewRegistry())})
	if err != nil {
		t.Fatal(err)
	}
	httpSrv := httptest.NewServer(h)
	t.Cleanup(httpSrv.Close)
	c := &client{t: t, srv: httpSrv}

	smtpSrv := smtptest.New(t, smtptest.Options{Mode: smtptest.StartTLS, User: "mailer", Pass: "mailer-secret"})
	workerPool := env.Pool(t, env.WorkerURL, 4)
	deliverer := jobs.NewMailDeliverer(jobs.MailConfig{PublicURL: "https://proreta.example.com"}, jobs.MailDeps{
		Pool: workerPool, Keys: keys, Settings: mail.NewSettings(workerPool, keys, log), Metrics: jobs.NewMetrics(prometheus.NewRegistry()), Log: log,
		NewSender: func(conn mail.Connection) jobs.MailSender {
			s := mail.NewSMTPSender(conn)
			s.TLSConfig = &tls.Config{RootCAs: smtpSrv.Roots, MinVersion: tls.VersionTLS12}
			return s
		},
	})
	deliver := func() {
		t.Helper()
		// The delivery loop re-checks "is a server configured" at most every few seconds.
		deadline := time.Now().Add(15 * time.Second)
		for deliverer.RunOnce(ctx) == 0 && time.Now().Before(deadline) {
			time.Sleep(200 * time.Millisecond)
		}
		// Jobs wrote the result; the api would see it within mail.FeaturesTTL.
		settings.Invalidate()
	}
	// The test-email limiter runs on this clock.
	wait := func() { clk.Advance(11 * time.Second) }
	adminCookie, adminCSRF := c.login()
	ah := withCSRF(adminCookie, adminCSRF)
	providers := func() map[string]any { return c.do(http.MethodGet, "/api/v1/auth/providers", "", nil).body }
	flags := func() string {
		p := providers()
		return fmt.Sprint(p["emailEnabled"], p["passwordResetEnabled"], p["emailVerificationEnabled"])
	}
	connection := func(host string, port int, password string) string {
		pw := ""
		if password != "" {
			pw = `,"password":"` + password + `"`
		}
		return fmt.Sprintf(`{"host":%q,"port":%d,"tls":"starttls","from":"Proreta <proreta@example.com>","username":"mailer"%s}`, host, port, pw)
	}

	// 1. Everything is off by default.
	if got := flags(); got != "false false false" {
		t.Fatalf("defaults: %s", got)
	}
	r := c.do(http.MethodGet, "/api/v1/admin/email", "", ah)
	if r.code != http.StatusOK || r.body["configured"] != false || r.body["sendInvitations"] != false || r.body["lastTest"] != nil {
		t.Fatalf("default settings: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPost, "/api/v1/admin/email/test", `{"to":"ops@example.org"}`, ah); r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:email-settings-conflict" {
		t.Fatalf("test without a server: %d %s", r.code, r.raw)
	}

	// 2. Saving a server never exposes the password and switches nothing on.
	r = c.do(http.MethodPut, "/api/v1/admin/email", connection(smtpSrv.Host(), smtpSrv.Port(), "mailer-secret"), ah)
	if r.code != http.StatusOK || r.body["passwordSet"] != true || r.body["tested"] != false || strings.Contains(r.raw, "mailer-secret") {
		t.Fatalf("save: %d %s", r.code, r.raw)
	}
	if strings.Contains(c.do(http.MethodGet, "/api/v1/admin/email", "", ah).raw, "mailer-secret") {
		t.Fatal("the password is readable")
	}
	var sealed []byte
	if err := env.AdminConn(t).QueryRow(ctx, `SELECT password_sealed FROM email_settings`).Scan(&sealed); err != nil || len(sealed) == 0 || bytes.Contains(sealed, []byte("mailer-secret")) {
		t.Fatalf("stored password: %d bytes, err %v", len(sealed), err)
	}
	features := `{"sendInvitations":true,"sendEmailVerification":true,"allowPasswordReset":true}`
	if r := c.do(http.MethodPut, "/api/v1/admin/email/features", features, ah); r.code != http.StatusConflict {
		t.Fatalf("switches before a test: %d %s", r.code, r.raw)
	}
	if got := flags(); got != "false false false" {
		t.Fatalf("after saving: %s", got)
	}
	// Invitation mail is not possible before a test either.
	userCookie, userCSRF, _ := c.register("ada@example.com")
	uh := withCSRF(userCookie, userCSRF)
	if r := c.do(http.MethodPost, "/api/v1/invitations", `{"role":"viewer","email":"bob@example.org"}`, uh); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("invitation email before a test: %d %s", r.code, r.raw)
	}

	// 3. A real test message through jobs.
	wait()
	r = c.do(http.MethodPost, "/api/v1/admin/email/test", `{"to":"ops@example.org"}`, ah)
	if r.code != http.StatusAccepted || r.body["lastTest"].(map[string]any)["state"] != "pending" {
		t.Fatalf("test: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPost, "/api/v1/admin/email/test", `{"to":"ops@example.org"}`, ah); r.code != http.StatusTooManyRequests {
		t.Fatalf("second test at once: %d %s", r.code, r.raw)
	}
	deliver()
	r = c.do(http.MethodGet, "/api/v1/admin/email", "", ah)
	last, _ := r.body["lastTest"].(map[string]any)
	if r.body["tested"] != true || last["state"] != "succeeded" || last["error"] != nil {
		t.Fatalf("after the test: %s", r.raw)
	}
	msgs := smtpSrv.Messages()
	if len(msgs) != 1 || msgs[0].To[0] != "ops@example.org" || !msgs[0].TLS || !msgs[0].Authed {
		t.Fatalf("received %+v", msgs)
	}
	if got := flags(); got != "true false false" {
		t.Fatalf("tested but switches off: %s", got)
	}

	// 4. Switches on; every feature works through the real SMTP path.
	if r := c.do(http.MethodPut, "/api/v1/admin/email/features", features, ah); r.code != http.StatusOK || r.body["sendInvitations"] != true {
		t.Fatalf("switches: %d %s", r.code, r.raw)
	}
	if got := flags(); got != "true true true" {
		t.Fatalf("switches on: %s", got)
	}
	me := c.do(http.MethodGet, "/api/v1/auth/me", "", uh)
	if me.body["emailInvitations"] != true {
		t.Fatalf("me: %s", me.raw)
	}
	inv := c.do(http.MethodPost, "/api/v1/invitations", `{"role":"viewer","email":"bob@example.org","sendEmail":true}`, uh)
	if inv.code != http.StatusCreated || inv.body["emailQueued"] != true {
		t.Fatalf("invitation: %d %s", inv.code, inv.raw)
	}
	inv2 := c.do(http.MethodPost, "/api/v1/invitations", `{"role":"viewer","email":"carol@example.org","sendEmail":false}`, uh)
	if inv2.code != http.StatusCreated || inv2.body["emailQueued"] != false {
		t.Fatalf("invitation without sending: %d %s", inv2.code, inv2.raw)
	}
	if r := c.do(http.MethodPost, "/api/v1/auth/forgot-password", `{"email":"ada@example.com"}`, map[string]string{"X-Forwarded-For": "198.51.100.77"}); r.code != http.StatusAccepted {
		t.Fatalf("forgot-password: %d %s", r.code, r.raw)
	}
	deliver()
	deadline := time.Now().Add(10 * time.Second)
	for deliverer.RunOnce(ctx) > 0 || (len(smtpSrv.Messages()) < 4 && time.Now().Before(deadline)) {
		time.Sleep(100 * time.Millisecond)
	}
	recipients := map[string]int{}
	for _, m := range smtpSrv.Messages() {
		recipients[m.To[0]]++
	}
	if recipients["bob@example.org"] != 1 || recipients["carol@example.org"] != 0 || recipients["ada@example.com"] != 1 {
		t.Fatalf("recipients = %v", recipients)
	}

	// 5. Changing the connection forgets the test and turns everything off.
	if r := c.do(http.MethodPut, "/api/v1/admin/email", connection("smtp.invalid", 587, ""), ah); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("new host without the password: %d %s", r.code, r.raw)
	}
	r = c.do(http.MethodPut, "/api/v1/admin/email", fmt.Sprintf(`{"host":%q,"port":%d,"tls":"starttls","from":"Proreta <noreply@example.com>","username":"mailer"}`, smtpSrv.Host(), smtpSrv.Port()), ah)
	if r.code != http.StatusOK || r.body["tested"] != false || r.body["sendInvitations"] != false || r.body["passwordSet"] != true {
		t.Fatalf("from changed: %d %s", r.code, r.raw)
	}
	if got := flags(); got != "false false false" {
		t.Fatalf("after a change: %s", got)
	}
	if r := c.do(http.MethodPost, "/api/v1/auth/forgot-password", `{"email":"ada@example.com"}`, map[string]string{"X-Forwarded-For": "198.51.100.78"}); r.code != http.StatusNotFound {
		t.Fatalf("forgot-password after a change: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPut, "/api/v1/admin/email/features", features, ah); r.code != http.StatusConflict {
		t.Fatalf("switches after a change: %d %s", r.code, r.raw)
	}

	// 6. Bad settings are refused; a failing test reports a category and no server text.
	for name, body := range map[string]string{
		"metadata address": connection("169.254.169.254", 587, "x"),
		"unspecified":      connection("0.0.0.0", 587, "x"),
		"url as host":      connection("https://smtp.example.com", 587, "x"),
		"plain remote":     `{"host":"smtp.example.com","port":25,"tls":"none","from":"a@example.com"}`,
		"plain login":      `{"host":"localhost","port":25,"tls":"none","from":"a@example.com","username":"u","password":"p"}`,
		"bad from":         `{"host":"smtp.example.com","port":587,"tls":"starttls","from":"nope"}`,
	} {
		if r := c.do(http.MethodPut, "/api/v1/admin/email", body, ah); r.code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, r.code, r.raw)
		}
	}
	smtpSrv.Reply("AUTH", "535 5.7.8 bad credentials for mailer")
	if r := c.do(http.MethodPut, "/api/v1/admin/email", connection(smtpSrv.Host(), smtpSrv.Port(), "wrong-password"), ah); r.code != http.StatusOK {
		t.Fatalf("save wrong password: %d %s", r.code, r.raw)
	}
	wait()
	if r := c.do(http.MethodPost, "/api/v1/admin/email/test", `{"to":"ops@example.org"}`, ah); r.code != http.StatusAccepted {
		t.Fatalf("test: %d %s", r.code, r.raw)
	}
	deliver()
	r = c.do(http.MethodGet, "/api/v1/admin/email", "", ah)
	last, _ = r.body["lastTest"].(map[string]any)
	if last["state"] != "failed" || last["error"] != "auth" || last["smtpCode"] != float64(535) || r.body["tested"] != false {
		t.Fatalf("failed test: %s", r.raw)
	}
	if strings.Contains(r.raw, "bad credentials") || strings.Contains(r.raw, "wrong-password") {
		t.Fatal("server text or the password in the settings")
	}

	// 7. Audit rows exist and hold no secret or address; logs hold none either.
	var audits int
	if err := env.AdminConn(t).QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action IN ('admin.email_settings_updated', 'admin.email_test_sent')
		AND metadata::text !~* '(mailer-secret|wrong-password|@)'`).Scan(&audits); err != nil || audits < 6 {
		t.Fatalf("audit rows without secrets = %d, err %v", audits, err)
	}
	for _, secret := range []string{"mailer-secret", "wrong-password", "ops@example.org", "bob@example.org"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("logs contain %q", secret)
		}
	}

	// 8. Removing the server erases it.
	if r := c.do(http.MethodDelete, "/api/v1/admin/email", "", ah); r.code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", r.code, r.raw)
	}
	r = c.do(http.MethodGet, "/api/v1/admin/email", "", ah)
	if r.body["configured"] != false || r.body["passwordSet"] != false {
		t.Fatalf("after delete: %s", r.raw)
	}
}
