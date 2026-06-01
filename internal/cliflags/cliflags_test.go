package cliflags

import (
	"reflect"
	"testing"
)

func TestParseNumCSVAllowsSingleTrailingComma(t *testing.T) {
	got, err := parseNumCSV("--only", "501,")
	if err != nil {
		t.Fatalf("parseNumCSV returned error: %v", err)
	}
	if want := []int{501}; !reflect.DeepEqual(got, want) {
		t.Fatalf("parseNumCSV = %#v, want %#v", got, want)
	}
}

func TestParseNumCSVRejectsInternalAndRepeatedTrailingEmptyEntries(t *testing.T) {
	for _, raw := range []string{"501,,502", "501,,", ","} {
		if _, err := parseNumCSV("--only", raw); err == nil {
			t.Fatalf("parseNumCSV(%q) returned nil error", raw)
		}
	}
}
