package platformsettings

import (
	"errors"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/platform/validation"
)

func ptr(n int) *int { return &n }

func TestValidateRetention(t *testing.T) {
	for _, c := range []struct {
		name         string
		days, months *int
		fields       []string
	}{
		{"defaults", nil, nil, nil},
		{"lower bounds", ptr(7), ptr(4), nil},
		{"upper bounds", ptr(365), ptr(60), nil},
		{"days too low", ptr(6), nil, []string{"deliveryDays"}},
		{"days too high", ptr(366), nil, []string{"deliveryDays"}},
		{"months too low", nil, ptr(3), []string{"rollupMonths"}},
		{"months too high", nil, ptr(61), []string{"rollupMonths"}},
		{"both", ptr(0), ptr(-1), []string{"deliveryDays", "rollupMonths"}},
	} {
		err := ValidateRetention(c.days, c.months)
		var verr *validation.Error
		if len(c.fields) == 0 {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
			continue
		}
		if !errors.As(err, &verr) || len(verr.Fields) != len(c.fields) {
			t.Errorf("%s: err = %v, want fields %v", c.name, err, c.fields)
			continue
		}
		for i, f := range c.fields {
			if verr.Fields[i].Field != f {
				t.Errorf("%s: field %d = %s, want %s", c.name, i, verr.Fields[i].Field, f)
			}
		}
	}
}

func TestEffectiveRetention(t *testing.T) {
	if d, m := EffectiveRetention(nil, nil); d != 30 || m != 13 {
		t.Errorf("defaults = %d, %d; want 30, 13", d, m)
	}
	if d, m := EffectiveRetention(ptr(10), ptr(6)); d != 10 || m != 6 {
		t.Errorf("chosen = %d, %d; want 10, 6", d, m)
	}
}
