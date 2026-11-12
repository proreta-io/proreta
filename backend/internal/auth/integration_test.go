package auth_test

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

var cheap = crypto.Argon2Params{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}

func TestIntegrationBootstrapAdminIdempotent(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("auth"))
	ctx := context.Background()
	pool := env.Pool(t, env.APIURL, 8)
	admin := env.AdminConn(t)

	hash1, _ := crypto.HashPassword("first", cheap)
	hash2, _ := crypto.HashPassword("second", cheap)

	// Several replicas starting at once.
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc, err := auth.NewService(pool, auth.Options{SessionTTL: time.Hour})
			if err == nil {
				err = svc.BootstrapAdmin(ctx, "admin", hash1)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent bootstrap: %v", err)
		}
	}

	svc, err := auth.NewService(pool, auth.Options{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.BootstrapAdmin(ctx, "admin", hash1); err != nil {
		t.Fatal(err)
	}
	// Unknown logins are verified against a dummy hash with the admin hash's cost.
	if got := svc.DummyParams(); got != cheap {
		t.Fatalf("dummy hash params = %+v, want the admin's %+v", got, cheap)
	}
	assertCounts := func(wantHash string) {
		t.Helper()
		var tenants, users, members int
		var stored, role string
		q := `SELECT (SELECT count(*) FROM tenants WHERE slug = 'default'),
		             (SELECT count(*) FROM users),
		             (SELECT count(*) FROM memberships),
		             (SELECT password_hash FROM users WHERE email = 'admin'),
		             (SELECT role FROM memberships)`
		if err := admin.QueryRow(ctx, q).Scan(&tenants, &users, &members, &stored, &role); err != nil {
			t.Fatal(err)
		}
		if tenants != 1 || users != 1 || members != 1 || role != "owner" {
			t.Fatalf("tenants=%d users=%d memberships=%d role=%s, want 1/1/1/owner", tenants, users, members, role)
		}
		if stored != wantHash {
			t.Fatal("stored password hash does not match the configured one")
		}
	}
	assertCounts(hash1)
	assertPlan := func(want string) {
		t.Helper()
		var plan string
		if err := admin.QueryRow(ctx, `SELECT plan_key FROM tenants WHERE slug = 'default'`).Scan(&plan); err != nil || plan != want {
			t.Fatalf("default tenant plan = %q (%v), want %q", plan, err, want)
		}
	}
	// The default tenant is created on the internal plan.
	assertPlan("internal")

	// A changed Secret (password reset) replaces the hash; a demoted membership is restored. A
	// plan changed meanwhile is kept: bootstrap never changes the plan of an existing tenant.
	if _, err := admin.Exec(ctx, `UPDATE memberships SET role = 'viewer'`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE tenants SET plan_key = 'free'`); err != nil {
		t.Fatal(err)
	}
	if err := svc.BootstrapAdmin(ctx, "ADMIN", hash2); err != nil {
		t.Fatal(err)
	}
	assertCounts(hash2)
	assertPlan("free")
	var stored string
	if err := admin.QueryRow(ctx, `SELECT email::text FROM users`).Scan(&stored); err != nil || stored != "admin" {
		t.Fatalf("stored login = %q (err %v), want it lowercased", stored, err)
	}

	if err := svc.BootstrapAdmin(ctx, "admin", "not-a-hash"); !errors.Is(err, crypto.ErrInvalidHash) {
		t.Fatalf("invalid hash: err = %v", err)
	}
}

var testIP = netip.MustParseAddr("192.0.2.10")

func newBootstrapped(t *testing.T, env *pgtest.Env, login, hash string) *auth.Service {
	t.Helper()
	svc, err := auth.NewService(env.Pool(t, env.APIURL, 8), auth.Options{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.BootstrapAdmin(context.Background(), login, hash); err != nil {
		t.Fatal(err)
	}
	return svc
}

func login(t *testing.T, svc *auth.Service, name, password string) (auth.LoginResult, error) {
	t.Helper()
	return svc.Login(context.Background(), auth.LoginInput{Login: name, Password: password, IP: testIP})
}

func mustLogin(t *testing.T, svc *auth.Service, name, password string) string {
	t.Helper()
	res, err := login(t, svc, name, password)
	if err != nil {
		t.Fatalf("login %s: %v", name, err)
	}
	return res.Token
}

func TestIntegrationAdminLoginRotation(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("auth_rotation"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	hash, _ := crypto.HashPassword("pw", cheap)

	svcA := newBootstrapped(t, env, "a@example.com", hash)
	tokenA := mustLogin(t, svcA, "a@example.com", "pw")

	// The Secret now names b@: a@ is disabled, its session revoked, b@ can log in.
	svcB := newBootstrapped(t, env, "b@example.com", hash)
	if _, err := svcB.Authenticate(ctx, tokenA); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("old admin session: err = %v, want ErrUnauthenticated", err)
	}
	if _, err := login(t, svcB, "a@example.com", "pw"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("old admin login: err = %v, want ErrInvalidCredentials", err)
	}
	tokenB := mustLogin(t, svcB, "b@example.com", "pw")
	if _, err := svcB.Authenticate(ctx, tokenB); err != nil {
		t.Fatalf("new admin session: %v", err)
	}

	var statusA string
	var sessionsA, rotated int
	if err := admin.QueryRow(ctx, `SELECT (SELECT status FROM users WHERE email = 'a@example.com'),
		(SELECT count(*) FROM sessions s JOIN users u ON u.id = s.user_id WHERE u.email = 'a@example.com'),
		(SELECT count(*) FROM audit_log WHERE action = 'admin.rotated'
		   AND entity_id = (SELECT id::text FROM users WHERE email = 'a@example.com'))`).Scan(&statusA, &sessionsA, &rotated); err != nil {
		t.Fatal(err)
	}
	if statusA != "disabled" || sessionsA != 0 || rotated != 1 {
		t.Fatalf("old admin: status=%s sessions=%d admin.rotated rows=%d, want disabled/0/1", statusA, sessionsA, rotated)
	}

	// Re-running with the same login changes nothing (no second audit row).
	_ = newBootstrapped(t, env, "b@example.com", hash)
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'admin.rotated'`).Scan(&rotated); err != nil || rotated != 1 {
		t.Fatalf("admin.rotated rows after rerun = %d (err %v), want 1", rotated, err)
	}

	// Rotating back re-enables a@ and retires b@.
	svcA2 := newBootstrapped(t, env, "a@example.com", hash)
	mustLogin(t, svcA2, "a@example.com", "pw")
	if _, err := svcA2.Authenticate(ctx, tokenB); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("b@ session after rotating back: err = %v", err)
	}
}

