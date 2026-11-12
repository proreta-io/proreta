package monitors

import (
	"slices"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/platform/validation"
)

func TestHostMovedProblems(t *testing.T) {
	secret := "s3cret"
	keep, body := HeaderInput{Name: "Authorization"}, "payload"
	sent := HeaderInput{Name: "Authorization", Value: &secret}
	stored := httpConfig{HeaderNames: []string{"Authorization"}, BodySet: true}
	cases := []struct {
		name string
		from string
		to   string
		cfg  httpConfig
		h    HTTPSettings
		want []string
	}{
		{"same host, nothing sent", "https://a.example/x", "https://A.example/y", stored, HTTPSettings{}, nil},
		{"other host, nothing sent", "https://a.example", "https://evil.example", stored, HTTPSettings{}, []string{fieldHeaders, fieldBody}},
		{"other host, header kept by name", "https://a.example", "https://evil.example", stored,
			HTTPSettings{Headers: &[]HeaderInput{keep}, Body: &body}, []string{fieldHeaders + "[0].value"}},
		{"other host, everything sent again", "https://a.example", "https://evil.example", stored,
			HTTPSettings{Headers: &[]HeaderInput{sent}, Body: &body}, nil},
		{"other host, values removed", "https://a.example", "https://evil.example", stored,
			HTTPSettings{Headers: &[]HeaderInput{}, Body: new(string)}, nil},
		{"other host, nothing stored", "https://a.example", "https://evil.example", httpConfig{}, HTTPSettings{}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.cfg.URL = c.to
			var verr validation.Error
			hostMovedProblems(&verr, c.from, c.cfg, &c.h)
			var got []string
			for _, f := range verr.Fields {
				got = append(got, f.Field)
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("fields = %v, want %v", got, c.want)
			}
		})
	}
}
