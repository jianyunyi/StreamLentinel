// Package archtest enforces the dependency direction of the architecture
// (design document, section 13) as an ordinary test, so a violation fails CI
// the same way a failing unit test does.
//
// Layers, lowest first. A package may import only packages in a strictly lower
// layer (or itself); packages in the same layer never import each other.
//
//	codec
//	protocol, fault
//	bus
//	session, store
//	proxy, mock, replay, loadtest
//	scenario
//	facade
//	httpapi
package archtest

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// rank maps the first path element below internal/ to its layer number.
var rank = map[string]int{
	"codec":    0,
	"protocol": 1, "fault": 1,
	"bus":     2,
	"session": 3, "store": 3,
	"proxy": 4, "mock": 4, "replay": 4, "loadtest": 4,
	"scenario": 5,
	"facade":   6,
	"httpapi":  7,
}

// noThirdParty lists layers that may import only the standard library and
// other packages of this module. The codec must stay dependency-free so it can
// be audited, fuzzed and reused on its own.
var noThirdParty = map[string]bool{"codec": true}

// pkgOf returns the package directory (relative to the module root, slash
// separated) for a module-internal import path, or "" if it is not internal.
func pkgOf(module, importPath string) string {
	rest, ok := strings.CutPrefix(importPath, module+"/")
	if !ok || !strings.HasPrefix(rest, "internal/") {
		return ""
	}
	return rest
}

func layerOf(pkgDir string) (string, bool) {
	parts := strings.Split(pkgDir, "/")
	if len(parts) < 2 || parts[0] != "internal" {
		return "", false
	}
	_, known := rank[parts[1]]
	return parts[1], known
}

func isStdlib(importPath string) bool {
	first, _, _ := strings.Cut(importPath, "/")
	return !strings.Contains(first, ".")
}

// check returns one message per violation. imports maps a package directory to
// the import paths found in its files.
func check(module string, imports map[string][]string) []string {
	var problems []string
	for pkg, list := range imports {
		from, ok := layerOf(pkg)
		if !ok {
			continue
		}
		for _, imp := range list {
			if noThirdParty[from] && !isStdlib(imp) && !strings.HasPrefix(imp, module+"/") {
				problems = append(problems, pkg+" imports third-party package "+imp+"; layer "+from+" is dependency-free")
				continue
			}
			target := pkgOf(module, imp)
			if target == "" || target == pkg {
				continue
			}
			to, ok := layerOf(target)
			if !ok {
				continue
			}
			if rank[to] >= rank[from] {
				problems = append(problems, pkg+" must not import "+target+
					" (layer "+from+" -> "+to+" goes sideways or upwards)")
			}
		}
	}
	sort.Strings(problems)
	return problems
}

func TestCheckerCatchesViolations(t *testing.T) {
	const m = "example.com/app"
	bad := map[string][]string{
		"internal/codec/ws":  {m + "/internal/codec/sse"},           // same layer
		"internal/codec/sse": {m + "/internal/bus", "github.com/x"}, // upwards + third party
		"internal/bus":       {m + "/internal/codec/ws", "fmt"},     // fine
		"internal/session":   {m + "/internal/store"},               // same layer
		"internal/facade":    {m + "/internal/httpapi"},             // upwards
		"internal/httpapi":   {m + "/internal/facade", "net/http"},  // fine
	}
	got := check(m, bad)
	want := 5
	if len(got) != want {
		t.Fatalf("expected %d violations, got %d: %v", want, len(got), got)
	}
	if ok := check(m, map[string][]string{"internal/bus": {m + "/internal/codec/ws"}}); len(ok) != 0 {
		t.Fatalf("legal import flagged: %v", ok)
	}
}

func TestDependencyDirection(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	module := ""
	for _, line := range strings.Split(string(gomod), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			module = strings.TrimSpace(rest)
			break
		}
	}
	if module == "" {
		t.Fatal("module line not found in go.mod")
	}

	imports := map[string][]string{}
	fset := token.NewFileSet()
	err = filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		// ImportsOnly keeps this fast and independent of whether the code builds.
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, filepath.Dir(path))
		pkg := filepath.ToSlash(rel)
		for _, spec := range f.Imports {
			p, _ := strconv.Unquote(spec.Path.Value)
			imports[pkg] = append(imports[pkg], p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(imports) == 0 {
		t.Fatal("scanned no packages: the checker would pass vacuously")
	}
	for _, p := range check(module, imports) {
		t.Error(p)
	}
}
