package config

import (
	"fmt"
	"strings"
	"testing"
)

func smtpEnv(t *testing.T, extra map[string]string) map[string]string {
	e := apiEnv(t)
	for k, v := range extra {
		e[k] = v
	}
	return e
}

func TestSMTPOffByDefault(t *testing.T) {
	cfg, err := LoadAPI(env(apiEnv(t)))
	if err != nil || cfg.SMTP.Enabled() {
		t.Fatalf("api: enabled=%v err=%v", cfg.SMTP.Enabled(), err)
	}
}

func TestSMTPJobs(t *testing.T) {
	e := apiEnv(t)
	e["PRORETA_SMTP_HOST"] = "smtp.example.com"
	e["PRORETA_SMTP_FROM"] = "Proreta <proreta@example.com>"
	e["PRORETA_SMTP_USERNAME"] = "user"
	e["PRORETA_SMTP_PASSWORD"] = "hunter2-secret"
	cfg, err := LoadJobs(env(e))
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.SMTP
	if !s.Enabled() || s.Port != 587 || s.TLS != SMTPStartTLS || s.FromAddress != "proreta@example.com" || s.Password.Value() != "hunter2-secret" {
		t.Fatalf("smtp = %+v", s)
	}
	if out := fmt.Sprintf("%v %+v %#v %s", cfg, cfg, cfg, s); strings.Contains(out, "hunter2") {
		t.Fatal("SMTP password printed")
	}

	e["PRORETA_SMTP_TLS"] = "tls"
	if cfg, err = LoadJobs(env(e)); err != nil || cfg.SMTP.Port != 465 {
		t.Fatalf("tls: port=%d err=%v", cfg.SMTP.Port, err)
	}
	e["PRORETA_SMTP_PORT"] = "2465"
	if cfg, err = LoadJobs(env(e)); err != nil || cfg.SMTP.Port != 2465 {
		t.Fatalf("port: %d %v", cfg.SMTP.Port, err)
	}
}

func TestSMTPApiIgnoresLogin(t *testing.T) {
	cfg, err := LoadAPI(env(smtpEnv(t, map[string]string{
		"PRORETA_SMTP_HOST": "smtp.example.com", "PRORETA_SMTP_FROM": "proreta@example.com", "PRORETA_SMTP_USERNAME": "user",
	})))
	if err != nil || !cfg.SMTP.Enabled() || cfg.SMTP.Username != "" {
		t.Fatalf("api: %+v %v", cfg.SMTP, err)
	}
}

func TestSMTPInvalid(t *testing.T) {
	base := map[string]string{"PRORETA_SMTP_HOST": "smtp.example.com", "PRORETA_SMTP_FROM": "proreta@example.com"}
	cases := map[string]struct {
		set  map[string]string
		want string
	}{
		"no host":               {map[string]string{"PRORETA_SMTP_HOST": "", "PRORETA_SMTP_FROM": "proreta@example.com"}, "PRORETA_SMTP_FROM: is set but PRORETA_SMTP_HOST is not"},
		"no from":               {map[string]string{"PRORETA_SMTP_FROM": ""}, "PRORETA_SMTP_FROM: is required"},
		"bad from":              {map[string]string{"PRORETA_SMTP_FROM": "not an address"}, "PRORETA_SMTP_FROM"},
		"from with newline":     {map[string]string{"PRORETA_SMTP_FROM": "a@example.com\r\nBcc: x@example.com"}, "PRORETA_SMTP_FROM"},
		"bad mode":              {map[string]string{"PRORETA_SMTP_TLS": "ssl"}, "PRORETA_SMTP_TLS: must be one of"},
		"plain to remote":       {map[string]string{"PRORETA_SMTP_TLS": "none"}, "none is allowed only for localhost"},
		"bad port":              {map[string]string{"PRORETA_SMTP_PORT": "0"}, "PRORETA_SMTP_PORT"},
		"host with port":        {map[string]string{"PRORETA_SMTP_HOST": "smtp.example.com:587"}, "without a port"},
		"user without password": {map[string]string{"PRORETA_SMTP_USERNAME": "u"}, "must be set together"},
		"password without user": {map[string]string{"PRORETA_SMTP_PASSWORD": "p4ss-secret"}, "must be set together"},
		"login without TLS":     {map[string]string{"PRORETA_SMTP_HOST": "localhost", "PRORETA_SMTP_TLS": "none", "PRORETA_SMTP_USERNAME": "u", "PRORETA_SMTP_PASSWORD": "p4ss-secret"}, "never sent without TLS"},
		"password with newline": {map[string]string{"PRORETA_SMTP_USERNAME": "u", "PRORETA_SMTP_PASSWORD": "a\nb-secret"}, "control characters"},
	}
	for name, c := range cases {
		e := apiEnv(t)
		for k, v := range base {
			e[k] = v
		}
		for k, v := range c.set {
			e[k] = v
		}
		_, err := LoadJobs(env(e))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
		if err != nil && strings.Contains(err.Error(), "-secret") {
			t.Errorf("%s: error echoes the password", name)
		}
	}
}

func TestSMTPLocalhostPlain(t *testing.T) {
	for _, h := range []string{"localhost", "127.0.0.1", "::1"} {
		e := apiEnv(t)
		e["PRORETA_SMTP_HOST"], e["PRORETA_SMTP_FROM"], e["PRORETA_SMTP_TLS"] = h, "proreta@example.com", "none"
		cfg, err := LoadJobs(env(e))
		if err != nil || cfg.SMTP.Port != 25 {
			t.Errorf("%s: %+v %v", h, cfg.SMTP, err)
		}
	}
}
