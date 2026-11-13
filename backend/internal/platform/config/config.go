// Package config loads and validates process configuration from PRORETA_* environment variables.
// Every loader collects all problems and returns them together, so a misconfigured pod reports
// everything at once. Secret values are wrapped in Secret and never printed.
package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/platform/logging"
)

// LookupFunc reads an environment variable; os.LookupEnv in production.
type LookupFunc func(string) (string, bool)

// Secret is a string that redacts itself when printed or logged.
type Secret string

// Value returns the secret value. Call sites are easy to grep for.
func (s Secret) Value() string { return string(s) }

func (Secret) String() string { return "[redacted]" }

// GoString redacts %#v as well.
func (Secret) GoString() string { return `"[redacted]"` }

// LogValue redacts the value in slog output.
func (Secret) LogValue() slog.Value { return slog.StringValue("[redacted]") }

// EncryptionKeys maps key id to a 32-byte AES key. It redacts itself when printed.
type EncryptionKeys map[string][]byte

func (EncryptionKeys) String() string { return "[redacted]" }

// GoString redacts %#v as well.
func (EncryptionKeys) GoString() string { return "[redacted]" }

// LogValue redacts the keys in slog output.
func (EncryptionKeys) LogValue() slog.Value { return slog.StringValue("[redacted]") }

// Base is shared by every process.
type Base struct {
	LogLevel slog.Level
}

// Server is shared by the long-running processes (api, worker, jobs).
type Server struct {
	Base
	AdminAddr        string
	DatabaseURL      Secret
	ShutdownTimeout  time.Duration
	DatabaseMaxConns int32
}

// Encryption holds the keys of the secrets stored in the database. Only api and jobs load it;
// the worker never gets the keys.
type Encryption struct {
	EncryptionKeys  EncryptionKeys
	EncryptionKeyID string
}

// RequestSeal holds the keys that seal the headers and bodies of monitor requests. It is a
// separate keyring so the worker can open them without holding the installation's data keys. Both
// settings are optional: without them monitors can still choose a method but cannot carry headers
// or a body, and a worker cannot open sealed requests.
type RequestSeal struct {
	Keys  EncryptionKeys
	KeyID string
}

// Guard is the SSRF guard configuration shared by every process that uses the guarded client.
type Guard struct {
	// BlockedCIDRs are blocked by the SSRF guard in addition to its built-in ranges
	// (PRORETA_BLOCKED_CIDRS, comma-separated).
	BlockedCIDRs []netip.Prefix
}

// API is the configuration of cmd/api.
type API struct {
	Server
	Encryption
	Guard
	// Request seals the headers and bodies of monitor requests.
	Request  RequestSeal
	HTTPAddr string
	// AdminLogin is the admin's login (PRORETA_ADMIN_LOGIN), lowercased.
	AdminLogin        string
	AdminPasswordHash Secret
	SessionTTL        time.Duration
	CookieSecure      bool
	// TrustedProxyHops is the number of trusted proxies in front of the api (PRORETA_TRUSTED_PROXY_HOPS,
	// default 0: X-Forwarded-For is ignored and the peer address is the client).
	TrustedProxyHops int
	// PublicURL is where users open Proreta (PRORETA_PUBLIC_URL, https or http on localhost, no trailing slash); used in
	// Slack messages.
	PublicURL string
	// RegistrationEnabled is the kill switch of self-service registration
	// (PRORETA_REGISTRATION_ENABLED, default true).
	RegistrationEnabled bool
	// PasswordLoginEnabled turns the password form off for everyone but the admin login when
	// false (PRORETA_PASSWORD_LOGIN_ENABLED, default true).
	PasswordLoginEnabled bool
	// StatusPageCustomDomains turns on status pages on a customer's own domain
	// (PRORETA_STATUS_PAGE_CUSTOM_DOMAINS, default false); off, the domain operations answer 404.
	StatusPageCustomDomains bool
	OAuth                   OAuth
}

// OAuth holds the OAuth client credentials. A provider without both values is off.
type OAuth struct {
	GoogleClientID     string
	GoogleClientSecret Secret
	GitHubClientID     string
	GitHubClientSecret Secret
	// AllowedDomains are the Google Workspace domains organizations may claim for auto-join
	// (PRORETA_OAUTH_ALLOWED_DOMAINS, comma-separated lowercase domains). Empty: claiming off.
	AllowedDomains []string
}

