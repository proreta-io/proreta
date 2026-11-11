package api

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/authz"

	"github.com/proreta-io/proreta/backend/internal/api/gen"
)

// publicOperations need no session (compared case-insensitively: the embedded spec has the
// operation ids as Go names). Everything else in the contract must answer 401 without one.
// register and getPublicConfig were added deliberately with self-service registration.
// The heartbeat ping is public on purpose: Alertmanager and cron jobs call it without a session,
// the secret token in its URL is the credential.
// lookupInvitation is public: the invitation page asks what a link offers before the visitor has
// an account; the token in the body is the credential.
// The OAuth operations are public: the login page lists the providers, a sign-in starts and
// finishes without a session (the start of a link checks the session itself).
var publicOperations = []string{"getHealth", "getPublicConfig", "login", "register", "pingHeartbeat", "pingHeartbeatPost", "lookupInvitation",
	"getAuthProviders", "startOAuth", "oauthCallback", "verifyEmail", "requestPasswordReset", "resetPassword"}

// csrfExempt are the unsafe operations without a session, hence without a CSRF token. The POST
// form of the heartbeat ping is one: it has no session to carry a CSRF token, and it changes only
// the ping time of the heartbeat whose secret token is in the URL.
// startOAuth has no session for a sign-in; with intent link it checks the CSRF token itself.
// verifyEmail is public: the token in the emailed link is the credential and the visitor may have no
// session on this browser.
var csrfExempt = []string{"login", "register", "pingHeartbeatPost", "lookupInvitation", "startOAuth", "verifyEmail", "requestPasswordReset", "resetPassword"}

func isCSRFExempt(operationID string) bool {
	return slices.ContainsFunc(csrfExempt, func(id string) bool { return strings.EqualFold(id, operationID) })
}

type specOperation struct {
	method, path, id string
	op               *openapi3.Operation
}

func specOperations(t *testing.T) (*openapi3.T, []specOperation) {
	t.Helper()
	spec, err := gen.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	var ops []specOperation
	for _, path := range spec.Paths.InMatchingOrder() {
		for method, op := range spec.Paths.Value(path).Operations() {
			ops = append(ops, specOperation{method: method, path: path, id: op.OperationID, op: op})
		}
	}
	if len(ops) < 10 {
		t.Fatalf("only %d operations in the spec", len(ops))
	}
	return spec, ops
}

// concretePath fills path parameters with a syntactically valid value.
func concretePath(p string) string {
	// A well-formed heartbeat token: a malformed one is answered 404 before any other work.
	p = strings.ReplaceAll(p, "{token}", strings.Repeat("A", 43))
	return strings.NewReplacer(
		"{monitorId}", "0192b9c4-7c5e-7000-8000-000000000001",
		"{organizationId}", "0192b9c4-7c5e-7000-8000-000000000002",
		"{userId}", "0192b9c4-7c5e-7000-8000-000000000003",
		"{channelId}", "0192b9c4-7c5e-7000-8000-000000000004",
		"{invitationId}", "0192b9c4-7c5e-7000-8000-000000000005",
	).Replace(p)
}

// adminOperations are the platform admin operations. Every path under /api/v1/admin/ must be
// one of them, tagged admin, marked x-experimental and need a session; the integration test
// TestIntegrationAdminOperationsRefuseNonAdmins checks each answers 403 to a normal user.
var adminOperations = []string{"adminGetStats", "adminListOrganizations", "adminGetOrganization", "adminUpdateOrganization",
	"adminDeleteOrganization", "adminListOrganizationUsers", "adminResetUserPassword", "adminGetEmailSettings", "adminUpdateEmailSettings",
	"adminDeleteEmailSettings", "adminUpdateEmailFeatures", "adminSendTestEmail",
	"adminGetRegistration", "adminUpdateRegistration",
	"adminListAllUsers", "adminGetUser", "adminDisableUser", "adminEnableUser", "adminRevokeUserSessions"}

