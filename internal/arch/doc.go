// Package arch holds no code. It exists so that simlab-api's layering is
// enforced by the build rather than by everyone remembering it, as autoscaler's
// internal/arch does for autoscaler.
//
// The layers, inside out: the vocabulary (domain); the physics and geometry of
// a mine (hazard, seismic, mineplan); the simulated mine and its processing —
// the world, the queue, the workflow, what the mine can read, what it decides,
// and what its decisions are scored against (workload, orchestrator, queue,
// pipeline, observe, intent, usecase); the adapters to the outside (the
// autoscaler client, the store); the application (run, runner, events); its
// HTTP interface (api); and the composition root (app). Dependencies point
// inward only, nothing in the simulated mine knows there is a network or a
// database, and only the composition root knows the store is Postgres: the
// application and its interface declare what they need as interfaces of their
// own.
package arch