// GoogleEnabled reports whether Google sign-in is configured.
func (o OAuth) GoogleEnabled() bool { return o.GoogleClientID != "" }

// GitHubEnabled reports whether GitHub sign-in is configured.
func (o OAuth) GitHubEnabled() bool { return o.GitHubClientID != "" }

// Worker is the configuration of cmd/worker. It has no data encryption keys, only the request keys.
type Worker struct {
	Server
	Guard
	// Request opens the sealed headers and bodies of monitor requests; the only key the worker
	// holds.
	Request RequestSeal
	// ClaimBatch is the most (monitor, location) jobs one scheduler tick claims
	// (PRORETA_WORKER_CLAIM_BATCH, default 100).
	ClaimBatch int
	// Concurrency caps checks in flight in one worker process (PRORETA_WORKER_CONCURRENCY,
	// default 50); TenantConcurrency and HostConcurrency cap them per tenant and per target
	// host (PRORETA_WORKER_TENANT_CONCURRENCY default 5, PRORETA_WORKER_HOST_CONCURRENCY default 2).
	Concurrency       int
	TenantConcurrency int
	HostConcurrency   int
	// Location is the probe_locations code whose checks this worker runs
	// (PRORETA_WORKER_LOCATION, default "default").
	Location string
}

// SpeculaGateway is the configuration of cmd/speculagw, the endpoint of the executors that run in
// customer networks. It uses the worker's database role.
type SpeculaGateway struct {
	Server
	HTTPAddr string
	// Request opens the sealed headers and bodies of monitor requests handed to an executor.
	Request RequestSeal
}

// Specula is the configuration of cmd/specula, the executor that runs inside a customer's network.
// It has no database and no encryption keys: it only calls Proreta over HTTPS.
type Specula struct {
	Base
	AdminAddr string
	// URL is PRORETA_URL, the address of Proreta (the Specula gateway), without a trailing slash.
	URL string
	// Token is PRORETA_SPECULA_TOKEN, created in the app when the Specula is registered.
	Token Secret
	// Concurrency caps checks in flight (PRORETA_SPECULA_CONCURRENCY, default 20).
	Concurrency int
	// PollInterval is how often it asks for work (PRORETA_SPECULA_POLL_INTERVAL, default 5s).
	PollInterval time.Duration
}

// Jobs is the configuration of cmd/jobs.
type Jobs struct {
	Server
	Encryption
	Guard
	// PublicURL is PRORETA_PUBLIC_URL, linked from Slack messages and emails.
	PublicURL string
}

// Statuspage is the configuration of cmd/statuspage, the public status page renderer.
// Its database URL logs in as proreta_statuspage, which may only call status_page_snapshot(slug).
type Statuspage struct {
	Server
	HTTPAddr string
	// PublicURL is PRORETA_PUBLIC_URL: the origin of the badge and embed links and of the password
	// form's Origin check.
	PublicURL        string
	TrustedProxyHops int
	CookieSecure     bool
	// CookieKey signs the unlock cookies of password-protected pages
	// (PRORETA_STATUSPAGE_COOKIE_KEY, standard base64 of at least 32 bytes).
	CookieKey Secret
}

// Migrate is the configuration of cmd/migrate.
type Migrate struct {
	Base
	DatabaseURL Secret
}

// Bootstrap is the configuration of `proretactl db bootstrap`.
type Bootstrap struct {
	Base
	Host string
	Port int
	// AdminUser and AdminPassword are empty when the admin Secret is absent; the bootstrap then
	// only works if the database is already set up.
	AdminUser     string
	AdminPassword Secret
	// Force runs the full bootstrap even if the database looks ready.
	Force      bool
	DBName     string
	SSLMode    string
	SecretName string
	// Namespace is empty when PRORETA_BOOTSTRAP_NAMESPACE is unset; the caller then reads the
	// pod's service account namespace.
	Namespace string
	// SecretFile is PRORETA_BOOTSTRAP_SECRET_FILE: when set, the credentials are kept in this file
	// instead of a Kubernetes Secret (local development).
	SecretFile string
}

