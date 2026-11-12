package notify

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRenderTemplate(t *testing.T) {
	data := map[string]string{
		"monitor.name": `He said "hi" \ <b>&`,
		"event":        "opened",
		"error.kind":   "",
	}
	tests := []struct {
		name, ct, tpl, want string
		err                 error
	}{
		{name: "text verbatim", ct: ContentText, tpl: "{{event}}: {{monitor.name}}", want: `opened: He said "hi" \ <b>&`},
		{name: "json escapes quotes backslash and keeps html readable", ct: ContentJSON, tpl: `{"n":"{{monitor.name}}"}`, want: `{"n":"He said \"hi\" \\ <b>&"}`},
		{name: "unknown name with spaces is reported trimmed", ct: ContentJSON, tpl: `{"n":"{{ x }}"}`, err: &UnknownPlaceholderError{Name: "x"}},
		{name: "spaces inside braces", ct: ContentText, tpl: "{{  event }}", want: "opened"},
		{name: "missing value is empty", ct: ContentText, tpl: "[{{error.kind}}][{{link}}]", want: "[][]"},
		{name: "unknown", ct: ContentText, tpl: "{{nope}}", err: &UnknownPlaceholderError{Name: "nope"}},
		{name: "empty braces", ct: ContentText, tpl: "{{}}", err: &UnknownPlaceholderError{Name: ""}},
		{name: "triple braces keep the outer ones", ct: ContentText, tpl: "{{{event}}}", want: "{opened}"},
		{name: "single braces are literal", ct: ContentJSON, tpl: `{"a":{"b":"{{event}}"}}`, want: `{"a":{"b":"opened"}}`},
		{name: "values are not rescanned", ct: ContentText, tpl: "{{monitor.name}}", want: `He said "hi" \ <b>&`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RenderTemplate(tt.ct, tt.tpl, data)
			if tt.err != nil {
				var u *UnknownPlaceholderError
				if !errors.As(err, &u) || u.Name != tt.err.(*UnknownPlaceholderError).Name {
					t.Fatalf("err = %v, want %v", err, tt.err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestRenderJSONControlCharacters(t *testing.T) {
	got, err := RenderTemplate(ContentJSON, `{"n":"{{monitor.name}}"}`, map[string]string{"monitor.name": "a\nb\x00c\u2028\"}"})
	if err != nil {
		t.Fatal(err)
	}
	var v struct{ N string }
	if err := json.Unmarshal([]byte(got), &v); err != nil || v.N != "a\nb\x00c\u2028\"}" {
		t.Fatalf("round trip failed: %q %v %+v", got, err, v)
	}
}

func TestRenderSizeCap(t *testing.T) {
	big := strings.Repeat("x", MaxRenderedBytes+1)
	if _, err := RenderTemplate(ContentText, "{{monitor.name}}", map[string]string{"monitor.name": big}); !errors.Is(err, ErrTemplateTooLarge) {
		t.Fatalf("err = %v", err)
	}
	if _, err := RenderTemplate(ContentText, "{{monitor.name}}", map[string]string{"monitor.name": big[:MaxRenderedBytes]}); err != nil {
		t.Fatalf("just under the cap: %v", err)
	}
}

func TestValidateTemplate(t *testing.T) {
	tests := []struct {
		name, ct, tpl, wantSub string
	}{
		{"default json", ContentJSON, DefaultJSONTemplate, ""},
		{"default text", ContentText, DefaultTextTemplate, ""},
		{"every placeholder", ContentText, "{{" + strings.Join(Placeholders, "}} {{") + "}}", ""},
		{"empty", ContentText, "  ", "is required"},
		{"too long", ContentText, strings.Repeat("a", MaxTemplateBytes+1), "at most 8192"},
		{"unknown placeholder named", ContentText, "hi {{monitor.nam}}", "{{monitor.nam}}"},
		{"json syntax error has position", ContentJSON, `{"a": "{{event}}",}`, "at byte"},
		{"json placeholder outside a string", ContentJSON, `{"a": {{event}}}`, "not valid JSON"},
		{"json not a document", ContentJSON, `{"a":1} {"b":2}`, "not valid JSON"},
		{"text may be anything", ContentText, `{"a": "`, ""},
		{"invalid utf-8", ContentText, "a\xffb", "UTF-8"},
		{"rendered too large", ContentText, strings.Repeat("{{link}}", 1000), "more than"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := ValidateTemplate(tt.ct, tt.tpl)
			if tt.wantSub == "" && msg != "" || !strings.Contains(msg, tt.wantSub) {
				t.Fatalf("message = %q, want it to contain %q", msg, tt.wantSub)
			}
		})
	}
}

func TestValidateContentType(t *testing.T) {
	for _, ok := range []string{"json", "text"} {
		if ValidateContentType(ok) != "" {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", "xml", "JSON"} {
		if ValidateContentType(bad) == "" {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestDefaultTemplatesRenderValid(t *testing.T) {
	out, err := RenderTemplate(ContentJSON, DefaultTemplate(ContentJSON), SampleData("https://p.example/"))
	if err != nil || jsonProblem(out) != "" {
		t.Fatalf("%q %v", out, err)
	}
	if !strings.Contains(out, `Example \"API\" monitor`) || !strings.Contains(out, "https://p.example/monitors/") {
		t.Errorf("sample not rendered: %s", out)
	}
}
