package dashboard

import (
	"crypto/rand"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/errs"
	"github.com/butaosuinu/fanout/internal/infra/atomicfs"
	"github.com/butaosuinu/fanout/internal/infra/settings"
)

// DashboardEntry is a token-free locator, not authority to control a process.
// ProjectRoot can also identify the repository for future cross-repo discovery.
type DashboardEntry struct {
	ProjectRoot string `json:"projectRoot"`
	URL         string `json:"url"`
	PID         int    `json:"pid"`
	StartedAt   string `json:"startedAt"`
}

// DashboardRegistryDir keeps live-dashboard records separate from the persistent
// repository registry proposed in #313. It follows fanout's user config location.
func DashboardRegistryDir() (_ string, err error) {
	defer errs.Wrap(&err, "dashboard registry directory")
	dir := filepath.Dir(settings.UserConfigPath())
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("HOME or XDG_CONFIG_HOME must name an absolute directory")
	}
	return filepath.Join(dir, "dashboards"), nil
}

// RegisterDashboard writes one 0600 record per launch through atomicfs. Unique
// filenames let concurrent processes register and unregister without a shared
// read-modify-write transaction or removing a replacement server's record.
// The returned cleanup owns only this registration; missing files are harmless.
func RegisterDashboard(projectRoot string, rf RunFile) (_ func() error, err error) {
	defer errs.Wrap(&err, "register dashboard")
	dir, err := DashboardRegistryDir()
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(rf.URL)
	if err != nil || !isLoopbackHTTP(u) || !filepath.IsAbs(projectRoot) || rf.PID <= 0 {
		return nil, fmt.Errorf("invalid dashboard locator")
	}
	entry := DashboardEntry{projectRoot, (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/"}).String(), rf.PID, rf.StartedAt}
	path := filepath.Join(dir, rand.Text()+".json")
	if err := atomicfs.WriteJSON(path, entry, 0o600); err != nil {
		return nil, err
	}
	return func() error {
		err := os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}, nil
}

// ListDashboards returns only live, valid locators. It neither starts servers nor
// prunes records: a crashed launch's record must not race another launch's write.
func ListDashboards() (_ []DashboardEntry, err error) {
	defer errs.Wrap(&err, "list dashboards")
	dir, err := DashboardRegistryDir()
	if err != nil {
		return nil, err
	}
	files, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var live []DashboardEntry
	for _, file := range files {
		if !file.Type().IsRegular() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		entry, readErr := readDashboardEntry(filepath.Join(dir, file.Name()))
		if readErr != nil {
			return nil, readErr
		}
		if entry.isLive() {
			live = append(live, entry)
		}
	}
	return live, nil
}

func readDashboardEntry(path string) (DashboardEntry, error) {
	var entry DashboardEntry
	found, err := atomicfs.ReadJSON(path, &entry)
	if found && err != nil {
		return DashboardEntry{}, nil //nolint:nilerr // Malformed records are omitted; filesystem read failures still propagate.
	}
	return entry, err
}

func (entry DashboardEntry) isLive() bool {
	u, err := url.Parse(entry.URL)
	if err != nil || !filepath.IsAbs(entry.ProjectRoot) || entry.StartedAt == "" {
		return false
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/" {
		return false // Never return credentials or token-bearing URLs from the registry.
	}
	return (&RunFile{URL: entry.URL, PID: entry.PID}).IsLive()
}