// shutdownTimeout leaves 5 s of the chart's 30 s termination grace period for the process to exit.
const shutdownTimeout = 25 * time.Second

// The lowest argon2id cost accepted in the admin hash (OWASP minimum: 19 MiB, 2 passes).
const (
	minHashMemoryKiB  = 19 * 1024
	minHashIterations = 2
)

// LoadAPI loads the api configuration.
func LoadAPI(lookup LookupFunc) (API, error) {
	l := &loader{lookup: lookup}
	cfg := API{
		Server:                  l.server(10),
		Encryption:              l.encryption(),
		Request:                 l.requestSeal(),
		Guard:                   l.guard(),
		HTTPAddr:                l.addr("PRORETA_HTTP_ADDR", ":8080"),
		AdminLogin:              l.login("PRORETA_ADMIN_LOGIN"),
		AdminPasswordHash:       l.passwordHash("PRORETA_ADMIN_PASSWORD_HASH"),
		SessionTTL:              l.duration("PRORETA_SESSION_TTL", 12*time.Hour, 5*time.Minute, 30*24*time.Hour),
		CookieSecure:            l.boolean("PRORETA_COOKIE_SECURE", true),
		TrustedProxyHops:        l.integer("PRORETA_TRUSTED_PROXY_HOPS", 0, 0, 10),
		PublicURL:               l.publicURL("PRORETA_PUBLIC_URL"),
		RegistrationEnabled:     l.boolean("PRORETA_REGISTRATION_ENABLED", true),
		PasswordLoginEnabled:    l.boolean("PRORETA_PASSWORD_LOGIN_ENABLED", true),
		StatusPageCustomDomains: l.boolean("PRORETA_STATUS_PAGE_CUSTOM_DOMAINS", false),
		OAuth:                   l.oauth(),
	}
	if cfg.HTTPAddr != "" && cfg.HTTPAddr == cfg.AdminAddr {
		l.fail("PRORETA_HTTP_ADDR", "must differ from PRORETA_ADMIN_ADDR")
	}
	l.cookieMatchesURL(cfg.CookieSecure, cfg.PublicURL)
	if !cfg.PasswordLoginEnabled && !cfg.OAuth.GoogleEnabled() && !cfg.OAuth.GitHubEnabled() {
		l.fail("PRORETA_PASSWORD_LOGIN_ENABLED", "false needs at least one OAuth provider (PRORETA_OAUTH_GOOGLE_* or PRORETA_OAUTH_GITHUB_*)")
	}
	return cfg, l.err()
}

// oauth reads the client credentials of each provider: both the id and the secret, or neither.
func (l *loader) oauth() OAuth {
	pair := func(prefix string) (string, Secret) {
		id, secret := l.optional(prefix+"_CLIENT_ID"), l.optional(prefix+"_CLIENT_SECRET")
		switch {
		case id == "" && secret == "":
			return "", ""
		case id == "" || secret == "":
			l.fail(prefix+"_CLIENT_ID", "and "+prefix+"_CLIENT_SECRET must be set together")
			return "", ""
		case len(id) > 256 || strings.ContainsAny(id, " \t\n\"'<>&"):
			l.fail(prefix+"_CLIENT_ID", "must be the client id from the provider's console (no spaces or quotes)")
		case len(secret) > 512 || strings.ContainsAny(secret, " \t\n"):
			// The value is never echoed.
			l.fail(prefix+"_CLIENT_SECRET", "must be the client secret from the provider's console (no whitespace)")
		}
		return id, Secret(secret)
	}
	var o OAuth
	o.GoogleClientID, o.GoogleClientSecret = pair("PRORETA_OAUTH_GOOGLE")
	o.GitHubClientID, o.GitHubClientSecret = pair("PRORETA_OAUTH_GITHUB")
	o.AllowedDomains = l.domains("PRORETA_OAUTH_ALLOWED_DOMAINS")
	return o
}

// domainName is the org_auth_domains.domain check: lowercase labels, at least one dot.
var domainName = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

// domains reads a comma-separated list of lowercase domain names (no wildcards, no trailing dot).
func (l *loader) domains(name string) []string {
	var out []string
	for _, part := range strings.Split(l.optional(name), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if len(part) > 253 || !domainName.MatchString(part) {
			l.fail(name, fmt.Sprintf("%q is not a lowercase domain name such as example.com", part))
			continue
		}
		if !slices.Contains(out, part) {
			out = append(out, part)
		}
	}
	return out
}

