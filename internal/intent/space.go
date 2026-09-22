// Package intent is the mine deciding which of its queued work matters most,
// from what it knows about where events are and who is near them.
//
// What is protected is a volume, not a set of points: the union, over every
// protected person and vehicle, of the ground each is on or could be on over
// the lookahead. What that ground is comes from an observe.Whereabouts, never
// from the units' tracks: the mine reads where everyone is and where each
// vehicle is routed, but not where a person will walk, so a person's ground is
// every stretch of tunnel they could reach. An event threatens the volume when
// the ground motion it can cause reaches it, which with the hazard model's
// zones is a sphere around the event's estimated hypocentre; equivalently, a
// sphere of the zone's radius swept along the ground. The distance that
// matters is three-dimensional throughout — a crew on the level above an event
// is closer to it than the plan view suggests.
//
// An event whose zone reaches no protected ground is decayed; one whose high
// zone reaches it can be promoted. Work is never decided from a job's own
// contents, only from its event, because every pick of one event serves the
// same location.
package intent
