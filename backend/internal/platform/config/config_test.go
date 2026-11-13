package config

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
)

func env(m map[string]string) LookupFunc {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func validHash(t *testing.T) string {
	t.Helper()
	return hashWith(t, 19*1024, 2)
}

func hashWith(t *testing.T, memoryKiB, iterations uint32) string {
	t.Helper()
	h, err := crypto.HashPassword("pw", crypto.Argon2Params{Memory: memoryKiB, Iterations: iterations, Parallelism: 1, SaltLength: 16, KeyLength: 32})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestLoadAPIRejectsCheapAdminHash(t *testing.T) {
	for name, h := range map[string]string{
		"low memory":     hashWith(t, 64, 2),
		"low iterations": hashWith(t, 19*1024, 1),
	} {
		e := apiEnv(t)
		e["PRORETA_ADMIN_PASSWORD_HASH"] = h
		_, err := LoadAPI(env(e))
		if err == nil || !strings.Contains(err.Error(), "argon2id cost is too low") {
			t.Errorf("%s: err = %v, want a cost error", name, err)
		}
	}
}

func apiEnv(t *testing.T) map[string]string {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	return map[string]string{
		"PRORETA_DATABASE_URL":             "postgres://proreta_api:pw@db:5432/proreta?sslmode=require",
		"PRORETA_ADMIN_LOGIN":              "admin",
		"PRORETA_ADMIN_PASSWORD_HASH":      validHash(t),
		"PRORETA_ENCRYPTION_KEYS":          fmt.Sprintf(`{"k1":%q}`, key),
		"PRORETA_ENCRYPTION_ACTIVE_KEY_ID": "k1",
		"PRORETA_PUBLIC_URL":               "https://proreta.example.com",
	}
}

func TestLoadAPIDefaults(t *testing.T) {
	cfg, err := LoadAPI(env(apiEnv(t)))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != slog.LevelInfo || cfg.HTTPAddr != ":8080" || cfg.AdminAddr != ":9090" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.SessionTTL != 12*time.Hour || !cfg.CookieSecure || cfg.TrustedProxyHops != 0 || !cfg.RegistrationEnabled {
		t.Fatalf("unexpected auth defaults: ttl=%v secure=%v hops=%d", cfg.SessionTTL, cfg.CookieSecure, cfg.TrustedProxyHops)
	}
	if cfg.ShutdownTimeout != 25*time.Second {
		t.Fatalf("shutdown timeout = %v", cfg.ShutdownTimeout)
	}
	if len(cfg.EncryptionKeys["k1"]) != 32 || cfg.EncryptionKeyID != "k1" {
		t.Fatal("encryption keys not decoded")
	}
}

func TestLoadAPIOverrides(t *testing.T) {
	e := apiEnv(t)
	e["PRORETA_LOG_LEVEL"] = "debug"
	e["PRORETA_SESSION_TTL"] = "24h"
	e["PRORETA_COOKIE_SECURE"] = "false"
	e["PRORETA_TRUSTED_PROXY_HOPS"] = "2"
	e["PRORETA_HTTP_ADDR"] = "127.0.0.1:8081"
	cfg, err := LoadAPI(env(e))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != slog.LevelDebug || cfg.SessionTTL != 24*time.Hour || cfg.CookieSecure || cfg.TrustedProxyHops != 2 || cfg.HTTPAddr != "127.0.0.1:8081" {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
}

func TestLoadAPIReportsAllErrors(t *testing.T) {
	_, err := LoadAPI(env(map[string]string{
		"PRORETA_LOG_LEVEL":                "loud",
		"PRORETA_ADMIN_LOGIN":              "Admin <admin@example.com>",
		"PRORETA_ADMIN_PASSWORD_HASH":      "plaintext-password",
		"PRORETA_SESSION_TTL":              "forever",
		"PRORETA_COOKIE_SECURE":            "maybe",
		"PRORETA_TRUSTED_PROXY_HOPS":       "-1",
		"PRORETA_ENCRYPTION_KEYS":          `{"k1":"c2hvcnQ="}`,
		"PRORETA_ENCRYPTION_ACTIVE_KEY_ID": "k1",
		"PRORETA_ADMIN_ADDR":               "9090",
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, name := range []string{
		"PRORETA_LOG_LEVEL", "PRORETA_ADMIN_LOGIN", "PRORETA_ADMIN_PASSWORD_HASH", "PRORETA_SESSION_TTL",
		"PRORETA_COOKIE_SECURE", "PRORETA_TRUSTED_PROXY_HOPS", "PRORETA_ENCRYPTION_KEYS", "PRORETA_DATABASE_URL", "PRORETA_ADMIN_ADDR",
	} {
		if !strings.Contains(msg, name) {
			t.Errorf("error does not mention %s:\n%s", name, msg)
		}
	}
	if strings.Contains(msg, "plaintext-password") {
		t.Error("error message leaks the password hash value")
	}
}

func TestDatabaseURLErrorDoesNotLeak(t *testing.T) {
	e := apiEnv(t)
	e["PRORETA_DATABASE_URL"] = "postgres://u:supersecret@host:notaport/db"
	_, err := LoadAPI(env(e))
	if err == nil || strings.Contains(err.Error(), "supersecret") {
		t.Fatalf("err = %v", err)
	}
}

func TestEncryptionActiveKeyMustExist(t *testing.T) {
	e := apiEnv(t)
	e["PRORETA_ENCRYPTION_ACTIVE_KEY_ID"] = "k2"
	if _, err := LoadAPI(env(e)); err == nil || !strings.Contains(err.Error(), "PRORETA_ENCRYPTION_KEYS") {
		t.Fatalf("err = %v", err)
	}
}

func TestSecretRedaction(t *testing.T) {
	s := Secret("hunter2")
	for _, out := range []string{fmt.Sprint(s), fmt.Sprintf("%v %s %#v", s, s, s), fmt.Sprintf("%+v", struct{ S Secret }{s})} {
		if strings.Contains(out, "hunter2") {
			t.Fatalf("secret leaked: %s", out)
		}
	}
	k := EncryptionKeys{"k1": []byte("0123456789abcdef0123456789abcdef")}
	if out := fmt.Sprintf("%v %+v %#v", k, k, k); strings.Contains(out, "0123") {
		t.Fatalf("keys leaked: %s", out)
	}
	if s.Value() != "hunter2" {
		t.Fatal("Value() must return the secret")
	}
}

func TestLoadWorkerAndMigrate(t *testing.T) {
	e := apiEnv(t)
	if _, err := LoadWorker(env(e)); err != nil {
		t.Fatalf("worker: %v", err)
	}
	// The worker never needs the encryption keys.
	delete(e, "PRORETA_ENCRYPTION_KEYS")
	delete(e, "PRORETA_ENCRYPTION_ACTIVE_KEY_ID")
	if _, err := LoadWorker(env(e)); err != nil {
		t.Fatalf("worker without encryption keys: %v", err)
	}
	if _, err := LoadMigrate(env(map[string]string{})); err == nil || !strings.Contains(err.Error(), "PRORETA_MIGRATE_DATABASE_URL") {
		t.Fatalf("migrate without URL: %v", err)
	}
	m, err := LoadMigrate(env(map[string]string{"PRORETA_MIGRATE_DATABASE_URL": "postgres://proreta_owner:pw@db/proreta"}))
	if err != nil || m.DatabaseURL.Value() == "" {
		t.Fatalf("migrate: %+v %v", m, err)
	}
}

func TestLoadJobs(t *testing.T) {
	e := apiEnv(t)
	cfg, err := LoadJobs(env(e))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.EncryptionKeys["k1"]) != 32 || cfg.EncryptionKeyID != "k1" || cfg.DatabaseMaxConns != 5 {
		t.Fatalf("jobs config = %+v", cfg)
	}
	delete(e, "PRORETA_ENCRYPTION_KEYS")
	if _, err := LoadJobs(env(e)); err == nil || !strings.Contains(err.Error(), "PRORETA_ENCRYPTION_KEYS") {
		t.Fatalf("jobs without encryption keys: %v", err)
	}
}

// Every process using the guarded client reads PRORETA_BLOCKED_CIDRS.
func TestBlockedCIDRsInEveryGuardedProcess(t *testing.T) {
	loaders := map[string]func(LookupFunc) (Guard, error){
		"api": func(l LookupFunc) (Guard, error) { c, err := LoadAPI(l); return c.Guard, err },
		"worker": func(l LookupFunc) (Guard, error) {
			c, err := LoadWorker(l)
			return c.Guard, err
		},
		"jobs": func(l LookupFunc) (Guard, error) { c, err := LoadJobs(l); return c.Guard, err },
	}
	for name, load := range loaders {
		t.Run(name, func(t *testing.T) {
			e := apiEnv(t)
			e["PRORETA_BLOCKED_CIDRS"] = " 203.0.113.7/24, ,2001:db8::/32 "
			cfg, err := load(env(e))
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.BlockedCIDRs) != 2 || cfg.BlockedCIDRs[0].String() != "203.0.113.0/24" || cfg.BlockedCIDRs[1].String() != "2001:db8::/32" {
				t.Fatalf("blocked cidrs = %v", cfg.BlockedCIDRs)
			}
			e["PRORETA_BLOCKED_CIDRS"] = "10.0.0.0/8,not-a-cidr"
			if _, err := load(env(e)); err == nil || !strings.Contains(err.Error(), "PRORETA_BLOCKED_CIDRS") {
				t.Fatalf("invalid cidr accepted: %v", err)
			}
		})
	}
}

func TestLoadWorkerLimits(t *testing.T) {
	e := apiEnv(t)
	cfg, err := LoadWorker(env(e))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClaimBatch != 100 || cfg.Concurrency != 50 || cfg.TenantConcurrency != 5 || cfg.HostConcurrency != 2 || cfg.Location != "default" {
		t.Fatalf("defaults = %d %d %d %d %q", cfg.ClaimBatch, cfg.Concurrency, cfg.TenantConcurrency, cfg.HostConcurrency, cfg.Location)
	}
	e["PRORETA_WORKER_LOCATION"] = "Home_EU"
	if _, err := LoadWorker(env(e)); err == nil || !strings.Contains(err.Error(), "PRORETA_WORKER_LOCATION") {
		t.Fatalf("invalid location accepted: %v", err)
	}
	e["PRORETA_WORKER_LOCATION"] = "aws-us-east-1"
	if cfg, err := LoadWorker(env(e)); err != nil || cfg.Location != "aws-us-east-1" {
		t.Fatalf("location = %q, err %v", cfg.Location, err)
	}
	e["PRORETA_WORKER_CONCURRENCY"] = "20"
	e["PRORETA_WORKER_HOST_CONCURRENCY"] = "0"
	if _, err := LoadWorker(env(e)); err == nil || !strings.Contains(err.Error(), "PRORETA_WORKER_HOST_CONCURRENCY") {
		t.Fatalf("zero host concurrency accepted: %v", err)
	}
}

func TestLoadBootstrap(t *testing.T) {
	cfg, err := LoadBootstrap(env(map[string]string{
		"PRORETA_BOOTSTRAP_HOST":           "db.example.internal",
		"PRORETA_BOOTSTRAP_ADMIN_USER":     "postgres",
		"PRORETA_BOOTSTRAP_ADMIN_PASSWORD": "pw",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 5432 || cfg.DBName != "proreta" || cfg.SSLMode != "require" || cfg.SecretName != "proreta-db" || cfg.Namespace != "" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}

	_, err = LoadBootstrap(env(map[string]string{
		"PRORETA_BOOTSTRAP_PORT":        "70000",
		"PRORETA_BOOTSTRAP_DB_NAME":     "Proreta; DROP",
		"PRORETA_BOOTSTRAP_SSLMODE":     "off",
		"PRORETA_BOOTSTRAP_SECRET_NAME": "Bad_Name",
		"PRORETA_BOOTSTRAP_NAMESPACE":   "-ns",
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, name := range []string{"HOST", "PORT", "DB_NAME", "SSLMODE", "SECRET_NAME", "NAMESPACE"} {
		if !strings.Contains(err.Error(), "PRORETA_BOOTSTRAP_"+name) {
			t.Errorf("error does not mention PRORETA_BOOTSTRAP_%s", name)
		}
	}
}

func TestLoadBootstrapAdminAndForce(t *testing.T) {
	base := map[string]string{"PRORETA_BOOTSTRAP_HOST": "db.example.internal"}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	cfg, err := LoadBootstrap(env(base))
	if err != nil || cfg.AdminUser != "" || cfg.AdminPassword != "" || cfg.Force {
		t.Fatalf("no admin login must be accepted: %+v, %v", cfg, err)
	}
	cfg, err = LoadBootstrap(env(with(map[string]string{
		"PRORETA_BOOTSTRAP_ADMIN_USER": "postgres", "PRORETA_BOOTSTRAP_ADMIN_PASSWORD": "pw", "PRORETA_BOOTSTRAP_FORCE": "true",
	})))
	if err != nil || cfg.AdminUser != "postgres" || !cfg.Force {
		t.Fatalf("admin login and force: %+v, %v", cfg, err)
	}
	if _, err = LoadBootstrap(env(with(map[string]string{"PRORETA_BOOTSTRAP_ADMIN_USER": "postgres"}))); err == nil {
		t.Fatal("a user without a password must be rejected")
	}
	if _, err = LoadBootstrap(env(with(map[string]string{"PRORETA_BOOTSTRAP_FORCE": "maybe"}))); err == nil {
		t.Fatal("a bad force value must be rejected")
	}
}

// PRORETA_PUBLIC_URL is linked from Slack messages as <url/monitors/id|text>, so it must be a plain
// https URL without a trailing slash or characters that break the link.
func TestPublicURL(t *testing.T) {
	loaders := map[string]func(LookupFunc) (string, error){
		"api":  func(l LookupFunc) (string, error) { c, err := LoadAPI(l); return c.PublicURL, err },
		"jobs": func(l LookupFunc) (string, error) { c, err := LoadJobs(l); return c.PublicURL, err },
	}
	for name, load := range loaders {
		t.Run(name, func(t *testing.T) {
			e := apiEnv(t)
			for _, ok := range []string{"https://proreta.example.com", "https://proreta.example.com/app", "https://host:8443"} {
				e["PRORETA_PUBLIC_URL"] = ok
				if got, err := load(env(e)); err != nil || got != ok {
					t.Errorf("%q: got %q, %v", ok, got, err)
				}
			}
			e["PRORETA_COOKIE_SECURE"] = "false"
			for _, ok := range []string{"http://localhost", "http://localhost:5173", "http://127.0.0.1:8080", "http://[::1]:5173"} {
				e["PRORETA_PUBLIC_URL"] = ok
				if got, err := load(env(e)); err != nil || got != ok {
					t.Errorf("%q: got %q, %v", ok, got, err)
				}
			}
			delete(e, "PRORETA_COOKIE_SECURE")
			for _, bad := range []string{"", "http://proreta.example.com", "http://localhost.example.com", "http://127.0.0.2", "http://0.0.0.0:8080",
				"http://localhost/", "http://user@localhost", "ftp://localhost", "https://proreta.example.com/", "proreta.example.com",
				"https://user:pw@proreta.example.com", "https://proreta.example.com?x=1", "https://proreta.example.com#a",
				"https://proreta.example.com/a|b", "https://proreta.example.com/<x>", "https://"} {
				e["PRORETA_PUBLIC_URL"] = bad
				if _, err := load(env(e)); err == nil || !strings.Contains(err.Error(), "PRORETA_PUBLIC_URL") {
					t.Errorf("%q accepted: %v", bad, err)
				}
			}
			delete(e, "PRORETA_PUBLIC_URL")
			if _, err := load(env(e)); err == nil || !strings.Contains(err.Error(), "PRORETA_PUBLIC_URL") {
				t.Errorf("missing PRORETA_PUBLIC_URL accepted: %v", err)
			}
		})
	}
}

func TestRegistrationKillSwitch(t *testing.T) {
	for in, want := range map[string]bool{"false": false, "0": false, "true": true, "TRUE": true, " false ": false} {
		e := apiEnv(t)
		e["PRORETA_REGISTRATION_ENABLED"] = in
		cfg, err := LoadAPI(env(e))
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if cfg.RegistrationEnabled != want {
			t.Fatalf("%q: RegistrationEnabled = %v", in, cfg.RegistrationEnabled)
		}
	}
	for _, in := range []string{"yes", "off", "2"} {
		e := apiEnv(t)
		e["PRORETA_REGISTRATION_ENABLED"] = in
		if _, err := LoadAPI(env(e)); err == nil || !strings.Contains(err.Error(), "PRORETA_REGISTRATION_ENABLED: must be true or false") {
			t.Fatalf("%q accepted: %v", in, err)
		}
	}
}

func TestStatusPageCustomDomainsSwitch(t *testing.T) {
	cfg, err := LoadAPI(env(apiEnv(t)))
	if err != nil || cfg.StatusPageCustomDomains {
		t.Fatalf("default: %v, %v", cfg.StatusPageCustomDomains, err)
	}
	for in, want := range map[string]bool{"true": true, "1": true, "false": false, " TRUE ": true} {
		e := apiEnv(t)
		e["PRORETA_STATUS_PAGE_CUSTOM_DOMAINS"] = in
		cfg, err := LoadAPI(env(e))
		if err != nil || cfg.StatusPageCustomDomains != want {
			t.Fatalf("%q: %v, %v", in, cfg.StatusPageCustomDomains, err)
		}
	}
	for _, in := range []string{"yes", "on", "2"} {
		e := apiEnv(t)
		e["PRORETA_STATUS_PAGE_CUSTOM_DOMAINS"] = in
		if _, err := LoadAPI(env(e)); err == nil || !strings.Contains(err.Error(), "PRORETA_STATUS_PAGE_CUSTOM_DOMAINS: must be true or false") {
			t.Fatalf("%q accepted: %v", in, err)
		}
	}
}

func TestAdminLogin(t *testing.T) {
	for in, want := range map[string]string{
		"admin": "admin", "Admin": "admin", " ADMIN ": "admin", "owner@example.com": "owner@example.com",
		"ops.team_1-a": "ops.team_1-a", strings.Repeat("a", 254): strings.Repeat("a", 254),
		"abc": "abc", "User+Tag@Example.com": "user+tag@example.com",
	} {
		e := apiEnv(t)
		e["PRORETA_ADMIN_LOGIN"] = in
		cfg, err := LoadAPI(env(e))
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if cfg.AdminLogin != want {
			t.Fatalf("%q: AdminLogin = %q, want %q", in, cfg.AdminLogin, want)
		}
	}
	// Shorter than 3 or longer than 254 is rejected.
	for _, in := range []string{"ab", "a", "ad min", "adm!n", "Admin <a@example.com>", strings.Repeat("a", 255), "Kdmin", "admín"} {
		e := apiEnv(t)
		e["PRORETA_ADMIN_LOGIN"] = in
		if _, err := LoadAPI(env(e)); err == nil || !strings.Contains(err.Error(), "PRORETA_ADMIN_LOGIN") {
			t.Fatalf("%q accepted or error does not name the variable: %v", in, err)
		}
	}
	e := apiEnv(t)
	delete(e, "PRORETA_ADMIN_LOGIN")
	if _, err := LoadAPI(env(e)); err == nil || !strings.Contains(err.Error(), "PRORETA_ADMIN_LOGIN: is required") {
		t.Fatalf("missing login: %v", err)
	}
}

func TestLoadStatuspage(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	e := map[string]string{
		"PRORETA_DATABASE_URL":          "postgres://proreta_statuspage:pw@db:5432/proreta?sslmode=require",
		"PRORETA_PUBLIC_URL":            "https://proreta.example.com",
		"PRORETA_STATUSPAGE_COOKIE_KEY": key,
	}
	cfg, err := LoadStatuspage(env(e))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.AdminAddr != ":9090" || cfg.DatabaseMaxConns != 5 || !cfg.CookieSecure || cfg.TrustedProxyHops != 0 ||
		len(cfg.CookieKey.Value()) != 32 || cfg.PublicURL != "https://proreta.example.com" {
		t.Fatalf("statuspage config = %+v", cfg)
	}
	if s := fmt.Sprintf("%v %+v %#v", cfg.CookieKey, cfg, cfg); strings.Contains(s, "0123456789abcdef") {
		t.Fatal("cookie key printed")
	}
	// No encryption keys or admin credentials are needed (or read).
	for _, bad := range []map[string]string{
		{"PRORETA_STATUSPAGE_COOKIE_KEY": ""},
		{"PRORETA_STATUSPAGE_COOKIE_KEY": base64.StdEncoding.EncodeToString(make([]byte, 16))},
		{"PRORETA_STATUSPAGE_COOKIE_KEY": "not base64!"},
		{"PRORETA_PUBLIC_URL": "http://proreta.example.com"},
	} {
		m := map[string]string{}
		for k, v := range e {
			m[k] = v
		}
		for k, v := range bad {
			m[k] = v
		}
		if _, err := LoadStatuspage(env(m)); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

func TestLoadAPIOAuth(t *testing.T) {
	cfg, err := LoadAPI(env(apiEnv(t)))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.PasswordLoginEnabled || cfg.OAuth.GoogleEnabled() || cfg.OAuth.GitHubEnabled() {
		t.Fatalf("defaults: password=%v oauth=%+v", cfg.PasswordLoginEnabled, cfg.OAuth)
	}

	e := apiEnv(t)
	e["PRORETA_OAUTH_GOOGLE_CLIENT_ID"] = "123.apps.googleusercontent.com"
	e["PRORETA_OAUTH_GOOGLE_CLIENT_SECRET"] = "GOCSPX-supersecret"
	e["PRORETA_PASSWORD_LOGIN_ENABLED"] = "false"
	cfg, err = LoadAPI(env(e))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.OAuth.GoogleEnabled() || cfg.OAuth.GitHubEnabled() || cfg.PasswordLoginEnabled || cfg.OAuth.GoogleClientSecret.Value() != "GOCSPX-supersecret" {
		t.Fatalf("oauth: %+v password=%v", cfg.OAuth, cfg.PasswordLoginEnabled)
	}
	if s := fmt.Sprintf("%v %+v %#v", cfg, cfg, cfg); strings.Contains(s, "supersecret") {
		t.Fatal("client secret printed")
	}

	cases := map[string]struct {
		set  map[string]string
		want string
	}{
		"id without secret":              {map[string]string{"PRORETA_OAUTH_GITHUB_CLIENT_ID": "Iv1.abc"}, "PRORETA_OAUTH_GITHUB_CLIENT_ID: and PRORETA_OAUTH_GITHUB_CLIENT_SECRET must be set together"},
		"secret without id":              {map[string]string{"PRORETA_OAUTH_GOOGLE_CLIENT_SECRET": "s3cret-value"}, "PRORETA_OAUTH_GOOGLE_CLIENT_ID"},
		"id with spaces":                 {map[string]string{"PRORETA_OAUTH_GOOGLE_CLIENT_ID": "a b", "PRORETA_OAUTH_GOOGLE_CLIENT_SECRET": "x"}, "must be the client id"},
		"secret with space":              {map[string]string{"PRORETA_OAUTH_GOOGLE_CLIENT_ID": "a", "PRORETA_OAUTH_GOOGLE_CLIENT_SECRET": "s3cret value"}, "must be the client secret"},
		"password off without providers": {map[string]string{"PRORETA_PASSWORD_LOGIN_ENABLED": "false"}, "needs at least one OAuth provider"},
	}
	for name, c := range cases {
		e := apiEnv(t)
		for k, v := range c.set {
			e[k] = v
		}
		_, err := LoadAPI(env(e))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
		if err != nil && strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%s: error echoes the secret", name)
		}
	}
}

func TestLoadAPIAllowedDomains(t *testing.T) {
	e := apiEnv(t)
	e["PRORETA_OAUTH_ALLOWED_DOMAINS"] = " example.com, sub.example.org ,example.com"
	cfg, err := LoadAPI(env(e))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.OAuth.AllowedDomains, ",") != "example.com,sub.example.org" {
		t.Fatalf("allowed domains: %v", cfg.OAuth.AllowedDomains)
	}
	if cfg, err := LoadAPI(env(apiEnv(t))); err != nil || len(cfg.OAuth.AllowedDomains) != 0 {
		t.Fatalf("default: %v %v", cfg.OAuth.AllowedDomains, err)
	}
	for _, bad := range []string{"Example.com", "*.example.com", "example", "example.com.", "exa mple.com"} {
		e := apiEnv(t)
		e["PRORETA_OAUTH_ALLOWED_DOMAINS"] = bad
		if _, err := LoadAPI(env(e)); err == nil || !strings.Contains(err.Error(), "PRORETA_OAUTH_ALLOWED_DOMAINS") {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

func TestCookieInsecureOverHTTPS(t *testing.T) {
	for _, c := range []struct {
		secure bool
		url    string
		want   bool
	}{
		{false, "https://proreta.example.com", true},
		{true, "https://proreta.example.com", false},
		{false, "http://localhost:8080", false},
	} {
		if got := CookieInsecureOverHTTPS(c.secure, c.url); got != c.want {
			t.Errorf("CookieInsecureOverHTTPS(%v, %q) = %v, want %v", c.secure, c.url, got, c.want)
		}
	}
}

// Plain http is for local development only: it needs a loopback host and explicit non-Secure cookies.
func TestHTTPPublicURLNeedsInsecureCookies(t *testing.T) {
	e := apiEnv(t)
	e["PRORETA_PUBLIC_URL"] = "http://localhost:5173"
	if _, err := LoadAPI(env(e)); err == nil || !strings.Contains(err.Error(), "PRORETA_COOKIE_SECURE") {
		t.Fatalf("api accepted http with secure cookies: %v", err)
	}
	e["PRORETA_COOKIE_SECURE"] = "false"
	if cfg, err := LoadAPI(env(e)); err != nil || cfg.CookieSecure {
		t.Fatalf("api: %v, secure=%v", err, cfg.CookieSecure)
	}

	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	s := map[string]string{
		"PRORETA_DATABASE_URL":          "postgres://proreta_statuspage:pw@db:5432/proreta?sslmode=require",
		"PRORETA_PUBLIC_URL":            "http://127.0.0.1:5173",
		"PRORETA_STATUSPAGE_COOKIE_KEY": key,
	}
	if _, err := LoadStatuspage(env(s)); err == nil || !strings.Contains(err.Error(), "PRORETA_COOKIE_SECURE") {
		t.Fatalf("statuspage accepted http with secure cookies: %v", err)
	}
	s["PRORETA_COOKIE_SECURE"] = "false"
	if _, err := LoadStatuspage(env(s)); err != nil {
		t.Fatal(err)
	}

	// jobs sets no cookies: only the URL rule applies.
	j := apiEnv(t)
	j["PRORETA_PUBLIC_URL"] = "http://localhost:5173"
	if _, err := LoadJobs(env(j)); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapSecretFile(t *testing.T) {
	e := map[string]string{
		"PRORETA_BOOTSTRAP_HOST": "127.0.0.1", "PRORETA_BOOTSTRAP_ADMIN_USER": "postgres", "PRORETA_BOOTSTRAP_ADMIN_PASSWORD": "pw",
		"PRORETA_BOOTSTRAP_SECRET_FILE": "/tmp/db.secret",
	}
	cfg, err := LoadBootstrap(env(e))
	if err != nil || cfg.SecretFile != "/tmp/db.secret" {
		t.Fatalf("secret file = %q, %v", cfg.SecretFile, err)
	}
}

func TestRequestSeal(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	e := apiEnv(t)
	cfg, err := LoadAPI(env(e))
	if err != nil || cfg.Request.Keys != nil || cfg.Request.KeyID != "" {
		t.Fatalf("unset: %+v %v", cfg.Request, err)
	}
	e["PRORETA_REQUEST_KEYS"] = fmt.Sprintf(`{"r1":%q}`, key)
	if _, err := LoadAPI(env(e)); err == nil || !strings.Contains(err.Error(), "must be set together") {
		t.Fatalf("keys without id: %v", err)
	}
	e["PRORETA_REQUEST_ACTIVE_KEY_ID"] = "r1"
	cfg, err = LoadAPI(env(e))
	if err != nil || len(cfg.Request.Keys) != 1 || cfg.Request.KeyID != "r1" {
		t.Fatalf("api: %+v %v", cfg.Request, err)
	}
	if fmt.Sprint(cfg.Request.Keys) != "[redacted]" {
		t.Fatalf("keys not redacted: %v", cfg.Request.Keys)
	}
	w, err := LoadWorker(env(e))
	if err != nil || len(w.Request.Keys) != 1 {
		t.Fatalf("worker: %+v %v", w.Request, err)
	}
	e["PRORETA_REQUEST_ACTIVE_KEY_ID"] = "other"
	if _, err := LoadWorker(env(e)); err == nil || !strings.Contains(err.Error(), "PRORETA_REQUEST_KEYS") {
		t.Fatalf("unknown active id: %v", err)
	}
	e["PRORETA_REQUEST_ACTIVE_KEY_ID"] = "r1"
	e["PRORETA_REQUEST_KEYS"] = `{"r1":"c2hvcnQ="}`
	if _, err := LoadAPI(env(e)); err == nil || !strings.Contains(err.Error(), "PRORETA_REQUEST_KEYS") {
		t.Fatalf("short key: %v", err)
	}
}

func TestLoadSpecula(t *testing.T) {
	e := map[string]string{"PRORETA_URL": "https://proreta.example.com", "PRORETA_SPECULA_TOKEN": "spc_abc"}
	cfg, err := LoadSpecula(env(e))
	if err != nil || cfg.URL != "https://proreta.example.com" || cfg.Token.Value() != "spc_abc" || cfg.Concurrency != 20 || cfg.PollInterval != 5*time.Second {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	if fmt.Sprint(cfg.Token) != "[redacted]" {
		t.Fatalf("token printed: %v", cfg.Token)
	}
	e["PRORETA_SPECULA_CONCURRENCY"], e["PRORETA_SPECULA_POLL_INTERVAL"] = "50", "2s"
	if cfg, err := LoadSpecula(env(e)); err != nil || cfg.Concurrency != 50 || cfg.PollInterval != 2*time.Second {
		t.Fatalf("custom: %+v %v", cfg, err)
	}
	for name, mutate := range map[string]func(){
		"PRORETA_URL":                   func() { delete(e, "PRORETA_URL") },
		"PRORETA_SPECULA_TOKEN":         func() { e["PRORETA_SPECULA_TOKEN"] = "plain" },
		"PRORETA_SPECULA_CONCURRENCY":   func() { e["PRORETA_SPECULA_CONCURRENCY"] = "0" },
		"PRORETA_SPECULA_POLL_INTERVAL": func() { e["PRORETA_SPECULA_POLL_INTERVAL"] = "1ms" },
	} {
		bad := map[string]string{}
		for k, v := range e {
			bad[k] = v
		}
		saved := e
		e = bad
		mutate()
		if _, err := LoadSpecula(env(e)); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: %v", name, err)
		}
		e = saved
	}
	if _, err := LoadSpecula(env(map[string]string{"PRORETA_URL": "http://proreta.example.com", "PRORETA_SPECULA_TOKEN": "spc_x"})); err == nil {
		t.Error("plain http accepted for a remote host")
	}
}

func TestLoadJobsDomainChecks(t *testing.T) {
	load := func(extra map[string]string) (Jobs, error) {
		e := apiEnv(t)
		for k, v := range extra {
			e[k] = v
		}
		return LoadJobs(env(e))
	}
	cfg, err := load(nil)
	if err != nil || cfg.DomainChecks.Enabled || cfg.DomainChecks.Timeout != 3*time.Second {
		t.Fatalf("defaults: %+v %v", cfg.DomainChecks, err)
	}
	cfg, err = load(map[string]string{"PRORETA_STATUS_PAGE_CUSTOM_DOMAINS": "true", "PRORETA_STATUSPAGE_CNAME_TARGET": "Edge.Example.Net.", "PRORETA_DNS_RESOLVER": "10.0.0.2:53", "PRORETA_DNS_TIMEOUT": "2s"})
	if err != nil || !cfg.DomainChecks.Enabled || cfg.DomainChecks.CNAMETarget != "edge.example.net" || cfg.DomainChecks.Resolver != "10.0.0.2:53" || cfg.DomainChecks.Timeout != 2*time.Second {
		t.Fatalf("enabled: %+v %v", cfg.DomainChecks, err)
	}
	for name, extra := range map[string]map[string]string{
		"target required":    {"PRORETA_STATUS_PAGE_CUSTOM_DOMAINS": "true"},
		"target not a host":  {"PRORETA_STATUS_PAGE_CUSTOM_DOMAINS": "true", "PRORETA_STATUSPAGE_CNAME_TARGET": "https://edge.example.net"},
		"resolver not an IP": {"PRORETA_DNS_RESOLVER": "dns.example.net"},
		"timeout too small":  {"PRORETA_DNS_TIMEOUT": "1ms"},
	} {
		if _, err := load(extra); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