// LoadWorker loads the worker configuration.
func LoadWorker(lookup LookupFunc) (Worker, error) {
	l := &loader{lookup: lookup}
	cfg := Worker{
		Server:            l.server(5),
		Guard:             l.guard(),
		Request:           l.requestSeal(),
		ClaimBatch:        l.integer("PRORETA_WORKER_CLAIM_BATCH", 100, 1, 1000),
		Concurrency:       l.integer("PRORETA_WORKER_CONCURRENCY", 50, 1, 1000),
		TenantConcurrency: l.integer("PRORETA_WORKER_TENANT_CONCURRENCY", 5, 1, 1000),
		HostConcurrency:   l.integer("PRORETA_WORKER_HOST_CONCURRENCY", 2, 1, 1000),
		Location:          l.dnsLabel("PRORETA_WORKER_LOCATION", "default"),
	}
	return cfg, l.err()
}

// LoadSpeculaGateway loads the Specula gateway configuration.
func LoadSpeculaGateway(lookup LookupFunc) (SpeculaGateway, error) {
	l := &loader{lookup: lookup}
	cfg := SpeculaGateway{
		Server:   l.server(10),
		HTTPAddr: l.addr("PRORETA_HTTP_ADDR", ":8080"),
		Request:  l.requestSeal(),
	}
	if cfg.HTTPAddr != "" && cfg.HTTPAddr == cfg.AdminAddr {
		l.fail("PRORETA_HTTP_ADDR", "must differ from PRORETA_ADMIN_ADDR")
	}
	return cfg, l.err()
}

// LoadSpecula loads the Specula executor configuration.
func LoadSpecula(lookup LookupFunc) (Specula, error) {
	l := &loader{lookup: lookup}
	cfg := Specula{
		Base:         l.base(),
		AdminAddr:    l.addr("PRORETA_ADMIN_ADDR", ":9090"),
		URL:          l.publicURL("PRORETA_URL"),
		Token:        Secret(l.required("PRORETA_SPECULA_TOKEN")),
		Concurrency:  l.integer("PRORETA_SPECULA_CONCURRENCY", 20, 1, 500),
		PollInterval: l.duration("PRORETA_SPECULA_POLL_INTERVAL", 5*time.Second, time.Second, time.Minute),
	}
	if t := cfg.Token.Value(); t != "" && !strings.HasPrefix(t, "spc_") {
		l.fail("PRORETA_SPECULA_TOKEN", "must be a Specula token (it starts with spc_)")
	}
	return cfg, l.err()
}

// LoadJobs loads the jobs configuration.
func LoadJobs(lookup LookupFunc) (Jobs, error) {
	l := &loader{lookup: lookup}
	cfg := Jobs{Server: l.server(5), Encryption: l.encryption(), Guard: l.guard(), PublicURL: l.publicURL("PRORETA_PUBLIC_URL")}
	return cfg, l.err()
}

// LoadStatuspage loads the statuspage configuration.
func LoadStatuspage(lookup LookupFunc) (Statuspage, error) {
	l := &loader{lookup: lookup}
	cfg := Statuspage{
		Server:           l.server(5),
		HTTPAddr:         l.addr("PRORETA_HTTP_ADDR", ":8080"),
		PublicURL:        l.publicURL("PRORETA_PUBLIC_URL"),
		TrustedProxyHops: l.integer("PRORETA_TRUSTED_PROXY_HOPS", 0, 0, 10),
		CookieSecure:     l.boolean("PRORETA_COOKIE_SECURE", true),
		CookieKey:        l.base64Key("PRORETA_STATUSPAGE_COOKIE_KEY", 32),
	}
	if cfg.HTTPAddr != "" && cfg.HTTPAddr == cfg.AdminAddr {
		l.fail("PRORETA_HTTP_ADDR", "must differ from PRORETA_ADMIN_ADDR")
	}
	l.cookieMatchesURL(cfg.CookieSecure, cfg.PublicURL)
	return cfg, l.err()
}

