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
		"ResultOutcome": reflect.TypeFor[ResultOutcome](),
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
