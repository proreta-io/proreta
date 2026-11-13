package auth

import "testing"

func TestCanClaim(t *testing.T) {
	base := claimCandidate{Active: true}
	if !canClaim(base) {
		t.Fatal("an active, unverified account without identity must be claimable")
	}
	for name, c := range map[string]claimCandidate{
		"verified email": {Active: true, EmailVerified: true},
		"has identity":   {Active: true, HasIdentity: true},
		"platform admin": {Active: true, IsPlatformAdmin: true},
		"system admin":   {Active: true, IsSystemAdmin: true},
		"break-glass":    {Active: true, IsAdminLogin: true},
		"disabled user":  {},
		"everything set": {EmailVerified: true, HasIdentity: true, IsPlatformAdmin: true, IsSystemAdmin: true, IsAdminLogin: true},
	} {
		if canClaim(c) {
			t.Errorf("%s: claimable, want never", name)
		}
	}
}