// base64Key reads a required standard-base64 key of at least minBytes bytes; the decoded bytes are
// returned as a Secret.
func (l *loader) base64Key(name string, minBytes int) Secret {
	v := l.required(name)
	if v == "" {
		return ""
	}
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil || len(b) < minBytes {
		l.fail(name, fmt.Sprintf("must be standard base64 of at least %d random bytes", minBytes))
		return ""
	}
	return Secret(b)
}

// LoadMigrate loads the migrate configuration.
func LoadMigrate(lookup LookupFunc) (Migrate, error) {
	l := &loader{lookup: lookup}
	cfg := Migrate{Base: l.base(), DatabaseURL: l.databaseURL("PRORETA_MIGRATE_DATABASE_URL")}
	return cfg, l.err()
}

// LoadBootstrap loads the `proretactl db bootstrap` configuration.
func LoadBootstrap(lookup LookupFunc) (Bootstrap, error) {
	l := &loader{lookup: lookup}
	cfg := Bootstrap{
		Base:          l.base(),
		Host:          l.required("PRORETA_BOOTSTRAP_HOST"),
		Port:          l.integer("PRORETA_BOOTSTRAP_PORT", 5432, 1, 65535),
		AdminUser:     l.optional("PRORETA_BOOTSTRAP_ADMIN_USER"),
		AdminPassword: Secret(l.optional("PRORETA_BOOTSTRAP_ADMIN_PASSWORD")),
		Force:         l.boolean("PRORETA_BOOTSTRAP_FORCE", false),
		DBName:        l.identifier("PRORETA_BOOTSTRAP_DB_NAME", "proreta"),
		SSLMode:       l.oneOf("PRORETA_BOOTSTRAP_SSLMODE", "require", "disable", "allow", "prefer", "require", "verify-ca", "verify-full"),
		SecretName:    l.dnsLabel("PRORETA_BOOTSTRAP_SECRET_NAME", "proreta-db"),
		Namespace:     l.optional("PRORETA_BOOTSTRAP_NAMESPACE"),
		SecretFile:    l.optional("PRORETA_BOOTSTRAP_SECRET_FILE"),
	}
	if (cfg.AdminUser == "") != (cfg.AdminPassword == "") {
		l.fail("PRORETA_BOOTSTRAP_ADMIN_USER", "and PRORETA_BOOTSTRAP_ADMIN_PASSWORD must be set together")
	}
	if cfg.Namespace != "" && !isDNSLabel(cfg.Namespace) {
		l.fail("PRORETA_BOOTSTRAP_NAMESPACE", "must be a DNS label")
	}
	return cfg, l.err()
}

type loader struct {
	lookup LookupFunc
	errs   []error
}

func (l *loader) fail(name, msg string) {
	l.errs = append(l.errs, fmt.Errorf("%s: %s", name, msg))
}

func (l *loader) err() error {
	if len(l.errs) == 0 {
		return nil
	}
	msgs := make([]string, len(l.errs))
	for i, e := range l.errs {
		msgs[i] = e.Error()
	}
	return errors.New("invalid configuration:\n  " + strings.Join(msgs, "\n  "))
}

func (l *loader) optional(name string) string {
	v, _ := l.lookup(name)
	return strings.TrimSpace(v)
}

func (l *loader) str(name, def string) string {
	if v := l.optional(name); v != "" {
		return v
	}
	return def
}

func (l *loader) required(name string) string {
	v := l.optional(name)
	if v == "" {
		l.fail(name, "is required")
	}
	return v
}

func (l *loader) base() Base {
	level, err := logging.ParseLevel(l.str("PRORETA_LOG_LEVEL", "info"))
	if err != nil {
		l.fail("PRORETA_LOG_LEVEL", err.Error())
	}
	return Base{LogLevel: level}
}

func (l *loader) server(defaultMaxConns int32) Server {
	s := Server{
		Base:             l.base(),
		AdminAddr:        l.addr("PRORETA_ADMIN_ADDR", ":9090"),
		DatabaseURL:      l.databaseURL("PRORETA_DATABASE_URL"),
		ShutdownTimeout:  shutdownTimeout,
		DatabaseMaxConns: defaultMaxConns,
	}
	return s
}

func (l *loader) encryption() Encryption {
	var e Encryption
	e.EncryptionKeys, e.EncryptionKeyID = l.encryptionKeys()
	return e
}