func TestIntegrationSessionRejectedForInactiveUserOrTenant(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("auth_inactive"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	hash, _ := crypto.HashPassword("pw", cheap)
	svc := newBootstrapped(t, env, "admin", hash)

	cases := []struct {
		name, disable, enable string
	}{
		{"disabled user", `UPDATE users SET status = 'disabled'`, `UPDATE users SET status = 'active'`},
		{"suspended tenant", `UPDATE tenants SET status = 'suspended'`, `UPDATE tenants SET status = 'active'`},
		{"deleted tenant", `UPDATE tenants SET deleted_at = now()`, `UPDATE tenants SET deleted_at = NULL`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			token := mustLogin(t, svc, "admin", "pw")
			if _, err := svc.Authenticate(ctx, token); err != nil {
				t.Fatalf("before: %v", err)
			}
			pgtest.MustExec(t, admin, c.disable)
			if _, err := svc.Authenticate(ctx, token); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("after %q: err = %v, want ErrUnauthenticated", c.disable, err)
			}
			pgtest.MustExec(t, admin, c.enable)
			if _, err := svc.Authenticate(ctx, token); err != nil {
				t.Fatalf("after re-enabling: %v", err)
			}
		})
	}
}

func TestIntegrationPasswordChangeRevokesSessions(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("auth_pwchange"))
	ctx := context.Background()
	hash1, _ := crypto.HashPassword("first", cheap)
	hash2, _ := crypto.HashPassword("second", cheap)

	svc := newBootstrapped(t, env, "admin", hash1)
	token := mustLogin(t, svc, "admin", "first")

	// Same hash again (a restart): the session survives.
	svc = newBootstrapped(t, env, "admin", hash1)
	if _, err := svc.Authenticate(ctx, token); err != nil {
		t.Fatalf("restart with the same hash revoked the session: %v", err)
	}

	svc = newBootstrapped(t, env, "admin", hash2)
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("session after password change: err = %v, want ErrUnauthenticated", err)
	}
	if _, err := login(t, svc, "admin", "first"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("old password: err = %v", err)
	}
	mustLogin(t, svc, "admin", "second")
}