func TestSpecAdminOperations(t *testing.T) {
	spec, ops := specOperations(t)
	seen := 0
	for _, o := range ops {
		isAdminPath := strings.HasPrefix(o.path, adminPathPrefix)
		isAdminOp := slices.ContainsFunc(adminOperations, func(id string) bool { return strings.EqualFold(id, o.id) })
		if isAdminPath != isAdminOp {
			t.Errorf("%s %s (%s): admin path %v but listed as admin operation %v", o.method, o.path, o.id, isAdminPath, isAdminOp)
		}
		if !isAdminPath {
			continue
		}
		seen++
		if !slices.Equal(o.op.Tags, []string{"admin"}) {
			t.Errorf("%s %s: tags %v, want [admin]", o.method, o.path, o.op.Tags)
		}
		security := spec.Security
		if o.op.Security != nil {
			security = *o.op.Security
		}
		if len(security) == 0 {
			t.Errorf("%s %s: no security requirement", o.method, o.path)
		}
		if _, ok := o.op.Responses.Map()["403"]; !ok {
			t.Errorf("%s %s: 403 not documented", o.method, o.path)
		}
		if o.op.Extensions["x-experimental"] != true {
			t.Errorf("%s %s: not marked x-experimental: true", o.method, o.path)
		}
	}
	if seen != len(adminOperations) {
		t.Fatalf("%d admin operations in the spec, want %d", seen, len(adminOperations))
	}
}

// Without a session every admin operation is 401, before the platform-admin check.
func TestAdminOperationsNeedSession(t *testing.T) {
	h := newTestHandler(t)
	_, ops := specOperations(t)
	for _, o := range ops {
		if !strings.HasPrefix(o.path, adminPathPrefix) {
			continue
		}
		hdr := map[string]string{"X-CSRF-Token": "x", "Content-Type": "application/json"}
		body := ""
		if o.method == http.MethodPatch {
			body = `{"status":"suspended"}`
		}
		rec, p := do(t, h, o.method, concretePath(o.path), body, hdr)
		if rec.Code != http.StatusUnauthorized || p.Type != problemUnauthenticated {
			t.Errorf("%s %s: code=%d problem=%+v, want 401", o.method, o.path, rec.Code, p)
		}
	}
}

func TestSpecValidates(t *testing.T) {
	spec, _ := specOperations(t)
	if err := spec.Validate(context.Background()); err != nil {
		t.Fatalf("api/openapi.yaml is not a valid OpenAPI document: %v", err)
	}
}

// Every unsafe operation except login and registration must declare the CSRF token, both as a security
// requirement (enforced by the validator) and as a required header parameter (documented for
// the generated client).
func TestSpecUnsafeOperationsRequireCSRF(t *testing.T) {
	spec, ops := specOperations(t)
	for _, o := range ops {
		if !isUnsafe(o.method) || isCSRFExempt(o.id) {
			continue
		}
		security := spec.Security
		if o.op.Security != nil {
			security = *o.op.Security
		}
		if len(security) == 0 {
			t.Errorf("%s %s (%s): no security requirement", o.method, o.path, o.id)
		}
		for _, req := range security {
			_, cookie := req["cookieAuth"]
			_, csrf := req["csrfToken"]
			if !cookie || !csrf {
				t.Errorf("%s %s (%s): security requirement %v lacks cookieAuth+csrfToken", o.method, o.path, o.id, req)
			}
		}
		p := o.op.Parameters.GetByInAndName(openapi3.ParameterInHeader, csrfHeader)
		if p == nil || !p.Required {
			t.Errorf("%s %s (%s): missing required %s header parameter", o.method, o.path, o.id, csrfHeader)
		}
	}
}

func TestEveryOperationExceptPublicNeedsSession(t *testing.T) {
	h := newTestHandler(t)
	_, ops := specOperations(t)
	for _, o := range ops {
		rec, p := do(t, h, o.method, concretePath(o.path), "", nil)
		if slices.ContainsFunc(publicOperations, func(id string) bool { return strings.EqualFold(id, o.id) }) {
			if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusNotFound {
				t.Errorf("%s %s (%s) is public but answered %d", o.method, o.path, o.id, rec.Code)
			}
			continue
		}
		if rec.Code != http.StatusUnauthorized || p.Type != problemUnauthenticated {
			t.Errorf("%s %s (%s): code=%d problem=%+v, want 401", o.method, o.path, o.id, rec.Code, p)
		}
	}
}

