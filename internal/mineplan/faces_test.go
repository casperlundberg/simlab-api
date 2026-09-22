package mineplan_test

import (
	"math/rand/v2"
	"testing"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/mineplan"
)

func TestAFaceIsTheWorkingEndOfACrosscutOrWhereAnOreDriveEndsInRock(t *testing.T) {
	tunnels := []domain.Tunnel{
		tunnel(mineplan.Drive, pt(0, 0, 0), pt(100, 0, 0), pt(200, 0, 0), pt(300, 0, 0), pt(400, 0, 0)),
		tunnel(mineplan.Crosscut, pt(100, 0, 0), pt(100, 150, 0)),  // ends in rock at y=150
		tunnel(mineplan.OreDrive, pt(300, 0, 0), pt(300, -80, 0)),  // ends in rock at y=-80
		tunnel(mineplan.Crosscut, pt(200, 0, 0), pt(200, 0, -150)), // joins the level below
		tunnel(mineplan.Drive, pt(0, 0, -150), pt(200, 0, -150), pt(400, 0, -150)),
	}
	got := mineplan.Faces(tunnels)
	want := []domain.Point{pt(100, 150, 0), pt(300, -80, 0)}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Faces() = %v, want %v: the drives' own ends are not faces, and a crosscut joining "+
			"two drives has none", got, want)
	}
}

func TestAGeneratedMineHasFacesToWork(t *testing.T) {
	tunnels := mineplan.Tunnels(extent(), rand.New(rand.NewPCG(3, 3)))
	faces := mineplan.Faces(tunnels)
	if len(faces) < 8 {
		t.Fatalf("a generated mine has %d faces; a plan of levels with crosscuts should have many", len(faces))
	}
	// In a generated plan every crosscut runs from the level's drive to the
	// orebody, and its working end is on the ore drive.
	for _, f := range faces {
		if onKind(tunnels, mineplan.Drive, f) || !onKind(tunnels, mineplan.OreDrive, f) {
			t.Errorf("face %v is not where a crosscut meets the orebody", f)
		}
	}
}

func onKind(tunnels []domain.Tunnel, kind string, p domain.Point) bool {
	for _, t := range tunnels {
		if t.Kind != kind {
			continue
		}
		for _, q := range t.Path {
			if q == p {
				return true
			}
		}
	}
	return false
}
