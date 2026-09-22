# Use cases

Faster processing is not the claim worth making; fast enough is. A use case
asks, of a recorded run, whether the information each of the mine's tactical
decisions needed arrived before the decision stopped being useful.

```bash
curl -X POST localhost:8081/api/runs/$RUN/use-cases \
  -d '{"kind":"turn-back","params":{"level":"moderate","window_seconds":1800}}'
```

The answer is every decision the case would have had to make in the run — for
whom, about which event, waiting on what, when it opened and when it closed —
whether each had what it needed in time, and a summary: how many decisions,
the share in time, how many never had what they needed, how many closed before
they opened (nothing could have been in time for those), and the latency and
slack of the rest.

## How a case is scored

`internal/usecase` keeps two things apart:

- **The world** is how the run really was: each event's true hypocentre and
  magnitude, each unit's track, the tunnels. A case reads it to know which
  decisions there were — who entered which zone, and when — never to make them.
- **The record** is what the mine had and when: the moment each event was first
  located and the moment all its picks were processed. What a decision needs is
  read from the record and nothing else.

A decision is in time when its need was met no later than it closed. Both
halves are pure functions of what a run stores, so any case can be asked of any
run — including runs recorded before the case existed — and the same question
always gets the same answer. Nothing is stored.

## The cases

The zone of an event is its true hazard zone at a level of ground motion, from
its true magnitude (`internal/hazard`). All three cases take:

| Parameter | Default | Meaning |
|---|---|---|
| `level` | `moderate` | The ground motion that marks the zone. Moderate is the level intent protects at, so a case asks about the ground intent acts on. |
| `kinds` | all three | Whom decisions are made for: `person`, `crewed-vehicle`, `autonomous-vehicle`. |
| `window_seconds` | 1800 | How long after an event its zone still matters — aftershocks, and rock the shaking loosened. **An assumption**, to be swept. |
| `reaction_seconds` | 30 | How long before the moment it matters a unit has to be told, to act. **An assumption**, to be swept. |

- **`turn-back`** (use case 1): a unit that enters an event's zone within the
  window, having been outside it when it happened, must have the event's first
  location a reaction time before it enters. With `kinds` set to the machines it
  is use case 10, protecting machines.
- **`way-out`** (use case 3): a unit inside the zone when the event happens must
  have its first location a reaction time before it would have left on its own
  — or, if it would not have, before the window ends.
- **`reroute`** (use case 5): a unit whose way runs into the zone can wait or
  take another path only until the last junction on the way in, so it must have
  the first location a reaction time before passing it. A unit already past its
  last junction when the event happens has no other path: that decision is
  counted, and cannot be won.

On a calibrated 24-hour workday (91 events an hour, 15 units) the moderate zone
gives about 750 turn-back and reroute decisions and 80 way-out; the high zone
fewer than 20. Scripted encounters, to study the rarer high-level decisions
without days of runs, are a scenario feature still to come.

## Adding a case

1. A type implementing `usecase.Case`: `Kind`, `Params` (the parameters it runs
   with, defaults filled in) and `Opportunities` (from the world only, in event
   and then unit order).
2. One line in `registry`, in `usecase.go`.
3. Tests first: its own, against a world built by hand, and the contract every
   case keeps (`TestEveryCaseListsItsDecisionsDeterministically...`), which a
   registered case runs automatically.
4. A new kind of information is a type implementing `usecase.Need` — it reads
   the record, never the world.
5. Its parameters in `api/openapi.yaml`, and here.
