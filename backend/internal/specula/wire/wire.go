// Package wire holds the JSON shapes of the Specula API (api/specula.yaml), shared by the gateway
// and by the executor. A test keeps them in step with the specification.
package wire

import (
	"time"

	"github.com/google/uuid"
)

// Base path of the API on the gateway.
const BasePath = "/specula/v1"

// Limits the gateway enforces; the executor stays inside them.
const (
	MaxClaim   = 50
	MaxResults = 100
)

// HelloRequest is sent at start and every minute.
type HelloRequest struct {
	Version  string `json:"version"`
	Hostname string `json:"hostname"`
}

// HelloResponse tells the executor the server's clock, to notice a large skew.
type HelloResponse struct {
	ServerTime time.Time `json:"serverTime"`
}

// ClaimRequest asks for at most Max due jobs.
type ClaimRequest struct {
	Max int `json:"max"`
}

// Header is one request header with its value.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Job is one check to run before LeaseUntil. Headers and Body are the monitor's secrets, sent
// over TLS to an executor the customer installed.
type Job struct {
	MonitorID      uuid.UUID `json:"monitorId"`
	LeaseToken     uuid.UUID `json:"leaseToken"`
	LeaseUntil     time.Time `json:"leaseUntil"`
	URL            string    `json:"url"`
	Method         string    `json:"method"`
	Headers        []Header  `json:"headers,omitempty"`
	Body           string    `json:"body,omitempty"`
	ExpectedStatus string    `json:"expectedStatus"`
	TimeoutSeconds int       `json:"timeoutSeconds"`
}

// ClaimResponse lists the leased jobs, possibly none.
type ClaimResponse struct {
	Jobs []Job `json:"jobs"`
}

// Result is the outcome of one job.
type Result struct {
	MonitorID   uuid.UUID  `json:"monitorId"`
	LeaseToken  uuid.UUID  `json:"leaseToken"`
	CheckedAt   time.Time  `json:"checkedAt"`
	OK          bool       `json:"ok"`
	StatusCode  int        `json:"statusCode,omitempty"`
	LatencyMs   int        `json:"latencyMs,omitempty"`
	ErrorKind   string     `json:"errorKind,omitempty"`
	TLSNotAfter *time.Time `json:"tlsNotAfter,omitempty"`
}

// ResultsRequest reports finished jobs.
type ResultsRequest struct {
	Results []Result `json:"results"`
}

// Outcomes of one reported result.
const (
	// OutcomeRecorded: stored and applied.
	OutcomeRecorded = "recorded"
	// OutcomeStale: the lease expired or was replaced; the result is discarded and must not be resent.
	OutcomeStale = "stale"
	// OutcomeRejected: the result is malformed; it must not be resent.
	OutcomeRejected = "rejected"
)

// ResultOutcome is the answer for one result, in request order.
type ResultOutcome struct {
	MonitorID uuid.UUID `json:"monitorId"`
	Outcome   string    `json:"outcome"`
}

// ResultsResponse answers every reported result.
type ResultsResponse struct {
	Outcomes []ResultOutcome `json:"outcomes"`
}
