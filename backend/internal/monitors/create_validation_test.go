package monitors_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/monitors"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
)

func ptr[T any](v T) *T { return &v }

func fieldsOf(err error) string {
	var verr *validation.Error
	if !errors.As(err, &verr) {
		return "<" + errString(err) + ">"
	}
	out := make([]string, len(verr.Fields))
	for i, f := range verr.Fields {
		out[i] = f.Field
	}
	return strings.Join(out, ",")
}

func errString(err error) string {
	if err == nil {
		return "nil"
	}
	return err.Error()
}

// Every case here is refused before the database is touched, so no pool is needed. The order of
// the fields is part of the answer the API gives.
func TestCreateValidationFieldsAndOrder(t *testing.T) {
	svc := monitors.NewService(nil, nil, nil)
	actor := monitors.Actor{TenantID: uuid.New(), UserID: uuid.New(), Access: authz.Actor{Role: authz.Owner}}
	okURL := ptr("https://example.com/health")
	spec := uuid.New()
	cases := []struct {
		name string
		in   monitors.CreateInput
		want string
	}{
		{"empty input is an http monitor without settings", monitors.CreateInput{}, "name,http"},
		{"http settings without a URL", monitors.CreateInput{Name: "m", HTTP: &monitors.HTTPSettings{}}, "http.url"},
		{"other types' settings on an http monitor", monitors.CreateInput{Name: "m", Heartbeat: &monitors.HeartbeatSettings{}, TCP: &monitors.TCPSettings{}}, "heartbeat,tcp,http"},
		{"explicit http type behaves the same", monitors.CreateInput{Type: monitors.TypeHTTP, Name: "m"}, "http"},
		{"bad name and URL", monitors.CreateInput{Name: " ", HTTP: &monitors.HTTPSettings{URL: ptr("http://example.com")}}, "name,http.url"},
		{"every http field wrong", monitors.CreateInput{Name: "m", HTTP: &monitors.HTTPSettings{
			URL: ptr("ftp://x"), IntervalSeconds: ptr(30), TimeoutSeconds: ptr(5), ExpectedStatus: ptr("abc"), FailureThreshold: ptr(9), Method: ptr("TRACE"),
		}}, "http.url,http.intervalSeconds,http.timeoutSeconds,http.expectedStatus,http.failureThreshold,http.method"},
		{"the fixed interval and timeout may be sent as they are", monitors.CreateInput{Name: "m", HTTP: &monitors.HTTPSettings{
			URL: okURL, IntervalSeconds: ptr(monitors.IntervalSeconds), TimeoutSeconds: ptr(monitors.TimeoutSeconds), FailureThreshold: ptr(0),
		}}, "http.failureThreshold"},
		// Without the request keyring the body is also "not available", so both fields are reported.
		{"a body on GET", monitors.CreateInput{Name: "m", HTTP: &monitors.HTTPSettings{URL: okURL, Body: ptr("x")}}, "http.body,http.headers"},
		{"headers need the request keyring", monitors.CreateInput{Name: "m", HTTP: &monitors.HTTPSettings{URL: okURL,
			Headers: &[]monitors.HeaderInput{{Name: "X-Token", Value: ptr("v")}}}}, "http.headers"},
		{"a header without a value on create", monitors.CreateInput{Name: "m", HTTP: &monitors.HTTPSettings{URL: okURL,
			Headers: &[]monitors.HeaderInput{{Name: "X-Token"}}}}, "http.headers[0].value,http.headers"},
		{"specula targets use the private rules", monitors.CreateInput{Name: "m", SpeculaID: &spec, HTTP: &monitors.HTTPSettings{URL: ptr("ftp://10.0.0.1")}}, "http.url"},

		{"tcp without settings", monitors.CreateInput{Type: monitors.TypeTCP, Name: "m"}, "tcp"},
		{"tcp with the other settings", monitors.CreateInput{Type: monitors.TypeTCP, Name: "m", HTTP: &monitors.HTTPSettings{}, Heartbeat: &monitors.HeartbeatSettings{}}, "http,heartbeat,tcp"},
		{"tcp settings without host and port", monitors.CreateInput{Type: monitors.TypeTCP, Name: "m", TCP: &monitors.TCPSettings{}}, "tcp.host,tcp.port"},
		{"tcp everything wrong", monitors.CreateInput{Type: monitors.TypeTCP, Name: "", TCP: &monitors.TCPSettings{Host: ptr("a b"), Port: ptr(0), FailureThreshold: ptr(4)}}, "name,tcp.host,tcp.port,tcp.failureThreshold"},
		{"tcp private host from the worker", monitors.CreateInput{Type: monitors.TypeTCP, Name: "m", TCP: &monitors.TCPSettings{Host: ptr("10.0.0.1"), Port: ptr(22)}}, "tcp.host"},

		{"tcp send needs the request keyring", monitors.CreateInput{Type: monitors.TypeTCP, Name: "m", TCP: &monitors.TCPSettings{Host: ptr("example.com"), Port: ptr(22),
			Expect: &monitors.TCPExpect{Mode: "contains", Value: "ok"}}}, "tcp.send"},
		{"tcp send is not available at a specula yet", monitors.CreateInput{Type: monitors.TypeTCP, Name: "m", SpeculaID: &spec, TCP: &monitors.TCPSettings{Host: ptr("db.svc"), Port: ptr(22),
			Expect: &monitors.TCPExpect{Mode: "contains", Value: "ok"}}}, "tcp.send,tcp.expect"},

		{"heartbeat without settings", monitors.CreateInput{Type: monitors.TypeHeartbeat, Name: "m"}, "heartbeat"},
		{"heartbeat with everything else", monitors.CreateInput{Type: monitors.TypeHeartbeat, Name: "m", SpeculaID: &spec, HTTP: &monitors.HTTPSettings{}, TCP: &monitors.TCPSettings{}},
			"location.speculaId,http,tcp,heartbeat"},
		{"heartbeat interval and grace out of range", monitors.CreateInput{Type: monitors.TypeHeartbeat, Name: "m", Heartbeat: &monitors.HeartbeatSettings{ExpectedIntervalSeconds: ptr(1), GraceSeconds: ptr(-1)}},
			"heartbeat.expectedIntervalSeconds,heartbeat.graceSeconds"},

		// An unknown type reports the name and the type only: no settings are looked at.
		{"unknown type", monitors.CreateInput{Type: "dns", Name: "", HTTP: &monitors.HTTPSettings{}}, "name,type"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := svc.Create(context.Background(), actor, c.in)
			if got := fieldsOf(err); got != c.want || m.ID != uuid.Nil || m.HeartbeatToken != "" {
				t.Fatalf("fields = %q (%v), want %q", got, err, c.want)
			}
		})
	}
}
