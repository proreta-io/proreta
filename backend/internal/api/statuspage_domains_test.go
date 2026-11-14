package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/statuspages/domains"
)

func TestDomainErrorsBecomeProblems(t *testing.T) {
	_, hostErr := domains.NewRules(nil, nil).Validate("localhost.internal")
	tests := []struct {
		name   string
		err    error
		status int
		typ    string
		field  string
	}{
		{"feature off", errCustomDomainsDisabled, http.StatusNotFound, "urn:proreta:problem:custom-domains-disabled", ""},
		{"forbidden host", hostErr, http.StatusUnprocessableEntity, "urn:proreta:problem:domain-forbidden", "host"},
		{"wrapped forbidden host", fmt.Errorf("add: %w", hostErr), http.StatusUnprocessableEntity, "urn:proreta:problem:domain-forbidden", "host"},
		{"host not available", fmt.Errorf("verify: %w", domains.ErrUnavailable), http.StatusConflict, "urn:proreta:problem:domain-unavailable", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeError(rec, httptest.NewRequest(http.MethodPost, "/api/v1/status-pages/x/domains", nil), discard(), tc.err)
			var p problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatal(err)
			}
			if rec.Code != tc.status || p.Type != tc.typ || p.Status != tc.status {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if tc.field != "" && (len(p.FieldErrors) != 1 || p.FieldErrors[0].Field != tc.field || p.FieldErrors[0].Message == "") {
				t.Fatalf("field errors %+v", p.FieldErrors)
			}
		})
	}
}
