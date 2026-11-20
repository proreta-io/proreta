package auth

import "testing"

func TestCanLinkByEmail(t *testing.T) {
	// The account's own email verification is not an input: verified or not, an active account
	// without identity is linked (the provider's email is verified before this is reached).
	if !canLinkByEmail(linkCandidate{Active: true}) {
		t.Fatal("an active account without identity must be linkable")
	}
	for name, c := range map[string]linkCandidate{
		"has identity":   {Active: true, HasIdentity: true},
		"platform admin": {Active: true, IsPlatformAdmin: true},
		"system admin":   {Active: true, IsSystemAdmin: true},
		"break-glass":    {Active: true, IsAdminLogin: true},
		"disabled user":  {},
		"everything set": {HasIdentity: true, IsPlatformAdmin: true, IsSystemAdmin: true, IsAdminLogin: true},
	} {
		if canLinkByEmail(c) {
			t.Errorf("%s: linkable, want never", name)
		}
	}
}
