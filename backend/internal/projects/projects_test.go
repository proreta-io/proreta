package projects

import (
	"strings"
	"testing"
)

func TestValidateName(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"Web", "Web", true},
		{"  Web  ", "Web", true},
		{strings.Repeat("ж", 60), strings.Repeat("ж", 60), true},
		{"", "", false},
		{"   ", "", false},
		{strings.Repeat("ж", 61), strings.Repeat("ж", 61), false},
	} {
		got, msg := ValidateName(tc.in)
		if got != tc.want || (msg == "") != tc.ok {
			t.Errorf("ValidateName(%q) = %q, %q; want %q, ok=%t", tc.in, got, msg, tc.want, tc.ok)
		}
	}
}

func TestValidateSlug(t *testing.T) {
	for slug, ok := range map[string]bool{
		"web": true, "web-2": true, "a": true, strings.Repeat("a", 63): true,
		"": false, "-web": false, "Web": false, "we b": false, "wéb": false, "we_b": false,
		strings.Repeat("a", 64): false,
	} {
		if msg := ValidateSlug(slug); (msg == "") != ok {
			t.Errorf("ValidateSlug(%q) = %q; want ok=%t", slug, msg, ok)
		}
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Web App!": "web-app", "  --  ": "project", "": "project", "Ünï": "n",
	} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q; want %q", in, got, want)
		}
	}
}
