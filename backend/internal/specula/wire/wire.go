// Package wire holds the JSON shapes of the Specula API (api/specula.yaml), shared by the gateway
// and by the executor. A test keeps them in step with the specification.
package wire

import (
	"time"

	"github.com/google/uuid"
)

// Base path of the API on the gateway.
const BasePath = "/specula/v1"

// Paths of the operations below BasePath.
const (
	PathHello    = BasePath + "/hello"
	PathClaim    = BasePath + "/claim"
	PathResults  = BasePath + "/results"
	PathSchedule = BasePath + "/schedule"
	PathReports  = BasePath + "/reports"
)

// Limits the gateway enforces; the executor stays inside them.
const (
	MaxClaim    = 50
	MaxResults  = 100
	MaxSchedule = 5000 // monitors in one schedule; more answers 413
)

// Capabilities an executor announces in HelloRequest.Capabilities.
const (
	CapTCP      = "tcp"
	CapSchedule = "schedule/1"
)

// Modes the server offers in HelloResponse.Modes.
const (
	ModePull     = "pull"
	ModeSchedule = "schedule/1"
)

// HeaderScheduleValidUntil carries the extended validUntil of a 304 answer of /schedule.
const HeaderScheduleValidUntil = "X-Schedule-Valid-Until"

// HelloRequest is sent at start and every minute. The fields after Hostname are optional; an
// older server does not know them and answers 400, see api/specula.yaml.
type HelloRequest struct {
	Version          string     `json:"version"`
	Hostname         string     `json:"hostname"`
	Capabilities     []string   `json:"capabilities,omitempty"`
	InstanceID       string     `json:"instanceId,omitempty"`
	ScheduleIssuedAt *time.Time `json:"scheduleIssuedAt,omitempty"`
	BufferDepth      *int       `json:"bufferDepth,omitempty"`
	OldestUnsentAt   *time.Time `json:"oldestUnsentAt,omitempty"`
	BufferDegraded   *bool      `json:"bufferDegraded,omitempty"`
}

// HelloResponse tells the executor the server's clock, to notice a large skew, and (a server that
// knows schedule mode) the modes it offers and the current schedule version.
type HelloResponse struct {
	ServerTime      time.Time `json:"serverTime"`
	Modes           []string  `json:"modes,omitempty"`
	ScheduleVersion string    `json:"scheduleVersion,omitempty"`
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

// Job types. An empty Type is TypeHTTP.
const (
	TypeHTTP = "http"
	TypeTCP  = "tcp"
)

// Job is one check to run before LeaseUntil. Headers and Body are the monitor's secrets, sent
// over TLS to an executor the customer installed. A TCP job has Host and Port; its URL, Method
// and ExpectedStatus are unused.
type Job struct {
	MonitorID      uuid.UUID `json:"monitorId"`
	LeaseToken     uuid.UUID `json:"leaseToken"`
	LeaseUntil     time.Time `json:"leaseUntil"`
	Type           string    `json:"type,omitempty"`
	URL            string    `json:"url"`
	Host           string    `json:"host,omitempty"`
	Port           int       `json:"port,omitempty"`
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
	// OutcomeRejected: the result is malformed or the database refused it for good; it must not be resent.
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

// Outcomes of one report, in addition to OutcomeRecorded and OutcomeRejected above.
const (
	// OutcomeDuplicate: the slot already has a result; nothing to do, never resend.
	OutcomeDuplicate = "duplicate"
	// OutcomeSuperseded: the slot lies outside the time the epoch was active, or the epoch is not the monitor's.
	OutcomeSuperseded = "superseded"
	// OutcomeExpired: older than the backlog the server accepts.
	OutcomeExpired = "expired"
)

// ScheduledMonitor is one monitor of the schedule. Its slots are Anchor + n * IntervalSeconds.
// Headers and Body are the monitor's secrets; a tcp monitor has Host and Port.
type ScheduledMonitor struct {
	MonitorID       uuid.UUID `json:"monitorId"`
	Epoch           int       `json:"epoch"`
	Active          bool      `json:"active"`
	Anchor          int64     `json:"anchor"`
	IntervalSeconds int       `json:"intervalSeconds"`
	TimeoutSeconds  int       `json:"timeoutSeconds"`
	Type            string    `json:"type,omitempty"`
	URL             string    `json:"url"`
	Host            string    `json:"host,omitempty"`
	Port            int       `json:"port,omitempty"`
	Method          string    `json:"method"`
	Headers         []Header  `json:"headers,omitempty"`
	Body            string    `json:"body,omitempty"`
	ExpectedStatus  string    `json:"expectedStatus"`
}

// Schedule is the answer of GET /schedule. Version is also its ETag.
type Schedule struct {
	Version             string             `json:"version"`
	ServerTime          time.Time          `json:"serverTime"`
	IssuedAt            time.Time          `json:"issuedAt"`
	ValidUntil          time.Time          `json:"validUntil"`
	RefreshAfterSeconds int                `json:"refreshAfterSeconds"`
	Monitors            []ScheduledMonitor `json:"monitors"`
}

// Report is the outcome of the check of one slot.
type Report struct {
	MonitorID   uuid.UUID  `json:"monitorId"`
	Epoch       int        `json:"epoch"`
	Slot        int64      `json:"slot"`
	OK          bool       `json:"ok"`
	StatusCode  int        `json:"statusCode,omitempty"`
	LatencyMs   int        `json:"latencyMs,omitempty"`
	ErrorKind   string     `json:"errorKind,omitempty"`
	TLSNotAfter *time.Time `json:"tlsNotAfter,omitempty"`
}

// ReportsRequest reports finished slots, oldest first, at most MaxResults.
type ReportsRequest struct {
	Results []Report `json:"results"`
}

// ReportOutcome is the answer for one report, in request order.
type ReportOutcome struct {
	MonitorID uuid.UUID `json:"monitorId"`
	Slot      int64     `json:"slot"`
	Outcome   string    `json:"outcome"`
}

// ReportsResponse answers every report and tells the executor the current schedule version.
type ReportsResponse struct {
	ScheduleVersion string          `json:"scheduleVersion"`
	Outcomes        []ReportOutcome `json:"outcomes"`
}
