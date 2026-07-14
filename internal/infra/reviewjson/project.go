// Package reviewjson projects a reviewer result into the small file cache used
// by the post-work-review shell driver.
package reviewjson

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // Finding fingerprints are stable identifiers, not security hashes.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

var scalarKeys = []string{
	"backend",
	"review_type",
	"reviewer_agent",
	"reviewer_provenance",
	"reviewer_session_id",
	"same_agent_review",
	"reviewer_isolated",
	"reviewer_sandbox_mode",
	"hooks_only_success",
	"head",
	"diff_hash",
	"bundle_sha256",
	"finding_count",
	"truncated",
	"all_previous_findings_fixed",
	"new_regressions",
}

// BundleSHA256 returns the lowercase SHA-256 digest of one exact regular-file
// snapshot. It rejects symlinks and a path that changes identity while read.
func BundleSHA256(path string) (string, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect review bundle: %w", err)
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return "", errors.New("review bundle is not a regular file")
	}
	if before.Size() == 0 {
		return "", errors.New("review bundle is empty")
	}

	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open review bundle: %w", err)
	}
	defer func() {
		// The digest is already decided; a close error cannot change the bytes
		// read from the regular file.
		_ = file.Close()
	}()

	opened, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat open review bundle: %w", err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return "", errors.New("review bundle changed before it was opened")
	}

	hash := sha256.New()
	if _, copyErr := io.Copy(hash, file); copyErr != nil {
		return "", fmt.Errorf("hash review bundle: %w", copyErr)
	}
	after, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("reinspect review bundle: %w", err)
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() ||
		!os.SameFile(opened, after) || before.Size() != after.Size() ||
		!before.ModTime().Equal(after.ModTime()) {
		return "", errors.New("review bundle changed while it was hashed")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

var requiredFindingKeys = []string{
	"severity",
	"file",
	"line",
	"title",
	"description",
	"recommendation",
}

type projectedFile struct {
	name string
	data []byte
}

// Project parses inputPath and writes the driver's cache files into outputDir.
// The empty "valid" file is written last, after every other output succeeds.
func Project(inputPath, outputDir string) error {
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return fmt.Errorf("read reviewer JSON: %w", err)
	}
	return project(data, outputDir)
}

func project(data []byte, outputDir string) error {
	info, err := os.Stat(outputDir)
	if err != nil {
		return fmt.Errorf("stat cache directory: %w", err)
	}
	if !info.IsDir() {
		return errors.New("reviewer JSON cache path is not a directory")
	}

	validPath := filepath.Join(outputDir, "valid")
	removeErr := os.Remove(validPath)
	if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return fmt.Errorf("clear stale valid marker: %w", removeErr)
	}

	if !utf8.Valid(data) {
		return errors.New("reviewer JSON is not valid UTF-8")
	}
	if surrogateErr := validateSurrogatePairs(data); surrogateErr != nil {
		return surrogateErr
	}

	var root map[string]json.RawMessage
	unmarshalErr := json.Unmarshal(data, &root)
	if unmarshalErr != nil {
		return fmt.Errorf("parse reviewer JSON: %w", unmarshalErr)
	}
	if root == nil {
		return errors.New("reviewer JSON must be an object")
	}

	files, err := projectFiles(root)
	if err != nil {
		return err
	}
	for _, file := range files {
		if err := os.WriteFile(filepath.Join(outputDir, file.name), file.data, 0o600); err != nil {
			return fmt.Errorf("write cache file %s: %w", file.name, err)
		}
	}
	if err := os.WriteFile(validPath, nil, 0o600); err != nil {
		return fmt.Errorf("write valid marker: %w", err)
	}
	return nil
}

// validateSurrogatePairs rejects lone UTF-16 surrogate escapes before
// encoding/json can replace them with U+FFFD. The previous Ruby/Python parsers
// rejected these malformed reviewer results instead of marking them valid.
func validateSurrogatePairs(data []byte) error {
	inString := false
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case '"':
			inString = !inString
		case '\\':
			if !inString || i+1 >= len(data) {
				continue
			}
			if data[i+1] != 'u' {
				i++
				continue
			}
			unit, ok := escapedCodeUnit(data, i)
			if !ok {
				continue
			}
			switch {
			case unit >= 0xd800 && unit <= 0xdbff:
				next := i + 6
				low, paired := escapedCodeUnit(data, next)
				if !paired || low < 0xdc00 || low > 0xdfff {
					return fmt.Errorf("reviewer JSON contains an unpaired UTF-16 surrogate escape at byte %d", i)
				}
				i = next + 5
			case unit >= 0xdc00 && unit <= 0xdfff:
				return fmt.Errorf("reviewer JSON contains an unpaired UTF-16 surrogate escape at byte %d", i)
			default:
				i += 5
			}
		}
	}
	return nil
}

