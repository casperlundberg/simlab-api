# Intent: the mine reordering its own work

A seismic processing queue holds picks. Every pick of one event serves the same
thing — where that event was and how large — and what makes one event's picks
more urgent than another's is whether anyone is near it. Intent is the mine
using what it knows about that to reorder work it has already queued.

## What is protected

The protected volume is the union, over every protected person and vehicle, of
where each is now and where its planned route takes it over the **lookahead**.
An event threatens that volume when the ground motion it can cause reaches it.

Ground motion comes from the Canadian Rockburst Support Handbook's scaling law
(`internal/hazard`): `ppv = C*·√(10^(mN+1))/R`, falling as 1/R from the
hypocentre and rising √10-fold per unit of magnitude, with R three-dimensional.
For a level of ground motion — moderate above 0.01 m/s, high above 0.1, very
high above 1 — that law gives a radius, widened by how far the location may be
out. So an event threatens a path when the path passes within that radius:
spheres around the event, or equivalently, spheres of that radius swept along
every protected route.

| Setting | Default | What it decides |
|---|---|---|
| `mode` | `decay` | `off`, `decay`, `promote` or `both`. |
| `protect` | all three kinds | Which people and vehicles count. |
| `lookahead_seconds` | 300 | How far along its route an entity is protected. 0 is where it is now. |
| `protect_level` | `moderate` | An event whose zone at this level reaches no path **decays**. |
| `promote_level` | `high` | An event whose zone at this level reaches a path can be **promoted**. |
| `location_uncertainty_m` | 50 | Widens every zone from a location. |
| `margin_m` | 0 | Widens every reach further. |
| `decay_to`, `promote_to` | 0, 400 | Where work moves. Decay never raises a job; promotion never lowers one. |
| `knowledge` | `estimate` | The mine's locations, or `truth` — the oracle arm. |
| `pre_location` | false | Act before a location, from the first sensor to trigger. |
| `pre_location_magnitude` | 1.5 | The magnitude assumed before one is estimated. |
| `deadline_from` | `arrival` | A moved job's deadline is measured from arrival, or from the change. |
| `restore` | true | Decayed work returns when something protected comes within reach. False makes decay final. |
| `burst_exempt` | none | `decayed`, `promoted` and/or `restored` work may not be the reason cloud is bought. |

Decay is the default because it relaxes: it makes nothing more urgent. That is
only wholly true of decay itself. Decayed work that is **restored** — because a
vehicle is now heading towards its event — comes back to its submitted level
having waited all along, and with deadlines measured from arrival it is often
already late. To the autoscaler a waiting job that is already late is a breach
no capacity avoids, and it runs flat out; each restore also restarts the cloud
tier's minimum lifetime and the scale-down cooldown. On the verification
scenario, restores kept the cloud tier at its cap for about 110 cycles longer
than without intent. `restore: false`, `deadline_from: change` or
`burst_exempt: [restored]` each remove that, at different costs.

## What intent knows

Under `estimate`, an event is judged from the mine's first location — solved
from its first four processed picks — and the mean of those picks' magnitude
readings. Before that it is unknown and its work keeps its submitted priority,
unless `pre_location` is on: then the first sensor to trigger stands in for the
hypocentre, and the distance from it to the fourth sensor to trigger stands in
for how far out that may be. With a sparse array that allowance is large, and
pre-location rarely rules anywhere out.

Under `truth`, events are judged from where they really were, with no
allowance, from the moment they happen. No real mine has this; it bounds what
ordering by location could achieve. Nothing under `estimate` reads the truth,
and `TestUnderEstimatesIntentNeverReadsTheTruth` holds that.

Planned movement is taken as known over the lookahead: autonomous haulage runs
to a dispatch plan, and crews to work assignments. That is an assumption about
how well a mine knows its own plans, and the lookahead is the dial for it.

## What moves, and when

Every cycle, after the interval's completions are in and before the autoscaler
is shown the queue, every event with work outstanding is judged again. Work
moves when what is wanted for it changes — decayed as an entity drives away,
restored as one approaches — and a job an executor has already started simply
finishes: without preemption, moving it would change which deadline it is
judged by and not when it completes.

A level is kept in deadline order. With `deadline_from: arrival` a moved job
keeps its submission as its origin, so a promotion cannot hide a wait: a job
promoted past its new deadline is late at once, which to the autoscaler is a
breach no capacity avoids, and it runs flat out. That is emergency scaling.
`burst_exempt: [promoted]` is how an operator says not to pay for it; with
`deadline_from: change` the job starts a fresh deadline at its new level.

## Exempt from cloud burst

Exempt work reaches the autoscaler separately (`burst_exempt` in its workload).
It is served like any other work and may use all local capacity, but a breach
of it alone never buys cloud. It still takes capacity from work behind it, and
that work's deadline is still a reason to burst. Exempt work that is late is
still counted as a breach: exemption is a decision to let it be late rather
than pay, not a way of hiding that it was.

## Changing it while a run is in flight

`PATCH /api/runs/{id}/intent` changes intent from the run's next cycle, with
`expected_version` for compare-and-swap. Every version that takes effect is
recorded with its cycle (`GET /api/runs/{id}/intent`), and replaying those as an
`intent_schedule` reproduces the run exactly — including hand edits made while
watching it. A schedule is also how an experiment switches intent mid-run
deterministically.

## What is recorded

- per cycle, `intent`: the version in force, waiting jobs decayed, promoted and
  exempt, and how many moved or were too late to move;
- per event, `intent`: every change of judgement, with what it was judged from,
  the nearest protected entity, how near, and how far the deciding zone reached;
- per run, `sla_breaches` against the level each job was served at, and
  `sla_breaches_as_submitted` against the level it arrived at, from arrival —
  what decay cost against the SLA work was submitted under.

## Limits worth stating

- The zone radius is an upper bound for design (C* = 0.25 m²/s, the handbook's
  90–95 % design relationship), not a prediction, and the source is taken as
  radiating in the worst direction.
- One location, from four picks, decides an event for the rest of its work; a
  better location from more picks is not used until the event is finished.
- The arrival rate the autoscaler sees is by submitted priority: work that
  intent will decay a minute later still arrives as urgent.
