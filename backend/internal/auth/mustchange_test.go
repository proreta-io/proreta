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
		want  bool
	}{
		{"password login on, flag", on, true, "u@example.com", true},
		{"password login on, no flag", on, false, "u@example.com", false},
		{"password login off, flag, user", off, true, "u@example.com", false},
		{"password login off, flag, admin", off, true, "Admin", true},
		{"password login off, no flag, admin", off, false, "admin", false},
	} {
		if got := c.s.mustChange(c.flag, c.login); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
