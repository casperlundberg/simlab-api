# How a mine is worked

A scenario without `activity` spreads its mine's background seismicity evenly
along every drive, crosscut and ore drive, around the clock. That is not how a
mine behaves. Mining-induced seismicity concentrates around the few faces being
worked, where drilling and stope enlargement change the stress, and above all
follows blasting; quiet ground elsewhere has a low background. Where the events
are relative to the people decides how much of the queue intent can safely
relax, and when capacity is short — so a scenario can say how its mine is
worked.

```json
"activity": {
  "areas": 4, "rotate_seconds": 43200,
  "mix": {"blast": 0.3, "work": 0.5, "background": 0.2},
  "blasting": {"start_seconds": 4500, "window_seconds": 1800, "every_seconds": 600,
               "omori_p": 1.0, "omori_c_seconds": 300, "length_seconds": 43200,
               "clear_seconds": 1800, "reentry_seconds": 10800},
  "spread_m": 75
}
```

- **Faces** are the working end of each crosscut — on the orebody, or in rock —
  and any ore drive that ends in rock (`mineplan.Faces`). `areas` of them are
  worked at once, chosen afresh every `rotate_seconds`.
- **The day keeps the mine's rate.** `mix` divides it between work around the
  faces being worked, the sequences after blasts and background along the
  tunnels.
- **Blasts** are fired in a window each day: the first `start_seconds` after
  midnight, then one every `every_seconds` until `window_seconds` has passed,
  each at one of the faces worked then, in turn. Each is followed by a
  sequence decaying by the modified Omori law, n(t) = K/(c+t)^p, drawn over
  `length_seconds`.
- **Events of work and blasts** fall around their face with a spread of
  `spread_m` along each axis. Bursts still happen on top, as scenarios say.
- **Crews work the faces and leave for blasting.** People and crewed vehicles
  go to the faces being worked — a crew staying 45 minutes to two hours at a
  face on its own level where one is worked; service vehicles stopping briefly
  — and before the production areas are cleared (`clear_seconds` before the
  day's first blast) they go to their level's shaft station, returning after
  re-entry (`reentry_seconds` after the last). A crew that would still be at a
  face when clearing begins leaves early. Autonomous haulage carries on.

That gives the link between people and seismicity both signs a real mine has:
during a shift the crews are where the working induces events; at blasting,
where most of the day's events begin, they have deliberately left — and the
decision that follows is when to go back.

The activity draws from a stream of its own, so a scenario without it replays
exactly as before; the pinned job-stream digests hold that.

## Where the defaults come from

The defaults are a starting point to sweep, not findings.

- **Blasting at night, all production areas evacuated.** LKAB blasts every
  night in all major production areas at Kiruna, between 01:15 and 01:45;
  production areas are evacuated before and ventilated for several hours after
  (<https://lkab.com/en/what-we-do/our-environmental-efforts/blasting/>). The
  default window is that one, a blast every ten minutes.
- **Blasting triggers most of what closes areas; its events lie near mining.**
  In a survey of 18 seismically active mines, 90 % of re-entry incidents were
  triggered by blasting, and the events triggering them lay 50–100 m from
  mining (Vallejos & McKinnon, *Re-entry Protocols for Seismically Active Mines
  Using Statistical Analysis of Aftershock Sequences*, RockEng09, 2009) — hence a
  spread of 75 m.
- **Sequences decay over hours.** Across more than 250 mining aftershock
  sequences, power-law decay set in within an hour in 98 % and p ranged 0.4–1.6,
  with site means 0.74–1.05 (same paper); re-entry protocols wait from 2 hours
  to a 12-hour background window. Hence p = 1, c = 5 minutes and sequences drawn
  over 12 hours. Re-entry defaults to three hours after the last blast — LKAB's
  "several hours" of ventilation, within the 2–12 hours the protocols wait.
- **The mix and the number of faces** have no single right value: they are
  swept.