// Concurrent wrong passwords for one login cannot run more verifications than the limit:
// every attempt is either rejected by the limiter or verified (and then fails).
func TestIntegrationLoginLimitHoldsUnderConcurrency(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("auth_ratelimit"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	hash, _ := crypto.HashPassword("pw", cheap)
	const limit = 10
	svc, err := auth.NewService(env.Pool(t, env.APIURL, 8), auth.Options{
		SessionTTL:    time.Hour,
		IPLimiter:     auth.NewLimiter(1000, 15*time.Minute, 1000, clock.System{}),
		LoginLimiter:  auth.NewLimiter(limit, 15*time.Minute, 1000, clock.System{}),
		HashQueueWait: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.BootstrapAdmin(ctx, "admin", hash); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	var verified, limited, other atomic.Int32
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Login(ctx, auth.LoginInput{Login: "Admin", Password: "wrong", IP: testIP})
			switch {
			case errors.Is(err, auth.ErrInvalidCredentials):
				verified.Add(1)
			case errors.Is(err, auth.ErrRateLimited):
				limited.Add(1)
			default:
				other.Add(1)
			}
		}()
	}
	wg.Wait()
	if verified.Load() > limit || other.Load() != 0 || verified.Load()+limited.Load() != 200 {
		t.Fatalf("verified=%d rate_limited=%d other=%d; want at most %d verifications", verified.Load(), limited.Load(), other.Load(), limit)
	}
	var failures int32
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'auth.login_failed'`).Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if failures != verified.Load() {
		t.Fatalf("audit rows = %d, verifications = %d", failures, verified.Load())
	}
	// The correct password is now rate limited too, for this login.
	if _, err := login(t, svc, "admin", "pw"); !errors.Is(err, auth.ErrRateLimited) {
		t.Fatalf("login after the limit: err = %v", err)
	}
}

// Wrong passwords from one address do not lock the account for everybody else.
func TestIntegrationLoginLockoutIsPerAddress(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("auth_lockout"))
	ctx := context.Background()
	hash, _ := crypto.HashPassword("pw", cheap)
	const limit = 3
	svc, err := auth.NewService(env.Pool(t, env.APIURL, 4), auth.Options{
		SessionTTL:   time.Hour,
		IPLimiter:    auth.NewLimiter(1000, 15*time.Minute, 1000, clock.System{}),
		LoginLimiter: auth.NewLimiter(limit, 15*time.Minute, 1000, clock.System{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.BootstrapAdmin(ctx, "admin", hash); err != nil {
		t.Fatal(err)
	}
	attacker, owner := netip.MustParseAddr("203.0.113.9"), netip.MustParseAddr("198.51.100.7")
	for range limit + 2 {
		_, _ = svc.Login(ctx, auth.LoginInput{Login: "admin", Password: "wrong", IP: attacker})
	}
	if _, err := svc.Login(ctx, auth.LoginInput{Login: "admin", Password: "pw", IP: attacker}); !errors.Is(err, auth.ErrRateLimited) {
		t.Fatalf("the attacking address is not limited: %v", err)
	}
	if _, err := svc.Login(ctx, auth.LoginInput{Login: "admin", Password: "pw", IP: owner}); err != nil {
		t.Fatalf("the owner from another address is locked out: %v", err)
	}
}

// A registered user logs in to their own tenant; a suspended or deleted tenant refuses the login
// with the generic error, and a user without any membership cannot log in.
func TestIntegrationRegisteredLoginBindsToOwnTenant(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("auth_registered"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	hash, _ := crypto.HashPassword("pw", cheap)
	svc := newBootstrapped(t, env, "admin", hash)
	const password = "registered user password"

	reg, err := svc.Register(ctx, auth.RegisterInput{Email: "User@Example.com", Password: password, IP: testIP})
	if err != nil {
		t.Fatal(err)
	}
	if reg.Tenant.Slug == "default" || reg.Role != auth.RoleOwner || reg.User.Login != "user@example.com" {
		t.Fatalf("register result: %+v", reg.SessionInfo)
	}
	res, err := login(t, svc, "USER@example.com", password)
	if err != nil || res.Tenant.ID != reg.Tenant.ID {
		t.Fatalf("login: tenant %v, err %v; want %v", res.Tenant.ID, err, reg.Tenant.ID)
	}
	if p, err := svc.Authenticate(ctx, res.Token); err != nil || p.TenantID != reg.Tenant.ID {
		t.Fatalf("authenticate: %+v %v", p, err)
	}
	if _, err := svc.Register(ctx, auth.RegisterInput{Email: "user@example.COM", Password: password, IP: testIP}); !errors.Is(err, auth.ErrEmailTaken) {
		t.Fatalf("duplicate: err = %v", err)
	}

	// A suspended or deleted tenant is reported only to the right password; a wrong one,
	// or a user without membership, gets the generic failure.
	for _, c := range []struct {
		disable, enable string
		want            error
	}{
		{`UPDATE tenants SET status = 'suspended' WHERE id = $1`, `UPDATE tenants SET status = 'active' WHERE id = $1`, auth.ErrAccountSuspended},
		{`UPDATE tenants SET deleted_at = now() WHERE id = $1`, `UPDATE tenants SET deleted_at = NULL WHERE id = $1`, auth.ErrAccountSuspended},
		{`DELETE FROM memberships WHERE tenant_id = $1`, `INSERT INTO memberships (tenant_id, user_id, role) SELECT $1, id, 'owner' FROM users WHERE email = 'user@example.com'`, auth.ErrInvalidCredentials},
	} {
		if _, err := admin.Exec(ctx, c.disable, reg.Tenant.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := login(t, svc, "user@example.com", password); !errors.Is(err, c.want) {
			t.Fatalf("after %q: err = %v, want %v", c.disable, err, c.want)
		}
		if _, err := login(t, svc, "user@example.com", password+"x"); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("wrong password after %q: err = %v, want ErrInvalidCredentials", c.disable, err)
		}
		if _, err := admin.Exec(ctx, c.enable, reg.Tenant.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := login(t, svc, "user@example.com", password); err != nil {
			t.Fatalf("after %q: %v", c.enable, err)
		}
	}
	// The admin is unaffected.
	if res := mustLogin(t, svc, "admin", "pw"); res == "" {
		t.Fatal("admin login")
	}
}

// A stranger who registered the email that the operator later configures as PRORETA_ADMIN_LOGIN is
// not adopted: BootstrapAdmin fails and writes nothing.
func TestIntegrationBootstrapRefusesRegisteredStranger(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("auth_adopt_refused"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	hash, _ := crypto.HashPassword("pw", cheap)
	svc := newBootstrapped(t, env, "admin", hash)
	adminToken := mustLogin(t, svc, "admin", "pw")
	const strangerPassword = "stranger's own password"
	reg, err := svc.Register(ctx, auth.RegisterInput{Email: "ops@company.example", Password: strangerPassword, IP: testIP})
	if err != nil {
		t.Fatal(err)
	}

	snapshot := func() string {
		t.Helper()
		var s string
		if err := admin.QueryRow(ctx, `SELECT concat_ws('|',
			(SELECT string_agg(email || ':' || status || ':' || coalesce(password_hash, ''), ',' ORDER BY email) FROM users),
			(SELECT string_agg(tenant_id || ':' || user_id || ':' || role, ',' ORDER BY tenant_id, user_id) FROM memberships),
			(SELECT string_agg(id::text, ',' ORDER BY id) FROM sessions),
			(SELECT count(*) FROM audit_log))`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := snapshot()
	other, err := auth.NewService(env.Pool(t, env.APIURL, 2), auth.Options{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	for _, configured := range []string{"ops@company.example", " OPS@Company.example "} {
		if err := other.BootstrapAdmin(ctx, configured, hash); !errors.Is(err, auth.ErrAdminLoginInUse) {
			t.Fatalf("bootstrap with %q: err = %v, want ErrAdminLoginInUse", configured, err)
		}
	}
	if after := snapshot(); after != before {
		t.Fatalf("refused bootstrap changed data:\nbefore %s\nafter  %s", before, after)
	}
	// Both sides keep working as before: the admin in default, the stranger in its own tenant.
	if p, err := svc.Authenticate(ctx, adminToken); err != nil || p.TenantID == reg.Tenant.ID {
		t.Fatalf("admin session: %+v %v", p, err)
	}
	res, err := login(t, svc, "ops@company.example", strangerPassword)
	if err != nil || res.Tenant.ID != reg.Tenant.ID {
		t.Fatalf("stranger login: tenant %v err %v", res.Tenant.ID, err)
	}
}

// A user who is only a member of the default tenant may be adopted as the admin: it becomes the
// owner, every one of its sessions is deleted, the previous admin is retired, and its login lands
// in the default tenant even with an older membership elsewhere (which, once it exists, makes
// the next bootstrap refuse to start).
func TestIntegrationBootstrapAdoptsDefaultOnlyUser(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("auth_adopt_allowed"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	oldHash, _ := crypto.HashPassword("old", cheap)
	opsHash, _ := crypto.HashPassword("ops first", cheap)
	newHash, _ := crypto.HashPassword("ops second", cheap)
	svc := newBootstrapped(t, env, "admin", oldHash)
	oldAdminToken := mustLogin(t, svc, "admin", "old")
	pgtest.MustExec(t, admin,
		`INSERT INTO users (id, email, password_hash) VALUES ('01920000-0000-7000-8000-0000000000e1', 'ops@company.example', '`+opsHash+`')`,
		`INSERT INTO memberships (tenant_id, user_id, role) SELECT id, '01920000-0000-7000-8000-0000000000e1', 'viewer' FROM tenants WHERE slug = 'default'`)
	opsToken := mustLogin(t, svc, "ops@company.example", "ops first")

	// Same hash as stored: the adoption alone must revoke the session.
	svc = newBootstrapped(t, env, "ops@company.example", opsHash)
	for name, token := range map[string]string{"adopted user": opsToken, "previous admin": oldAdminToken} {
		if _, err := svc.Authenticate(ctx, token); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("%s session: err = %v, want ErrUnauthenticated", name, err)
		}
	}
	var role, oldStatus string
	if err := admin.QueryRow(ctx, `SELECT (SELECT m.role FROM memberships m JOIN users u ON u.id = m.user_id WHERE u.email = 'ops@company.example'),
		(SELECT status FROM users WHERE email = 'admin')`).Scan(&role, &oldStatus); err != nil {
		t.Fatal(err)
	}
	if role != auth.RoleOwner || oldStatus != "disabled" {
		t.Fatalf("role=%s previous admin=%s, want owner/disabled", role, oldStatus)
	}
	res, err := login(t, svc, "ops@company.example", "ops first")
	if err != nil || res.Tenant.Slug != "default" {
		t.Fatalf("adopted admin login: %+v %v", res.Tenant, err)
	}

	// An older membership elsewhere does not steer the admin's login away from default.
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name) VALUES ('01920000-0000-7000-8000-0000000000ef', 'elsewhere', 'Elsewhere')`,
		`INSERT INTO memberships (tenant_id, user_id, role, created_at)
		 VALUES ('01920000-0000-7000-8000-0000000000ef', '01920000-0000-7000-8000-0000000000e1', 'owner', now() - interval '1 year')`)
	if res, err := login(t, svc, "ops@company.example", "ops first"); err != nil || res.Tenant.Slug != "default" {
		t.Fatalf("login with an older membership: %+v %v", res.Tenant, err)
	}
	other, err := auth.NewService(env.Pool(t, env.APIURL, 2), auth.Options{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := other.BootstrapAdmin(ctx, "ops@company.example", newHash); !errors.Is(err, auth.ErrAdminLoginInUse) {
		t.Fatalf("bootstrap with a membership elsewhere: err = %v, want ErrAdminLoginInUse", err)
	}
}

// The system admin follows PRORETA_ADMIN_LOGIN: BootstrapAdmin sets both flags for the configured
// admin at every start and clears them for a retired one; a registered user never has them from
// registration, and a platform admin granted later keeps the flag across restarts and rotations.
// Login and Me report both.
func TestIntegrationPlatformAdminFlag(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("auth_platform_admin"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	hash, _ := crypto.HashPassword("pw", cheap)
	flags := func() string {
		t.Helper()
		var s string
		if err := admin.QueryRow(ctx, `SELECT string_agg(email::text || '=' || is_platform_admin::text || '/' || is_system_admin::text, ',' ORDER BY email) FROM users`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	svc := newBootstrapped(t, env, "admin", hash)
	res, err := login(t, svc, "admin", "pw")
	if err != nil || !res.User.IsPlatformAdmin || !res.User.IsSystemAdmin {
		t.Fatalf("admin login: %+v %v", res.User, err)
	}
	p, err := svc.Authenticate(ctx, res.Token)
	if err != nil {
		t.Fatal(err)
	}
	if me, err := svc.Me(ctx, p); err != nil || !me.User.IsPlatformAdmin {
		t.Fatalf("me: %+v %v", me.User, err)
	}
	if ok, err := svc.IsPlatformAdmin(ctx, p.UserID); err != nil || !ok {
		t.Fatalf("IsPlatformAdmin(admin) = %v, %v", ok, err)
	}

	reg, err := svc.Register(ctx, auth.RegisterInput{Email: "user@example.com", Password: "registered user password", IP: testIP})
	if err != nil || reg.User.IsPlatformAdmin {
		t.Fatalf("register: %+v %v", reg.User, err)
	}
	if ok, err := svc.IsPlatformAdmin(ctx, reg.User.ID); err != nil || ok {
		t.Fatalf("IsPlatformAdmin(registered) = %v, %v", ok, err)
	}
	if got := flags(); got != "admin=true/true,user@example.com=false/false" {
		t.Fatalf("flags = %s", got)
	}

	// A manual reset of the flag is repaired at the next start (idempotent otherwise).
	pgtest.MustExec(t, admin, `UPDATE users SET is_platform_admin = false, is_system_admin = false`)
	_ = newBootstrapped(t, env, "admin", hash)
	if got := flags(); got != "admin=true/true,user@example.com=false/false" {
		t.Fatalf("flags after restart = %s", got)
	}

	// A granted admin (the flag is data) survives a restart.
	pgtest.MustExec(t, admin, `UPDATE users SET is_platform_admin = true WHERE email = 'user@example.com'`)
	_ = newBootstrapped(t, env, "admin", hash)
	if got := flags(); got != "admin=true/true,user@example.com=true/false" {
		t.Fatalf("flags after a grant and a restart = %s", got)
	}
	if ok, err := svc.IsPlatformAdmin(ctx, reg.User.ID); err != nil || !ok {
		t.Fatalf("IsPlatformAdmin(granted) = %v, %v", ok, err)
	}

	// Rotating the login moves the system admin; the retired admin loses both flags (and is
	// disabled); the granted admin keeps its flag.
	_ = newBootstrapped(t, env, "root", hash)
	if got := flags(); got != "admin=false/false,root=true/true,user@example.com=true/false" {
		t.Fatalf("flags after rotation = %s", got)
	}
	if ok, err := svc.IsPlatformAdmin(ctx, p.UserID); err != nil || ok {
		t.Fatalf("IsPlatformAdmin(retired admin) = %v, %v", ok, err)
	}
	if ok, err := svc.IsPlatformAdmin(ctx, uuid.New()); err != nil || ok {
		t.Fatalf("IsPlatformAdmin(unknown) = %v, %v", ok, err)
	}
}

// HashPassword uses the admin hash's parameters and the bounded argon2 queue.
func TestIntegrationHashPassword(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("auth_hashpw"))
	hash, _ := crypto.HashPassword("pw", cheap)
	svc := newBootstrapped(t, env, "admin", hash)
	phc, err := svc.HashPassword(context.Background(), "temporary password 123")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := crypto.ParseArgon2idHash(phc)
	if err != nil || parsed.Params != cheap || !parsed.Verify("temporary password 123") {
		t.Fatalf("hash %q: params %+v err %v", phc, parsed, err)
	}
}

// A login whose password was verified before a password change never keeps a session:
// the login is paused between the verification and the session transaction while a
// rotation commits, or while a reset is still in flight in a second transaction.
func TestIntegrationLoginRaceWithPasswordReset(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("auth_login_race"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	hash1, _ := crypto.HashPassword("first", cheap)
	hash2, _ := crypto.HashPassword("second", cheap)
	hash3, _ := crypto.HashPassword("third", cheap)
	svc := newBootstrapped(t, env, "admin", hash1)

	sessions := func() int {
		t.Helper()
		var n int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM sessions AS s JOIN users AS u ON u.id = s.user_id WHERE u.email = 'admin'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// pausedLogin starts a login that stops after its password verification; resume lets it go on.
	pausedLogin := func(password string) (resume func(), done <-chan error) {
		t.Helper()
		paused, cont := make(chan struct{}), make(chan struct{})
		svc.SetAfterVerify(func() { close(paused); <-cont })
		out := make(chan error, 1)
		go func() {
			_, err := svc.Login(ctx, auth.LoginInput{Login: "admin", Password: password, IP: testIP})
			out <- err
		}()
		<-paused
		return func() { close(cont) }, out
	}
	t.Cleanup(func() { svc.SetAfterVerify(func() {}) })

	t.Run("a rotation committed after the verification", func(t *testing.T) {
		resume, done := pausedLogin("first")
		// Another replica rotates the password from the Secret.
		newBootstrapped(t, env, "admin", hash2)
		resume()
		if err := <-done; !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("login verified before the rotation: err = %v, want ErrInvalidCredentials", err)
		}
		if n := sessions(); n != 0 {
			t.Fatalf("sessions = %d, want 0", n)
		}
	})

	t.Run("a reset in flight in a second transaction", func(t *testing.T) {
		resume, done := pausedLogin("second")
		tx, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		// What admin_reset_user_password does: new hash and updated_at, then the sessions go.
		if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = $1, updated_at = now() WHERE email = 'admin'`, hash3); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT auth_revoke_user_sessions(id) FROM users WHERE email = 'admin'`); err != nil {
			t.Fatal(err)
		}
		resume()
		// The session transaction waits for the row lock of the reset.
		waitForLockWait(t, env, "FOR SHARE")
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("login verified before the reset: err = %v, want ErrInvalidCredentials", err)
		}
		if n := sessions(); n != 0 {
			t.Fatalf("sessions = %d, want 0", n)
		}
	})

	t.Run("an unchanged credential still logs in", func(t *testing.T) {
		resume, done := pausedLogin("third")
		resume()
		if err := <-done; err != nil {
			t.Fatalf("login: %v", err)
		}
		if n := sessions(); n != 1 {
			t.Fatalf("sessions = %d, want 1", n)
		}
	})
}

// waitForLockWait waits until a backend of the test database waits for a lock in a query that
// contains fragment.
func waitForLockWait(t *testing.T, env *pgtest.Env, fragment string) {
	t.Helper()
	conn := env.AdminConn(t)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		if err := conn.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND strpos(query, $1) > 0)`, fragment).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no backend waits for a lock in a query with %q", fragment)
}

// An organization suspended between the invitation's lookup and the accept
// transaction is refused, and the invitation stays open.
func TestIntegrationAcceptRefusesTenantSuspendedMeanwhile(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("auth_accept_suspended"))
	ctx := context.Background()
	hash, _ := crypto.HashPassword("admin password", cheap)
	svc := newBootstrapped(t, env, "admin", hash)
	ownerRes, err := svc.Register(ctx, auth.RegisterInput{Email: "owner@example.com", Password: "owner passphrase", IP: testIP})
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := svc.CreateInvitation(ctx, auth.Actor{TenantID: ownerRes.Tenant.ID, UserID: ownerRes.User.ID}, "viewer", "")
	if err != nil {
		t.Fatal(err)
	}
	guest, err := svc.Register(ctx, auth.RegisterInput{Email: "guest@example.com", Password: "guest passphrase", IP: testIP})
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.Authenticate(ctx, guest.Token)
	if err != nil {
		t.Fatal(err)
	}
	admin := env.AdminConn(t)
	svc.SetBeforeJoin(func() {
		pgtest.MustExec(t, admin, `UPDATE tenants SET status = 'suspended' WHERE id = '`+ownerRes.Tenant.ID.String()+`'`)
	})
	if _, err := svc.AcceptInvitation(ctx, p, token, testIP, ""); !errors.Is(err, auth.ErrInvitationInvalid) {
		t.Fatalf("accept into a suspended tenant: %v", err)
	}
	var open, members int
	if err := admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM invitations WHERE accepted_at IS NULL),
		(SELECT count(*) FROM memberships WHERE tenant_id = $1)`, ownerRes.Tenant.ID).Scan(&open, &members); err != nil || open != 1 || members != 1 {
		t.Fatalf("open invitations %d, members %d, %v", open, members, err)
	}
	// The same for a registration through the link.
	if _, err := svc.Register(ctx, auth.RegisterInput{Email: "new@example.com", Password: "new passphrase!", IP: testIP, InviteToken: token}); !errors.Is(err, auth.ErrInvitationInvalid) {
		t.Fatalf("register into a suspended tenant: %v", err)
	}
}

const strongPassword = "a brand new passphrase"

func principalOf(t *testing.T, svc *auth.Service, token string) auth.Principal {
	t.Helper()
	p, err := svc.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	return p
}

func fieldErrorOn(err error, field string) bool {
	var verr *validation.Error
	if !errors.As(err, &verr) {
		return false
	}
	for _, f := range verr.Fields {
		if f.Field == field {
			return true
		}
	}
	return false
}

// A password chosen in the app survives a restart with the same Secret hash; a new Secret hash
// (the documented reset) replaces it.
func TestIntegrationChangePasswordKeepsAdminPasswordAcrossRestarts(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("auth_pwchange_admin"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	secret1, _ := crypto.HashPassword("first", cheap)
	secret2, _ := crypto.HashPassword("second", cheap)

	svc := newBootstrapped(t, env, "admin", secret1)
	current := mustLogin(t, svc, "admin", "first")
	other := mustLogin(t, svc, "admin", "first")

	res, err := svc.ChangePassword(ctx, principalOf(t, svc, current), auth.ChangePasswordInput{Current: "first", New: strongPassword, IP: testIP})
	if err != nil {
		t.Fatalf("change: %v", err)
	}
	if res.Token == "" || res.Token == current || res.CSRFToken == "" || res.User.Login != "admin" || !res.User.IsPlatformAdmin || res.Tenant.Slug != "default" || res.Role != auth.RoleOwner {
		t.Fatalf("answer = %+v", res)
	}
	for name, token := range map[string]string{"the old current session": current, "the other session": other} {
		if _, err := svc.Authenticate(ctx, token); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("%s: err = %v, want ErrUnauthenticated", name, err)
		}
	}
	if _, err := svc.Authenticate(ctx, res.Token); err != nil {
		t.Fatalf("new session: %v", err)
	}
	if _, err := login(t, svc, "admin", "first"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("old password: err = %v", err)
	}
	mustLogin(t, svc, "admin", strongPassword)

	var action, entity, meta string
	if err := admin.QueryRow(ctx, `SELECT action, entity_type, metadata::text FROM audit_log WHERE action = 'auth.password_changed'`).Scan(&action, &entity, &meta); err != nil {
		t.Fatalf("audit row: %v", err)
	}
	if entity != "user" || meta != `{"sessionsRevoked": 2}` {
		t.Fatalf("audit entity=%s metadata=%s", entity, meta)
	}

	// Restarts with the same Secret hash: the chosen password stays and its sessions survive.
	for i := 0; i < 2; i++ {
		svc = newBootstrapped(t, env, "admin", secret1)
		if _, err := svc.Authenticate(ctx, res.Token); err != nil {
			t.Fatalf("session after restart: %v", err)
		}
		mustLogin(t, svc, "admin", strongPassword)
	}
	if _, err := login(t, svc, "admin", "first"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("Secret password after a change: err = %v", err)
	}
	var marker *time.Time
	if err := admin.QueryRow(ctx, `SELECT password_changed_at FROM users WHERE email = 'admin'`).Scan(&marker); err != nil || marker == nil {
		t.Fatalf("password_changed_at = %v (%v), want set", marker, err)
	}

	// The reset: a new Secret hash (regenerated Secrets) replaces the chosen password and ends
	// its sessions.
	svc = newBootstrapped(t, env, "admin", secret2)
	if _, err := svc.Authenticate(ctx, res.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("session after reset: err = %v", err)
	}
	if _, err := login(t, svc, "admin", strongPassword); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("chosen password after reset: err = %v", err)
	}
	mustLogin(t, svc, "admin", "second")
	if err := admin.QueryRow(ctx, `SELECT password_changed_at FROM users WHERE email = 'admin'`).Scan(&marker); err != nil || marker != nil {
		t.Fatalf("password_changed_at after reset = %v (%v), want NULL", marker, err)
	}
	// A password can be chosen again, and survives the same Secret again.
	if _, err := svc.ChangePassword(ctx, principalOf(t, svc, mustLogin(t, svc, "admin", "second")), auth.ChangePasswordInput{Current: "second", New: strongPassword + "!", IP: testIP}); err != nil {
		t.Fatalf("second change: %v", err)
	}
	svc = newBootstrapped(t, env, "admin", secret2)
	mustLogin(t, svc, "admin", strongPassword+"!")
}