func (l *loader) guard() Guard {
	return Guard{BlockedCIDRs: l.cidrs("PRORETA_BLOCKED_CIDRS")}
}

func (l *loader) addr(name, def string) string {
	v := l.str(name, def)
	if _, port, err := net.SplitHostPort(v); err != nil || port == "" {
		l.fail(name, "must be host:port or :port")
	}
	return v
}

func (l *loader) databaseURL(name string) Secret {
	v := l.required(name)
	if v == "" {
		return ""
	}
	// Do not include the parse error: it can echo parts of the URL.
	if _, err := pgx.ParseConfig(v); err != nil {
		l.fail(name, "is not a valid PostgreSQL connection string")
	}
	return Secret(v)
}

// publicURL accepts an absolute https URL without user info, query, fragment or trailing slash,
// made only of characters that need no escaping in a Slack link (<url|text>). Plain http is
// accepted only for a loopback host (local development).
func (l *loader) publicURL(name string) string {
	v := l.required(name)
	if v == "" {
		return ""
	}
	u, err := url.Parse(v)
	schemeOK := err == nil && (u.Scheme == "https" || (u.Scheme == "http" && IsLoopbackHost(u.Hostname())))
	if !schemeOK || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || strings.HasSuffix(v, "/") || strings.ContainsAny(v, "|<>& \t\n\"'") || u.String() != v {
		l.fail(name, "must be an https URL without a trailing slash, such as https://proreta.example.com (http is allowed only for localhost, 127.0.0.1 and [::1])")
	}
	return v
}

// IsLoopbackHost reports whether host (without port or brackets) is localhost, 127.0.0.1 or ::1.
func IsLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// cookieMatchesURL fails when the public URL is plain http but cookies are marked Secure: browsers
// do not send a Secure cookie back over http (Safari and curl never do), so sign-in would appear to
// work and the next request would be anonymous. The development setup must say so explicitly.
func (l *loader) cookieMatchesURL(cookieSecure bool, publicURL string) {
	if cookieSecure && strings.HasPrefix(publicURL, "http://") {
		l.fail("PRORETA_COOKIE_SECURE", "must be false when PRORETA_PUBLIC_URL is an http:// localhost URL (Secure cookies are not sent over plain http)")
	}
}

// CookieInsecureOverHTTPS reports a configuration that serves an https public URL with cookies
// that lack the Secure attribute. It is allowed (a TLS-terminating proxy test setup) but worth a
// startup warning.
func CookieInsecureOverHTTPS(cookieSecure bool, publicURL string) bool {
	return !cookieSecure && strings.HasPrefix(strings.ToLower(publicURL), "https://")
}

func (l *loader) login(name string) string {
	v := l.required(name)
	if v == "" {
		return ""
	}
	n, err := NormalizeLogin(v)
	if err != nil {
		l.fail(name, err.Error())
	}
	return n
}

// NormalizeLogin lowercases a login and checks that it is 3-254 characters of [a-z0-9._@+-]:
// the system login such as `admin`, or an email address. The minimum matches the
// users.email check. Logins are case-insensitive: every input is normalised with this
// function.
func NormalizeLogin(s string) (string, error) {
	if len(s) < 3 || len(s) > 254 {
		return "", fmt.Errorf("login must be 3-254 characters, got %d", len(s))
	}
	// Checked before lowercasing: strings.ToLower maps some non-ASCII runes (the Kelvin sign) to ASCII.
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && !strings.ContainsRune("._@+-", c) {
			return "", errors.New("login must contain only letters, digits and . _ @ + -")
		}
	}
	return strings.ToLower(s), nil
}

func (l *loader) passwordHash(name string) Secret {
	v := l.required(name)
	if v == "" {
		return ""
	}
	h, err := crypto.ParseArgon2idHash(v)
	switch {
	case err != nil:
		l.fail(name, "must be an argon2id PHC hash (generate with `proretactl hashpw`)")
	case h.Params.Memory < minHashMemoryKiB || h.Params.Iterations < minHashIterations:
		// Every password set later is hashed with the parameters of this hash.
		l.fail(name, fmt.Sprintf("argon2id cost is too low: need m>=%d KiB and t>=%d (generate with `proretactl hashpw`)", minHashMemoryKiB, minHashIterations))
	}
	return Secret(v)
}

