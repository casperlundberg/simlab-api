package arch

import (
	"go/build"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const module = "github.com/casperlundberg/simlab-api/"

// mayImport is the layering, written out: a package may import the packages
// listed for it and no others. Entries marked "debt" are dependencies the
// layering does not want, written down so they cannot grow while they are
// removed; each names what removes it.
var mayImport = map[string][]string{
	"cmd/simlab-api": {"internal/app"},

	// The vocabulary, and which code this process is.
	"internal/domain":    {},
	"internal/buildinfo": {"internal/domain"},

	// This package. It reads the graph and imports none of it.
	"internal/arch": {},

	// The physics and geometry of a mine.
	"internal/hazard":   {},
	"internal/seismic":  {"internal/domain"},
	"internal/mineplan": {"internal/domain"},

	// The simulated mine and its processing.
	"internal/workload":     {"internal/domain", "internal/hazard", "internal/mineplan", "internal/seismic"},
	"internal/orchestrator": {"internal/domain"},
	"internal/queue":        {"internal/domain", "internal/orchestrator", "internal/workload"},
	"internal/pipeline":     {"internal/domain", "internal/seismic", "internal/workload"},
	"internal/observe":      {"internal/domain", "internal/mineplan"},
	// Debt: workload is both the jobs intent orders and the world's truth;
	// splitting the job vocabulary out of it removes this.
	"internal/intent":  {"internal/domain", "internal/hazard", "internal/observe", "internal/orchestrator", "internal/seismic", "internal/workload"},
	"internal/usecase": {"internal/domain", "internal/hazard", "internal/mineplan"},

	// Adapters to the outside.
	"internal/autoscaler":        {},
	"internal/autoscaler/astest": {},
	"internal/store":             {"internal/domain"},

	// The application. Debt: run holds the concrete autoscaler client; a port
	// it owns, with the client as one adapter behind it, removes this.
	"internal/run":    {"internal/autoscaler", "internal/buildinfo", "internal/domain", "internal/intent", "internal/observe", "internal/pipeline", "internal/queue", "internal/workload"},
	"internal/runner": {"internal/domain", "internal/intent", "internal/run", "internal/store"},
	"internal/events": {"internal/run"},

	// The HTTP interface. Debt: it takes the concrete store, manager and
	// autoscaler client; interfaces of its own remove the store and client.
	"internal/api": {"internal/autoscaler", "internal/buildinfo", "internal/domain", "internal/events", "internal/intent", "internal/observe", "internal/runner", "internal/store", "internal/usecase", "internal/workload"},

	"internal/app": nil, // the composition root sees everything
}

// The simulated mine: everything that models the mine and decides about it.
var mine = []string{
	"internal/domain", "internal/hazard", "internal/seismic", "internal/mineplan", "internal/workload",
	"internal/orchestrator", "internal/queue", "internal/pipeline", "internal/observe", "internal/intent",
	"internal/usecase",
}

// The outside: the network, the database, the application and its interface.
var outside = []string{
	"internal/autoscaler", "internal/store", "internal/run", "internal/runner", "internal/events",
	"internal/api", "internal/app",
}

// ours maps every package directory to the packages of ours that it imports.
func ours(t *testing.T) map[string][]string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("locating the module root: %v", err)
	}
	graph := map[string][]string{}
	for _, tree := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, entry os.DirEntry, err error) error {
			if err != nil || !entry.IsDir() {
				return err
			}
			pkg, err := build.ImportDir(path, 0)
			if err != nil {
				// No buildable Go files: not a package (internal/arch, the
				// migrations directory).
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			var imports []string
			for _, imported := range pkg.Imports {
				if after, found := strings.CutPrefix(imported, module); found {
					imports = append(imports, after)
				}
			}
			sort.Strings(imports)
			graph[filepath.ToSlash(rel)] = imports
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", tree, err)
		}
	}
	if len(graph) == 0 {
		t.Fatal("found no packages — this test is not looking where it thinks it is")
	}
	return graph
}

func TestEveryPackageImportsOnlyWhatItsLayerAllows(t *testing.T) {
	graph := ours(t)
	for pkg, imports := range graph {
		allowed, known := mayImport[pkg]
		if !known {
			t.Errorf("package %s is not in the layering table: add it with the packages it may import, "+
				"or the rules do not cover it", pkg)
			continue
		}
		if allowed == nil {
			continue // the composition root
		}
		for _, imported := range imports {
			if !contains(allowed, imported) {
				t.Errorf("%s imports %s, which its layer does not allow.\n  allowed: %v\n  dependencies "+
					"point inward only; if this import is right, the layering has changed and the table "+
					"and the docs change with it", pkg, imported, allowed)
			}
		}
	}
	for pkg := range mayImport {
		if _, found := graph[pkg]; !found {
			t.Errorf("the layering table lists %s, which is not a package any more", pkg)
		}
	}
}

func TestTheDomainImportsNothingOfOurs(t *testing.T) {
	if imports := ours(t)["internal/domain"]; len(imports) > 0 {
		t.Errorf("internal/domain imports %v, want nothing of ours", imports)
	}
}

// What makes a run a test of the mine's logic rather than of its plumbing: the
// simulated mine can be exercised, and replayed, with no network, no database
// and no application around it.
func TestTheSimulatedMineKnowsNothingOfTheNetworkOrTheDatabase(t *testing.T) {
	graph := ours(t)
	for _, pkg := range mine {
		for _, imported := range graph[pkg] {
			if contains(outside, imported) {
				t.Errorf("%s imports %s: the simulated mine has learned about the outside", pkg, imported)
			}
		}
	}
}

// What decides and what judges it stay apart: a planner that could see how it
// is scored could be tuned to the score, and a score that could see the
// planner could agree with it.
func TestWhatDecidesAndWhatScoresItNeverSeeEachOther(t *testing.T) {
	graph := ours(t)
	if contains(graph["internal/intent"], "internal/usecase") {
		t.Error("internal/intent imports internal/usecase: the planner can see how it is scored")
	}
	if contains(graph["internal/usecase"], "internal/intent") {
		t.Error("internal/usecase imports internal/intent: the score can see the planner")
	}
}

// The seam that makes a run a test of the autoscaler: the queue owns queue
// mechanics, the autoscaler owns scaling decisions, and neither knows how the
// other works. The run engine is where they meet.
func TestTheQueueAndTheAutoscalerNeverSeeEachOther(t *testing.T) {
	graph := ours(t)
	for _, pkg := range []string{"internal/queue", "internal/orchestrator", "internal/workload", "internal/pipeline"} {
		if contains(graph[pkg], "internal/autoscaler") {
			t.Errorf("%s imports internal/autoscaler: queue mechanics have learned about scaling decisions", pkg)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