// The authorization table of internal/authz lists exactly the operations of the spec (deny by
// default makes a missing one unusable, an extra one is a stale entry), and its public operations
// are exactly the ones without a session.
func TestAuthzTableCoversSpec(t *testing.T) {
	_, ops := specOperations(t)
	inSpec := map[string]bool{}
	for _, o := range ops {
		id := strings.ToLower(o.id)
		inSpec[id] = true
		min, ok := authz.MinRole(o.id)
		if !ok {
			t.Errorf("%s %s (%s) is not in the authorization table", o.method, o.path, o.id)
			continue
		}
		public := slices.ContainsFunc(publicOperations, func(p string) bool { return strings.EqualFold(p, o.id) })
		if public != (min == authz.Public) {
			t.Errorf("%s: public in the contract test %v, in the table %v", o.id, public, min == authz.Public)
		}
		if strings.HasPrefix(o.path, adminPathPrefix) && min != authz.Viewer {
			t.Errorf("%s: platform admin operations are decided by the flag, want minimum role viewer, got %s", o.id, min)
		}
	}
	for _, id := range authz.Operations() {
		if !inSpec[id] {
			t.Errorf("authorization table lists %q, which is not in the spec", id)
		}
	}
}

// While the user has to change the password, the authorization middleware lets through only
// the session basics and the change itself; every other operation of the spec, public ones
// aside, is refused before its handler runs.
func TestAuthorizeRefusesEverythingButPasswordChange(t *testing.T) {
	s := &server{log: discard()}
	_, ops := specOperations(t)
	allowed := []string{"getMe", "logout", "changePassword"}
	var passed []string
	for _, o := range ops {
		called := false
		h := s.authorize(func(context.Context, http.ResponseWriter, *http.Request, any) (any, error) {
			called = true
			return nil, nil
		}, o.id)
		slot := &sessionSlot{}
		slot.once.Do(func() {
			slot.principal = auth.Principal{Role: string(authz.Owner), MustChangePassword: true}
		})
		r := httptest.NewRequest(o.method, o.path, nil)
		r = r.WithContext(context.WithValue(r.Context(), slotKey{}, slot))
		_, err := h(r.Context(), httptest.NewRecorder(), r, nil)
		public := slices.ContainsFunc(publicOperations, func(id string) bool { return strings.EqualFold(id, o.id) })
		want := slices.ContainsFunc(allowed, func(id string) bool { return strings.EqualFold(id, o.id) })
		switch {
		case public:
			if !called || err != nil {
				t.Errorf("%s: public operation refused: called=%v err=%v", o.id, called, err)
			}
		case want:
			passed = append(passed, o.id)
			if !called || err != nil {
				t.Errorf("%s: refused while a password change is required: called=%v err=%v", o.id, called, err)
			}
		default:
			if called || !errors.Is(err, errPasswordChangeRequired) {
				t.Errorf("%s %s (%s): called=%v err=%v, want password-change-required", o.method, o.path, o.id, called, err)
			}
		}
	}
	if len(passed) != len(allowed) {
		t.Errorf("operations allowed: %v, want %v", passed, allowed)
	}
}

// An operation the table does not know is refused even with a session.
func TestAuthorizeDeniesUnknownOperation(t *testing.T) {
	s := &server{log: discard()}
	called := false
	h := s.authorize(func(context.Context, http.ResponseWriter, *http.Request, any) (any, error) {
		called = true
		return nil, nil
	}, "NoSuchOperation")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	_, err := h(context.Background(), httptest.NewRecorder(), r, nil)
	var re *roleError
	if called || !errors.As(err, &re) {
		t.Fatalf("unknown operation: called=%v err=%v", called, err)
	}
}

// Every problem type errors.go can emit is documented in the ProblemType schema of the spec, and
// the schema lists no type the code does not emit.
func TestSpecDocumentsEveryProblemType(t *testing.T) {
	spec, _ := specOperations(t)
	ref := spec.Components.Schemas["ProblemType"]
	if ref == nil || ref.Value == nil {
		t.Fatal("no ProblemType schema")
	}
	doc := ref.Value.Description
	f, err := parser.ParseFile(token.NewFileSet(), "errors.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var emitted []string
	ast.Inspect(f, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if v, err := strconv.Unquote(lit.Value); err == nil && strings.HasPrefix(v, "urn:proreta:problem:") {
				emitted = append(emitted, v)
			}
		}
		return true
	})
	if len(emitted) < 20 {
		t.Fatalf("only %d problem types found in errors.go", len(emitted))
	}
	for _, typ := range emitted {
		if !strings.Contains(doc, "`"+typ+"`") {
			t.Errorf("%s is emitted but not documented in ProblemType", typ)
		}
	}
	for _, field := range strings.Fields(doc) {
		typ := strings.Trim(field, "`|")
		if strings.HasPrefix(typ, "urn:proreta:problem:") && !slices.Contains(emitted, typ) {
			t.Errorf("ProblemType documents %s, which errors.go does not emit", typ)
		}
	}
}