func TestIntegrationChangePasswordRules(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("auth_pwchange_rules"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	secret, _ := crypto.HashPassword("first", cheap)
	userHash, _ := crypto.HashPassword("user password 1", cheap)
	svc := newBootstrapped(t, env, "admin", secret)
	pgtest.MustExec(t, admin,
		`INSERT INTO users (id, email, password_hash) VALUES ('01920000-0000-7000-8000-0000000000a1', 'user@example.com', '`+userHash+`')`,
		`INSERT INTO users (id, email) VALUES ('01920000-0000-7000-8000-0000000000a2', 'oauth@example.com')`,
		`INSERT INTO memberships (tenant_id, user_id, role) SELECT t.id, u.id, 'member' FROM tenants t, users u WHERE t.slug = 'default' AND u.email IN ('user@example.com', 'oauth@example.com')`)
	token := mustLogin(t, svc, "user@example.com", "user password 1")
	p := principalOf(t, svc, token)

	for name, tc := range map[string]struct {
		in    auth.ChangePasswordInput
		field string
	}{
		"wrong current": {auth.ChangePasswordInput{Current: "nope", New: strongPassword, IP: testIP}, "currentPassword"},
		"too short":     {auth.ChangePasswordInput{Current: "user password 1", New: "short", IP: testIP}, "newPassword"},
		"too long":      {auth.ChangePasswordInput{Current: "user password 1", New: strings.Repeat("x", 129), IP: testIP}, "newPassword"},
		"the login":     {auth.ChangePasswordInput{Current: "user password 1", New: "USER@example.com", IP: testIP}, "newPassword"},
		"unchanged":     {auth.ChangePasswordInput{Current: "user password 1", New: "user password 1", IP: testIP}, "newPassword"},
	} {
		if _, err := svc.ChangePassword(ctx, p, tc.in); !fieldErrorOn(err, tc.field) {
			t.Errorf("%s: err = %v, want a validation error on %s", name, err, tc.field)
		}
	}
	// Nothing changed and the session is still there.
	if _, err := svc.Authenticate(ctx, token); err != nil {
		t.Fatalf("session after refused changes: %v", err)
	}
	mustLogin(t, svc, "user@example.com", "user password 1")

	oauthP := auth.Principal{SessionID: p.SessionID, UserID: uuid.MustParse("01920000-0000-7000-8000-0000000000a2"), TenantID: p.TenantID}
	if _, err := svc.ChangePassword(ctx, oauthP, auth.ChangePasswordInput{Current: "x", New: strongPassword, IP: testIP}); !errors.Is(err, auth.ErrNoPassword) {
		t.Fatalf("user without a password: err = %v", err)
	}
	// A session that was deleted meanwhile cannot change anything.
	pgtest.MustExec(t, admin, `DELETE FROM sessions WHERE id = '`+p.SessionID.String()+`'`)
	if _, err := svc.ChangePassword(ctx, p, auth.ChangePasswordInput{Current: "user password 1", New: strongPassword, IP: testIP}); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("revoked session: err = %v", err)
	}
	mustLogin(t, svc, "user@example.com", "user password 1")
}

