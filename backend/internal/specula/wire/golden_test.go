package wire

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// schedule1Fields is the recorded JSON field set of everything schedule/1 sends or receives.
var schedule1Fields = map[string][]string{
	"HelloRequest":     {"version", "hostname", "capabilities", "instanceId", "scheduleIssuedAt", "bufferDepth", "oldestUnsentAt", "bufferDegraded", "clockOffsetMs"},
	"HelloResponse":    {"serverTime", "modes", "scheduleVersion"},
	"Header":           {"name", "value"},
	"ScheduledMonitor": {"monitorId", "epoch", "active", "anchor", "intervalSeconds", "timeoutSeconds", "type", "url", "host", "port", "method", "headers", "body", "expectedStatus"},
	"Schedule":         {"version", "serverTime", "issuedAt", "validUntil", "refreshAfterSeconds", "monitors"},
	"Report":           {"monitorId", "epoch", "slot", "ok", "statusCode", "latencyMs", "errorKind", "tlsNotAfter"},
	"ReportsRequest":   {"results"},
	"ReportOutcome":    {"monitorId", "slot", "outcome"},
	"ReportsResponse":  {"scheduleVersion", "outcomes"},
}

// A schedule/1 request or response never gains a field: both sides reject unknown fields (an older
// gateway answers 400, and a 400 on /reports is retried for ever), so the new field needs a new
// capability (schedule/2) and the gateway is upgraded first.
func TestSchedule1FieldsAreFrozen(t *testing.T) {
	types := map[string]reflect.Type{
		"HelloRequest": reflect.TypeFor[HelloRequest](), "HelloResponse": reflect.TypeFor[HelloResponse](),
		"Header": reflect.TypeFor[Header](), "ScheduledMonitor": reflect.TypeFor[ScheduledMonitor](),
		"Schedule": reflect.TypeFor[Schedule](), "Report": reflect.TypeFor[Report](),
		"ReportsRequest": reflect.TypeFor[ReportsRequest](), "ReportOutcome": reflect.TypeFor[ReportOutcome](),
		"ReportsResponse": reflect.TypeFor[ReportsResponse](),
	}
	for name, typ := range types {
		var got []string
		for f := range typ.Fields() {
			got = append(got, strings.Split(f.Tag.Get("json"), ",")[0])
		}
		if !slices.Equal(got, schedule1Fields[name]) {
			t.Errorf("%s fields are %v, recorded %v.\nA schedule/1 request or response never gains, loses or renames a field: an older peer rejects it "+
				"(a 400 on /reports is retried for ever). A change needs a new capability (schedule/2), the gateway upgraded first; "+
				"then record the new set here and in docs/runbooks/specula.md.", name, got, schedule1Fields[name])
		}
	}
}