func (l *loader) duration(name string, def, minimum, maximum time.Duration) time.Duration {
	v := l.optional(name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		l.fail(name, "must be a Go duration such as 12h")
		return def
	}
	if d < minimum || d > maximum {
		l.fail(name, fmt.Sprintf("must be between %s and %s", minimum, maximum))
	}
	return d
}

func (l *loader) boolean(name string, def bool) bool {
	v := l.optional(name)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.fail(name, "must be true or false")
		return def
	}
	return b
}

func (l *loader) integer(name string, def, minimum, maximum int) int {
	v := l.optional(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < minimum || n > maximum {
		l.fail(name, fmt.Sprintf("must be an integer between %d and %d", minimum, maximum))
		return def
	}
	return n
}

func (l *loader) cidrs(name string) []netip.Prefix {
	var out []netip.Prefix
	for _, part := range strings.Split(l.optional(name), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		p, err := netip.ParsePrefix(part)
		if err != nil {
			l.fail(name, fmt.Sprintf("%q is not a CIDR such as 10.0.0.0/8", part))
			continue
		}
		out = append(out, p.Masked())
	}
	return out
}

func (l *loader) oneOf(name, def string, allowed ...string) string {
	v := l.str(name, def)
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	l.fail(name, "must be one of "+strings.Join(allowed, ", "))
	return v
}

// identifier accepts lower-case SQL identifiers (database names are interpolated into DDL,
// quoted, but a narrow alphabet keeps them boring).
func (l *loader) identifier(name, def string) string {
	v := l.str(name, def)
	if !isIdentifier(v) {
		l.fail(name, "must match [a-z_][a-z0-9_]{0,62}")
	}
	return v
}

func (l *loader) dnsLabel(name, def string) string {
	v := l.str(name, def)
	if !isDNSLabel(v) {
		l.fail(name, "must be a DNS label")
	}
	return v
}

func (l *loader) encryptionKeys() (EncryptionKeys, string) {
	return l.keyMap("PRORETA_ENCRYPTION_KEYS", "PRORETA_ENCRYPTION_ACTIVE_KEY_ID", l.required)
}

// requestSeal reads PRORETA_REQUEST_KEYS and PRORETA_REQUEST_ACTIVE_KEY_ID: both or neither.
func (l *loader) requestSeal() RequestSeal {
	const keysName, idName = "PRORETA_REQUEST_KEYS", "PRORETA_REQUEST_ACTIVE_KEY_ID"
	switch haveKeys, haveID := l.optional(keysName) != "", l.optional(idName) != ""; {
	case !haveKeys && !haveID:
		return RequestSeal{}
	case haveKeys != haveID:
		l.fail(keysName, "and "+idName+" must be set together")
		return RequestSeal{}
	}
	keys, id := l.keyMap(keysName, idName, l.required)
	return RequestSeal{Keys: keys, KeyID: id}
}

func (l *loader) keyMap(keysName, idName string, get func(string) string) (EncryptionKeys, string) {
	raw := get(keysName)
	active := get(idName)
	if raw == "" {
		return nil, active
	}
	var encoded map[string]string
	if err := json.Unmarshal([]byte(raw), &encoded); err != nil {
		l.fail(keysName, `must be a JSON object {"<keyId>":"<base64 32 bytes>"}`)
		return nil, active
	}
	keys := make(EncryptionKeys, len(encoded))
	for id, b64 := range encoded {
		key, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(key) != 32 {
			l.fail(keysName, fmt.Sprintf("key %q must be standard base64 of 32 bytes", id))
			continue
		}
		keys[id] = key
	}
	if active != "" && len(keys) == len(encoded) {
		// NewKeyring checks key ids and that the active key exists.
		if _, err := crypto.NewKeyring(keys, active); err != nil {
			l.fail(keysName, err.Error())
		}
	}
	return keys, active
}

func isIdentifier(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	for i, c := range s {
		switch {
		case c == '_' || (c >= 'a' && c <= 'z'):
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func isDNSLabel(s string) bool {
	if len(s) == 0 || len(s) > 63 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, c := range s {
		if c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}
