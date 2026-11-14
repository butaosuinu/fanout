// Command typegen generates the dashboard's TypeScript wire types from Go DTOs.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gzuidhof/tygo/tygo"

	"github.com/butaosuinu/fanout/internal/core/errs"
)

const outputDir = "web/src/transport/generated"

var sourcePackages = []tygo.PackageConfig{
	{
		Path:         "github.com/butaosuinu/fanout/internal/core/blockers",
		IncludeFiles: []string{"blockers.go"},
	},
	{
		Path:         "github.com/butaosuinu/fanout/internal/infra/ghissue",
		IncludeFiles: []string{"ghissue.go", "stack.go"},
	},
	{
		Path:         "github.com/butaosuinu/fanout/internal/app/sessionview",
		IncludeFiles: []string{"model.go"},
		TypeMappings: map[string]string{"backend.Name": "string", "blockers.Status": "Status"},
		Frontmatter:  "import type { Status } from './blockers.gen';\nimport type { PRRef } from './ghissue.gen';\n",
	},
	{
		Path:         "github.com/butaosuinu/fanout/internal/ui/dashboard",
		IncludeFiles: []string{"peek.go", "plan.go", "diff.go"},
	},
}

func main() {
	write := flag.Bool("write", false, "write generated TypeScript types (run from the repository root)")
	flag.Parse()
	if !*write || flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := writeTypes(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func writeTypes() (err error) {
	defer errs.Wrap(&err, "typegen")
	dir, err := os.MkdirTemp("", "fanout-typegen-")
	if err != nil {
		return err
	}
	defer func() {
		// Only disposable generated files remain if cleanup fails.
		_ = os.RemoveAll(dir)
	}()
	files, err := generate(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(outputDir, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Generate into a fresh scratch directory: tygo v0.2.21 swallows write errors,
// so reading every expected output must succeed before we update tracked files.
func generate(dir string) (map[string][]byte, error) {
	config := &tygo.Config{}
	for _, source := range sourcePackages {
		source.OutputPath = filepath.Join(dir, filepath.Base(source.Path)+".gen.ts")
		source.FallbackType = "unknown"
		source.PreserveComments = "types"
		config.Packages = append(config.Packages, &source)
	}
	if err := tygo.New(config).Generate(); err != nil {
		return nil, err
	}
	files := make(map[string][]byte, len(config.Packages))
	for _, pkg := range config.Packages {
		data, err := os.ReadFile(pkg.OutputPath)
		if err != nil {
			return nil, err
		}
		files[filepath.Base(pkg.OutputPath)] = data
	}
	return files, nil
}