func escapedCodeUnit(data []byte, start int) (uint16, bool) {
	if start < 0 || start+6 > len(data) || data[start] != '\\' || data[start+1] != 'u' {
		return 0, false
	}
	var unit uint16
	for _, b := range data[start+2 : start+6] {
		nibble, ok := hexNibble(b)
		if !ok {
			return 0, false
		}
		unit = unit<<4 | uint16(nibble)
	}
	return unit, true
}

func hexNibble(b byte) (byte, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, true
	default:
		return 0, false
	}
}

func projectFiles(root map[string]json.RawMessage) ([]projectedFile, error) {
	files := make([]projectedFile, 0, len(scalarKeys)+3)
	for _, key := range scalarKeys {
		raw, ok := root[key]
		if !ok {
			continue
		}
		value, err := scalarValue(raw)
		if err != nil {
			return nil, fmt.Errorf("project %s: %w", key, err)
		}
		files = append(files, projectedFile{name: key, data: []byte(value)})
	}

	findingsRaw, ok := root["findings"]
	if !ok {
		return files, nil
	}
	findings, array := findingsArray(findingsRaw)
	if !array {
		// Schema validation remains in the shell driver. Omitting these derived
		// files preserves its existing "findings must be an array" result.
		return files, nil
	}

	missing := 0
	var tsv bytes.Buffer
	for i, raw := range findings {
		finding, object := findingObject(raw)
		if !object || findingMissingRequired(finding) {
			missing++
		}

		severity := cleanFindingValue(finding["severity"])
		path := cleanFindingValue(finding["file"])
		line := cleanFindingValue(finding["line"])
		title := cleanFindingValue(finding["title"])
		description := cleanFindingValue(finding["description"])
		recommendation := cleanFindingValue(finding["recommendation"])
		fingerprint := findingFingerprint(path, line, title, description)
		fields := []string{
			strconv.Itoa(i + 1),
			severity,
			path,
			line,
			title,
			fingerprint,
			description,
			recommendation,
		}
		tsv.WriteString(strings.Join(fields, "\t"))
		tsv.WriteByte('\n')
	}

	files = append(files,
		projectedFile{name: "findings_count", data: []byte(strconv.Itoa(len(findings)))},
		projectedFile{name: "findings_missing_required_count", data: []byte(strconv.Itoa(missing))},
		projectedFile{name: "findings.tsv", data: tsv.Bytes()},
	)
	return files, nil
}

func findingsArray(raw json.RawMessage) ([]json.RawMessage, bool) {
	var findings []json.RawMessage
	if json.Unmarshal(raw, &findings) != nil || findings == nil {
		return nil, false
	}
	return findings, true
}

func scalarValue(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", errors.New("empty JSON value")
	}
	switch trimmed[0] {
	case '"':
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return "", err
		}
		return value, nil
	case '{', '[':
		var compact bytes.Buffer
		if err := json.Compact(&compact, trimmed); err != nil {
			return "", err
		}
		return compact.String(), nil
	default:
		return string(trimmed), nil
	}
}

func findingObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var finding map[string]json.RawMessage
	if err := json.Unmarshal(raw, &finding); err != nil || finding == nil {
		return map[string]json.RawMessage{}, false
	}
	return finding, true
}

func findingMissingRequired(finding map[string]json.RawMessage) bool {
	for _, key := range requiredFindingKeys {
		raw, ok := finding[key]
		if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return true
		}
		value, err := scalarValue(raw)
		if err != nil || strings.TrimSpace(value) == "" {
			return true
		}
	}
	return false
}

func cleanFindingValue(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ""
	}
	value, err := scalarValue(raw)
	if err != nil {
		return ""
	}
	return strings.Join(strings.Fields(value), " ")
}

func findingFingerprint(path, line, title, description string) string {
	source := strings.Join([]string{path, line, title, description}, "\x00")
	sum := sha1.Sum([]byte(source)) //nolint:gosec // Compatibility fingerprint only.
	return hex.EncodeToString(sum[:])
}