// Wrong current passwords are limited per account and per client address, like logins.
func TestIntegrationChangePasswordRateLimited(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("auth_pwchange_limit"))
	ctx := context.Background()
	secret, _ := crypto.HashPassword("first", cheap)
	pool := env.Pool(t, env.APIURL, 8)
	svc, err := auth.NewService(pool, auth.Options{SessionTTL: time.Hour, LoginLimiter: auth.NewLimiter(3, time.Hour, 100, clock.System{})})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.BootstrapAdmin(ctx, "admin", secret); err != nil {
		t.Fatal(err)
	}
	p := principalOf(t, svc, mustLogin(t, svc, "admin", "first"))
	for i := 0; i < 3; i++ {
		if _, err := svc.ChangePassword(ctx, p, auth.ChangePasswordInput{Current: "nope", New: strongPassword, IP: testIP}); !fieldErrorOn(err, "currentPassword") {
			t.Fatalf("attempt %d: err = %v", i, err)
		}
	}
	// Even the right password is refused until the window passes.
	if _, err := svc.ChangePassword(ctx, p, auth.ChangePasswordInput{Current: "first", New: strongPassword, IP: testIP}); !errors.Is(err, auth.ErrRateLimited) {
		t.Fatalf("after the limit: err = %v, want ErrRateLimited", err)
	}
}

