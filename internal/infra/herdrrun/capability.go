package herdrrun

import (
	"fmt"
	"regexp"
	"strings"
)

const minimumVersion = "0.7.5"

var stableVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

func parseAdmittedVersion(output []byte) (string, error) {
	raw := strings.TrimSpace(string(output))
	version, ok := strings.CutPrefix(raw, "herdr ")
	if !ok || version == "" || strings.ContainsAny(version, " \t\r\n") {
		return "", fmt.Errorf("unsupported herdr CLI version %q (required: herdr stable >=%s)", raw, minimumVersion)
	}
	if err := validateAdmittedVersion(version); err != nil {
		return "", fmt.Errorf("unsupported herdr CLI version %q: %w", raw, err)
	}
	return version, nil
}

func validateAdmittedVersion(version string) error {
	got := stableVersionPattern.FindStringSubmatch(version)
	floor := stableVersionPattern.FindStringSubmatch(minimumVersion)
	if got == nil {
		return fmt.Errorf("required: stable >=%s", minimumVersion)
	}
	for i := 1; i <= 3; i++ {
		if len(got[i]) != len(floor[i]) {
			if len(got[i]) < len(floor[i]) {
				return fmt.Errorf("version %s is below floor %s", version, minimumVersion)
			}
			return nil
		}
		if got[i] < floor[i] {
			return fmt.Errorf("version %s is below floor %s", version, minimumVersion)
		}
		if got[i] > floor[i] {
			return nil
		}
	}
	return nil
}
