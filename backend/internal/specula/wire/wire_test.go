package wire

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// The Go shapes and api/specula.yaml describe the same JSON: same property names, and every
// required property is a field without omitempty.
func TestMatchesSpecification(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../../../api/specula.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("api/specula.yaml is not valid: %v", err)
	}
	for name, typ := range map[string]reflect.Type{
		"HelloRequest": reflect.TypeFor[HelloRequest](), "HelloResponse": reflect.TypeFor[HelloResponse](),
		"ClaimRequest": reflect.TypeFor[ClaimRequest](), "ClaimResponse": reflect.TypeFor[ClaimResponse](),
		"Header": reflect.TypeFor[Header](), "Job": reflect.TypeFor[Job](), "Result": reflect.TypeFor[Result](),
		"ResultsRequest": reflect.TypeFor[ResultsRequest](), "ResultsResponse": reflect.TypeFor[ResultsResponse](),
		"ResultOutcome":    reflect.TypeFor[ResultOutcome](),
		"ScheduledMonitor": reflect.TypeFor[ScheduledMonitor](), "Schedule": reflect.TypeFor[Schedule](),
		"Report": reflect.TypeFor[Report](), "ReportsRequest": reflect.TypeFor[ReportsRequest](),
		"ReportOutcome": reflect.TypeFor[ReportOutcome](), "ReportsResponse": reflect.TypeFor[ReportsResponse](),
	} {
		ref := doc.Components.Schemas[name]
		if ref == nil {
			t.Errorf("%s: missing from the specification", name)
			continue
		}
		fields := map[string]bool{} // json name -> omitempty
		for i := range typ.NumField() {
			tag := typ.Field(i).Tag.Get("json")
			parts := strings.Split(tag, ",")
			fields[parts[0]] = slices.Contains(parts[1:], "omitempty")
		}
		for prop := range ref.Value.Properties {
			if _, ok := fields[prop]; !ok {
				t.Errorf("%s: property %q is not a Go field", name, prop)
			}
		}
		for field := range fields {
			if _, ok := ref.Value.Properties[field]; !ok {
				t.Errorf("%s: Go field %q is not in the specification", name, field)
			}
		}
		for _, req := range ref.Value.Required {
			if fields[req] {
				t.Errorf("%s: %q is required but the Go field is omitempty", name, req)
			}
		}
	}
}

// The paths, the outcome values and the capability and mode names are the same in both.
func TestSpecificationValues(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../../../api/specula.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for p := range doc.Paths.Map() {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	want := []string{PathClaim, PathHello, PathReports, PathResults, PathSchedule}
	if !slices.Equal(paths, want) {
		t.Errorf("paths = %v, want %v", paths, want)
	}
	enumOf := func(ref *openapi3.SchemaRef) []string {
		var out []string
		for _, v := range ref.Value.Enum {
			out = append(out, v.(string))
		}
		slices.Sort(out)
		return out
	}
	check := func(name string, got, want []string) {
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	prop := func(schema, p string) *openapi3.SchemaRef { return doc.Components.Schemas[schema].Value.Properties[p] }
	check("ResultOutcome.outcome", enumOf(prop("ResultOutcome", "outcome")), []string{OutcomeRecorded, OutcomeStale, OutcomeRejected})
	check("ReportOutcome.outcome", enumOf(prop("ReportOutcome", "outcome")),
		[]string{OutcomeRecorded, OutcomeDuplicate, OutcomeSuperseded, OutcomeExpired, OutcomeRejected})
	check("ScheduledMonitor.type", enumOf(prop("ScheduledMonitor", "type")), []string{TypeHTTP, TypeTCP})
	check("HelloResponse.modes", enumOf(prop("HelloResponse", "modes").Value.Items), []string{ModePull, ModeSchedule})
	if doc.Paths.Find(PathSchedule).Get == nil || doc.Paths.Find(PathReports).Post == nil {
		t.Error("schedule is a GET and reports a POST")
	}
	if doc.Paths.Find(PathSchedule).Get.Responses.Status(304).Value.Headers[HeaderScheduleValidUntil] == nil {
		t.Errorf("304 of schedule lacks the %s header", HeaderScheduleValidUntil)
	}
	if got := prop("ReportsRequest", "results").Value.MaxItems; got == nil || *got != MaxResults {
		t.Errorf("ReportsRequest max items = %v, want %d", got, MaxResults)
	}
	if got := prop("Schedule", "monitors").Value.MaxItems; got == nil || *got != MaxSchedule {
		t.Errorf("Schedule max items = %v, want %d", got, MaxSchedule)
	}
}
