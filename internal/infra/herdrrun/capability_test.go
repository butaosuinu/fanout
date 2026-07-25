package herdrrun

import (
	"strings"
	"testing"
)

func TestValidateAdmittedVersionAcceptsStableFloorOrNewer(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"0.7.5", "0.7.6", "0.10.0", "1.0.0", "0.7.5+build.1"} {
		if err := validateAdmittedVersion(version); err != nil {
			t.Errorf("validateAdmittedVersion(%q) error = %v", version, err)
		}
	}
}

func TestValidateAdmittedVersionRejectsUnsupportedForms(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"0.7.4", "0.7.5-preview.1", "0.7", "v0.7.5", "00.7.5", "0.7.5 other"} {
		if err := validateAdmittedVersion(version); err == nil {
			t.Errorf("validateAdmittedVersion(%q) succeeded", version)
		}
	}
}

func TestParseAdmittedVersionRequiresCanonicalCLIOutput(t *testing.T) {
	t.Parallel()
	version, err := parseAdmittedVersion([]byte("herdr 0.7.5\n"))
	if err != nil || version != "0.7.5" {
		t.Fatalf("parseAdmittedVersion() = %q, %v", version, err)
	}
	for _, output := range []string{"0.7.5", "herdr v0.7.5", "herdr 0.7.5 extra"} {
		if _, err := parseAdmittedVersion([]byte(output)); err == nil || !strings.Contains(err.Error(), "stable >=0.7.5") {
			t.Errorf("parseAdmittedVersion(%q) error = %v", output, err)
		}
	}
}
