package intent_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/intent"
	"github.com/casperlundberg/simlab-api/internal/mineplan"
	"github.com/casperlundberg/simlab-api/internal/observe"
	"github.com/casperlundberg/simlab-api/internal/seismic"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// junction is two drives crossing where a person stands at the start of the
// run, about to walk east, and one event of the given magnitude somewhere on
// the level.
func junction(magnitude float64, at domain.Point) workload.Workload {
	tunnels := []domain.Tunnel{
		{ID: "east-west", Kind: mineplan.Drive, Path: []domain.Point{{X: 0, Y: 500, Z: -500}, person, {X: 1000, Y: 500, Z: -500}}},
		{ID: "north-south", Kind: mineplan.Drive, Path: []domain.Point{{X: 500, Y: 0, Z: -500}, person, {X: 500, Y: 1000, Z: -500}}},
	}
	walker := domain.Entity{ID: "person-01", Kind: domain.EntityPerson, Track: []domain.Waypoint{
		{At: 0, Point: person},
		{At: 400 * time.Second, Point: domain.Point{X: 900, Y: 500, Z: -500}},
		{At: 24 * time.Hour, Point: domain.Point{X: 900, Y: 500, Z: -500}},
	}}
	layout := domain.Layout{
		Extent:  domain.Extent{Min: domain.Point{Z: -1000}, Max: domain.Point{X: 1000, Y: 1000}},
		Sensors: sensors(),
		Tunnels: tunnels,
	}
	w := workload.Workload{Layout: layout, Model: workload.Rock, Entities: []domain.Entity{walker}}
	origin := 10 * time.Second
	picks := workload.Rock.Picks(seismic.Event{At: at, Origin: origin}, layout.Sensors, 0, nil)
	for k := range picks {
		picks[k].Magnitude = magnitude
	}
	w.Events = []workload.Event{{Origin: origin, Truth: at, Magnitude: magnitude, Picks: picks}}
	for range picks {
		w.Jobs = append(w.Jobs, workload.Job{ID: domain.JobID(len(w.Jobs)), SubmittedAt: origin, Priority: 50, Seconds: 10})
	}
	return w
}

func truthful(w workload.Workload) intent.Views {
	tracks := observe.NewTracks(w.Entities)
	return intent.Views{Mine: tracks, Oracle: tracks}
}

func realistic(w workload.Workload) intent.Views {
	return intent.Views{
		Mine:   observe.NewPositioned(w.Entities, w.Layout.Tunnels, workload.WalkingSpeed),
		Oracle: observe.NewTracks(w.Entities),
	}
}

// 180 m up the northern drive, a Nuttli 0 event: its moderate zone, widened by
// the 50 m a location may be out, reaches about 130 m. The person's true route
// east never comes within 180 m of it; the northern drive they could have
// taken runs straight through it.
var upTheNorthDrive = domain.Point{X: 500, Y: 680, Z: -500}

func judged(t *testing.T, w workload.Workload, s domain.IntentSettings, views intent.Views) domain.IntentTransition {
	t.Helper()
	c := workload.NewCatalogue(w)
	// Enough picks to locate it, with work left for intent to order. Five, not
	// four: four of these symmetric corner sensors leave the solve at the
	// array's centre, which is where the person stands.
	process(t, w, c, 20*time.Second, 0, 5)
	state, ok := stateOf(intent.New(w, c, s, views).Plan(20*time.Second), 0)
	if !ok {
		t.Fatalf("the event was not judged")
	}
	return state
}

func TestAPersonIsProtectedWhereTheyCouldWalkNotWhereTheSimulatorWillWalkThem(t *testing.T) {
	w := junction(0, upTheNorthDrive)

	if got := judged(t, w, domain.DefaultIntent(), realistic(w)); got.State != domain.EventKept {
		t.Errorf("state = %s, want kept: the mine cannot know the person will turn east rather than "+
			"north, and 180 m up the northern drive is well within 300 s of walking", got.State)
	}
	if got := judged(t, w, domain.DefaultIntent(), truthful(w)); got.State != domain.EventDecayed {
		t.Errorf("state = %s under the true tracks, want decayed: the route east never comes within "+
			"its zone — this is the foresight the mine does not have", got.State)
	}
}

func TestTheOracleArmStillDecidesFromTheTruth(t *testing.T) {
	w := junction(0, upTheNorthDrive)
	if got := judged(t, w, settings(`{"knowledge":"truth"}`), realistic(w)); got.State != domain.EventDecayed {
		t.Errorf("state = %s, want decayed: the oracle knows where the event is and where the person "+
			"is going", got.State)
	}
}

// What the planner may read about where people and vehicles are is whatever
// Whereabouts it is given. Reading a unit's track directly would hand it the
// simulator's foresight whatever view it was wired with, and nothing would
// say so — so the planner's own code may not touch one.
func TestThePlannerReadsWhereUnitsAreOnlyThroughAView(t *testing.T) {
	set := token.NewFileSet()
	packages, err := parser.ParseDir(set, ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing the package: %v", err)
	}
	forbidden := map[string]string{
		"Entities":   "a workload's units",
		"Track":      "a unit's track",
		"PositionAt": "a position read from a track",
	}
	checked := 0
	for _, pkg := range packages {
		for name, file := range pkg.Files {
			checked++
			ast.Inspect(file, func(n ast.Node) bool {
				selector, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if what, bad := forbidden[selector.Sel.Name]; bad {
					t.Errorf("%s: reads %s (.%s); the planner may know where units are only through the "+
						"observe.Whereabouts it is given", set.Position(selector.Pos()), what, selector.Sel.Name)
				}
				if x, ok := selector.X.(*ast.Ident); ok && x.Name == "domain" && selector.Sel.Name == "Entity" {
					t.Errorf("%s: names domain.Entity in %s; the planner's view of units is observe's",
						set.Position(selector.Pos()), name)
				}
				return true
			})
		}
	}
	if checked == 0 {
		t.Fatal("no source files were checked, so nothing was proved")
	}
}
