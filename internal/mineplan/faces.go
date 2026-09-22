package mineplan

import "github.com/casperlundberg/simlab-api/internal/domain"

// Faces is where the mine is worked: the working end of each crosscut — the
// end that does not join a drive, on the orebody or in rock — and any ore
// drive that ends in rock. Development advances and stopes are worked from
// these, so this is where mining changes the stress and where the seismicity
// it induces concentrates. In the order the tunnels list them, so the same
// plan always gives the same faces.
func Faces(tunnels []domain.Tunnel) []domain.Point {
	graph := NewGraph(tunnels, nil)
	onDrive := map[int]bool{}
	for _, t := range tunnels {
		if t.Kind != Drive {
			continue
		}
		for _, p := range t.Path {
			if node, ok := graph.NodeAt(p); ok {
				onDrive[node] = true
			}
		}
	}
	var out []domain.Point
	seen := map[int]bool{}
	add := func(p domain.Point) {
		if node, ok := graph.NodeAt(p); ok && !seen[node] {
			seen[node] = true
			out = append(out, p)
		}
	}
	for _, t := range tunnels {
		if len(t.Path) < 2 {
			continue
		}
		for _, end := range []domain.Point{t.Path[0], t.Path[len(t.Path)-1]} {
			node, ok := graph.NodeAt(end)
			if !ok || onDrive[node] {
				continue
			}
			switch {
			case t.Kind == Crosscut:
				add(end)
			case t.Kind == OreDrive && graph.Degree(node) == 1:
				add(end)
			}
		}
	}
	return out
}
