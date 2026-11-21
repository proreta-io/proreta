package auth

import "testing"

func TestMustChange(t *testing.T) {
	on := &Service{}
	off := &Service{passwordLoginDisabled: true, adminLogin: "admin"}
	for _, c := range []struct {
		name  string
		s     *Service
		flag  bool
		login string
		pw    bool
		want  bool
	}{
		{"password login on, flag", on, true, "u@example.com", true, true},
		{"password login on, no flag", on, false, "u@example.com", true, false},
		{"password login off, flag, user", off, true, "u@example.com", true, false},
		{"password login off, flag, admin", off, true, "Admin", true, true},
		{"password login off, no flag, admin", off, false, "admin", true, false},
		{"provider session, flag", on, true, "u@example.com", false, false},
		{"provider session, flag, admin with password login off", off, true, "admin", false, false},
	} {
		if got := c.s.mustChange(c.flag, c.login, c.pw); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