// Password login turned off: only the admin keeps a usable password.
func TestIntegrationChangePasswordWhenPasswordLoginIsOff(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("auth_pwchange_off"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	secret, _ := crypto.HashPassword("first", cheap)
	userHash, _ := crypto.HashPassword("user password 1", cheap)
	svc, err := auth.NewService(env.Pool(t, env.APIURL, 8), auth.Options{SessionTTL: time.Hour, PasswordLoginDisabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.BootstrapAdmin(ctx, "admin", secret); err != nil {
		t.Fatal(err)
	}
	pgtest.MustExec(t, admin,
		`INSERT INTO users (id, email, password_hash) VALUES ('01920000-0000-7000-8000-0000000000a1', 'user@example.com', '`+userHash+`')`)
	p := principalOf(t, svc, mustLogin(t, svc, "admin", "first"))
	userP := auth.Principal{SessionID: p.SessionID, UserID: uuid.MustParse("01920000-0000-7000-8000-0000000000a1"), TenantID: p.TenantID}
	if _, err := svc.ChangePassword(ctx, userP, auth.ChangePasswordInput{Current: "user password 1", New: strongPassword, IP: testIP}); !errors.Is(err, auth.ErrPasswordLoginDisabled) {
		t.Fatalf("user: err = %v", err)
	}
	if _, err := svc.ChangePassword(ctx, p, auth.ChangePasswordInput{Current: "first", New: strongPassword, IP: testIP}); err != nil {
		t.Fatalf("admin: %v", err)
	}
}

// An adopted user takes the Secret's hash even after it changed its password in the app.
func TestIntegrationBootstrapAdoptionOverridesChosenPassword(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("auth_pwchange_adopt"))
	admin := env.AdminConn(t)
	secret, _ := crypto.HashPassword("first", cheap)
	opsHash, _ := crypto.HashPassword("ops chosen", cheap)
	newBootstrapped(t, env, "admin", secret)
	pgtest.MustExec(t, admin,
		`INSERT INTO users (id, email, password_hash, password_changed_at) VALUES ('01920000-0000-7000-8000-0000000000e1', 'ops@company.example', '`+opsHash+`', now())`,
		`INSERT INTO memberships (tenant_id, user_id, role) SELECT id, '01920000-0000-7000-8000-0000000000e1', 'viewer' FROM tenants WHERE slug = 'default'`)
	ops, _ := crypto.HashPassword("ops from secret", cheap)
	svc := newBootstrapped(t, env, "ops@company.example", ops)
	mustLogin(t, svc, "ops@company.example", "ops from secret")
	var marker *time.Time
	if err := admin.QueryRow(context.Background(), `SELECT password_changed_at FROM users WHERE email = 'ops@company.example'`).Scan(&marker); err != nil || marker != nil {
		t.Fatalf("marker = %v (%v), want NULL", marker, err)
	}
}

func mustChangeFlag(t *testing.T, env *pgtest.Env, login string) bool {
	t.Helper()
	var flag bool
	if err := env.AdminConn(t).QueryRow(context.Background(), `SELECT password_must_change FROM users WHERE email = $1`, login).Scan(&flag); err != nil {
		t.Fatalf("password_must_change of %s: %v", login, err)
	}
	return flag
}

// The password from the Secret has to be replaced at the first sign-in: set by the first
// bootstrap and by a new Secret hash, kept by restarts with the same one, cleared by the change.
func TestIntegrationMustChangePasswordFromSecret(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("auth_mustchange"))
	ctx := context.Background()
	secret1, _ := crypto.HashPassword("first", cheap)
	secret2, _ := crypto.HashPassword("second", cheap)

	svc := newBootstrapped(t, env, "admin", secret1)
	if !mustChangeFlag(t, env, "admin") {
		t.Fatal("a new admin must change the password")
	}
	res, err := login(t, svc, "admin", "first")
	if err != nil || !res.User.MustChangePassword {
		t.Fatalf("login: %+v %v, want mustChangePassword", res.User, err)
	}
	p := principalOf(t, svc, res.Token)
	if !p.MustChangePassword {
		t.Fatal("principal does not carry the flag")
	}
	if info, err := svc.Me(ctx, p); err != nil || !info.User.MustChangePassword {
		t.Fatalf("me: %+v %v", info.User, err)
	}

	// Restarts with the same Secret keep the pending change.
	svc = newBootstrapped(t, env, "admin", secret1)
	if !mustChangeFlag(t, env, "admin") {
		t.Fatal("a restart cleared the pending change")
	}

	// The new password must differ from the current one, and the flag stays until it does.
	if _, err := svc.ChangePassword(ctx, principalOf(t, svc, mustLogin(t, svc, "admin", "first")), auth.ChangePasswordInput{Current: "first", New: "first", IP: testIP}); !fieldErrorOn(err, "newPassword") {
		t.Fatalf("same password: err = %v", err)
	}
	if !mustChangeFlag(t, env, "admin") {
		t.Fatal("a refused change cleared the flag")
	}
	if _, err := svc.ChangePassword(ctx, principalOf(t, svc, mustLogin(t, svc, "admin", "first")), auth.ChangePasswordInput{Current: "wrong", New: strongPassword, IP: testIP}); !fieldErrorOn(err, "currentPassword") {
		t.Fatalf("wrong current: err = %v", err)
	}
	if !mustChangeFlag(t, env, "admin") {
		t.Fatal("a wrong current password cleared the flag")
	}
	changed, err := svc.ChangePassword(ctx, principalOf(t, svc, mustLogin(t, svc, "admin", "first")), auth.ChangePasswordInput{Current: "first", New: strongPassword, IP: testIP})
	if err != nil || changed.User.MustChangePassword {
		t.Fatalf("change: %+v %v", changed.User, err)
	}
	if mustChangeFlag(t, env, "admin") || principalOf(t, svc, changed.Token).MustChangePassword {
		t.Fatal("the change did not clear the flag")
	}
	newBootstrapped(t, env, "admin", secret1)
	if mustChangeFlag(t, env, "admin") {
		t.Fatal("a restart with the same Secret asked for another change")
	}

	// A new Secret hash is a reset: the temporary password must be replaced again.
	svc = newBootstrapped(t, env, "admin", secret2)
	if !mustChangeFlag(t, env, "admin") {
		t.Fatal("a new Secret hash did not ask for a change")
	}
	mustLogin(t, svc, "admin", "second")

	// An installation that predates the flag keeps its admin as it is: the same hash again does
	// not ask for a change.
	pgtest.MustExec(t, env.AdminConn(t), `UPDATE users SET password_must_change = false, configured_password_hash = NULL WHERE email = 'admin'`)
	newBootstrapped(t, env, "admin", secret2)
	if mustChangeFlag(t, env, "admin") {
		t.Fatal("an upgrade with the same Secret asked for a change")
	}
}
