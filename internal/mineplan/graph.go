package mineplan

import (
	"fmt"
	"math"
	"math/rand/v2"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

// Graph is the tunnel network as something to travel through: junctions and
// bends as nodes, tunnel legs as edges.
//
// Tunnels join where they share a vertex exactly. The plan builds every
// junction that way, and a stated layout has to as well — a crosscut whose end
// merely lies close to a drive is, to this graph, not connected to it.
type Graph struct {
	points []domain.Point
	index  map[string]int
	edges  [][]edge
}

type edge struct {
	to     int
	length float64
}

// NewGraph builds the network from tunnels, keeping only those allow accepts.
// A nil allow keeps them all.
func NewGraph(tunnels []domain.Tunnel, allow func(domain.Tunnel) bool) *Graph {
	g := &Graph{index: map[string]int{}}
	for _, tunnel := range tunnels {
		if allow != nil && !allow(tunnel) {
			continue
		}
		for i := 1; i < len(tunnel.Path); i++ {
			a, b := g.node(tunnel.Path[i-1]), g.node(tunnel.Path[i])
			if a == b {
				continue
			}
			d := tunnel.Path[i-1].DistanceTo(tunnel.Path[i])
			g.edges[a] = append(g.edges[a], edge{to: b, length: d})
			g.edges[b] = append(g.edges[b], edge{to: a, length: d})
		}
	}
	return g
}

// node is the index of a point, added if new. Nodes are numbered in the order
// the tunnels first reach them, so the numbering depends on the plan alone.
func (g *Graph) node(p domain.Point) int {
	key := fmt.Sprintf("%.3f,%.3f,%.3f", p.X, p.Y, p.Z)
	if i, ok := g.index[key]; ok {
		return i
	}
	g.index[key] = len(g.points)
	g.points = append(g.points, p)
	g.edges = append(g.edges, nil)
	return len(g.points) - 1
}

// Nodes is how many junctions and bends the network has.
func (g *Graph) Nodes() int { return len(g.points) }

// Point is where a node is.
func (g *Graph) Point(node int) domain.Point { return g.points[node] }

// RandomNode is a node drawn uniformly.
func (g *Graph) RandomNode(random *rand.Rand) int { return random.IntN(len(g.points)) }

// Degree is how many legs meet at a node: one is a dead end, the face of a
// drift or crosscut, which is where work happens.
func (g *Graph) Degree(node int) int { return len(g.edges[node]) }

// Components is how many separate networks the tunnels form.
func (g *Graph) Components() int {
	seen := make([]bool, len(g.points))
	parts := 0
	for start := range g.points {
		if seen[start] {
			continue
		}
		parts++
		stack := []int{start}
		seen[start] = true
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for _, e := range g.edges[n] {
				if !seen[e.to] {
					seen[e.to] = true
					stack = append(stack, e.to)
				}
			}
		}
	}
	return parts
}

// NodeAt is the node at a point, if the network has one there.
func (g *Graph) NodeAt(p domain.Point) (int, bool) {
	i, ok := g.index[fmt.Sprintf("%.3f,%.3f,%.3f", p.X, p.Y, p.Z)]
	return i, ok
}

// Path is the shortest route between two nodes along the tunnels, as the
// points passed through, and its length. Nil if there is none.
func (g *Graph) Path(from, to int) ([]domain.Point, float64) {
	return g.Routes(from).To(to)
}

// Routes is every shortest route out of one node, for asking about many
// destinations without searching again for each.
type Routes struct {
	graph    *Graph
	distance []float64
	previous []int
}

// Routes searches out from a node once.
//
// Dijkstra without a heap: a mine plan has a few hundred nodes, and the plain
// quadratic scan breaks ties by node number, which keeps a route the same
// every time it is asked for.
func (g *Graph) Routes(from int) Routes {
	n := len(g.points)
	r := Routes{graph: g, distance: make([]float64, n), previous: make([]int, n)}
	done := make([]bool, n)
	for i := range r.distance {
		r.distance[i], r.previous[i] = math.Inf(1), -1
	}
	r.distance[from] = 0

	for {
		current := -1
		for i := 0; i < n; i++ {
			if !done[i] && !math.IsInf(r.distance[i], 1) && (current < 0 || r.distance[i] < r.distance[current]) {
				current = i
			}
		}
		if current < 0 {
			break
		}
		done[current] = true
		for _, e := range g.edges[current] {
			if d := r.distance[current] + e.length; d < r.distance[e.to] {
				r.distance[e.to], r.previous[e.to] = d, current
			}
		}
	}
	return r
}

// To is the route to a node, as the points passed through, and its length.
func (r Routes) To(to int) ([]domain.Point, float64) {
	if math.IsInf(r.distance[to], 1) {
		return nil, 0
	}
	var reversed []domain.Point
	for at := to; at >= 0; at = r.previous[at] {
		reversed = append(reversed, r.graph.points[at])
	}
	path := make([]domain.Point, len(reversed))
	for i, p := range reversed {
		path[len(reversed)-1-i] = p
	}
	return path, r.distance[to]
}
