# Practice History and Recognition

Design for `internal/progress` (ROADMAP Phase 6): a practice log, and additive recognition
built on top of it — session scores, a combo, a streak, and badges.

This document amends a non-goal. ROADMAP's non-goals table rejected "Gamification meta (XP,
streaks)" with "Practice tool, not a retention funnel." That rejection stands, and this
design is built to keep it true rather than to talk around it: the streak never reads a
verdict, misses cannot subtract, and badges cannot be revoked. See DECISIONS D7.

The narrowing in one line: **retention-funnel XP stays out; a practice log with additive
recognition comes in.**

---

## The problem this must not create

Gamification amplifies every false negative. A grey note on a tab is a shrug; the same
detection error that costs a streak is a reason to close the app. ROADMAP's guiding
principles already say it — "False 'you missed' feedback is the #1 rage-quit cause in this
category" — and attaching a number to a verdict multiplies the cost of being wrong.

The app is wrong more often than it looks, and every source below is already in the tree:

| Source | Evidence |
|---|---|
| **Chords are a deterministic miss generator** — the detector is monophonic, so a 3-note chord yields 3 misses on *perfect synthetic audio* | `internal/practice/roundtrip_test.go` documents exactly this |
| Sessions run **uncalibrated** — `setupListen` warns and proceeds with offset 0 | `cmd/musictutor/live.go` |
| Weak calibration — confidence varies, which is why `appconfig.ConfidenceFor` exists | `internal/appconfig` |
| Split-device clock drift — correct at minute 0, wrong at minute 20 | ROADMAP Known risks |
| Dead notes are pitchless but keep their events | `internal/score/events.go` |
| Bends are tracked but not judged | ROADMAP Phase 2, unchecked |
| Dropped capture samples under backpressure | `live.Session.DroppedSamples()` |
| Signal below the -55 dBFS gate detects as nothing | `pitch.DefaultConfig` |
| The 4 s advance lag force-misses a note held longer | `advanceLagFrames = 4 * sampleRate` |
| A seek or loop edit truncates the answering window of whatever just sounded | fixed — `Scorer.AbandonBefore`, driven by `Engine.DiscontinuityFrame` |

## The never-punish invariants

Each is structural — enforced by the shape of the code, not by remembering.

**1. The streak never reads a verdict.** A day counts as practiced on transport-playing
time (≥ 5 minutes, frame-derived) or completed passes (≥ 3). Detection accuracy is not an
input. A guitarist practicing with nothing plugged in keeps their streak. This one decision
removes most of the risk above.

**2. A credibility gate.** Verdicts feed score, combo, and badges only when the session is
live, calibrated with confidence ≥ 0.6, free of dropped samples, and above the input gate.
Otherwise the tab still tints hit/close/miss exactly as it does today — Phase 2 behaviour
is untouched — the session still counts for the streak and for time-based badges, and
nothing reaches the score. The HUD shows `unscored` and **hides the combo entirely**. A
player must never see a number they can't trust.

**3. Misses are arithmetically incapable of reducing anything.** There is no subtraction
operator anywhere in the scoring path: hit `+100 x mult`, close `+50 x mult`, miss `+0`.
The combo decays by a quarter on a miss rather than zeroing, so one false negative in a
40-note run costs 25% instead of everything, while five genuine consecutive misses still
decay it to ~24%. This is the most rage-quit-relevant number in the design.

**4. Unhittable expectations are filtered before any arithmetic.** Dropped outright,
counting as neither hit nor miss: dead notes, bends, and any result inside a transport
discontinuity the scorer does not already handle (pause and wait hold — seeks and loop
edits are abandoned at the source by `Scorer.AbandonBefore`, so no miss ever reaches here).
And **chord folding** — the recorder builds an expectations-per-start-tick map from
`sc.Events()` at session start, so any tick with two or more expectations contributes at
most one hit and *never* a miss. That directly neutralizes the documented 3-miss case.

**5. Badges are set-only bits.** Criteria are thresholds over metrics that are all counters
or best-ever values, so a satisfied criterion cannot become unsatisfied. There is
deliberately no OR and no negation in the condition type — negation is how "you lost it"
gets in. Evaluation only ever sets a bit; there is no revocation path to call.

**6. Streak forgiveness.** Two freezes, regenerating one per seven consecutive days, capped
at three; a missed day consumes a freeze instead of breaking. Past the freezes the streak
resets to **1, never 0**, and longest-ever and total-days are permanent and monotone. The
HUD leads with total days practiced — a number that cannot go down cannot produce guilt.
If the system clock reads earlier than the last recorded day, do nothing; never punish a
wrong clock. And a `comeback` badge rewards returning after a long gap, which is the
retention mechanic inverted.

**7. Score rewards slow practice.** A session bonus of up to 1.5x at 0.5x tempo, applied
once at finalization. Practicing slowly is worth more per note than grinding at full
speed — the number should point at what the tool exists to encourage.

**8. Opt-out, and never blocking.** Every setting persists, `-progress=false` disables the
layer for a run, and `G` hides the HUD. Every call from `cmd` and `ui` is best-effort: a
save failure prints one line to stderr and is never retried in a hot path. No error from
this package can fail a practice session.

## The package

`internal/progress`, importing only `internal/practice` and `internal/score` — no `engine`,
no `ui`, no `audio`. Engine facts arrive as a plain value struct, so the whole package
tests with no clock, no window, and no audio device. `ui` already imports `practice`, so
this introduces no cycle.

| File | Contents |
|---|---|
| `progress.go` | package doc, path resolution, load, atomic save |
| `schema.go` | persisted structs and migration |
| `piece.go` | piece identity: hashing and re-linking |
| `recorder.go` | live session aggregation, combo, credibility gate |
| `streak.go` | day arithmetic and forgiveness |
| `badges.go` | the declarative catalog and pure evaluation |
| `../ui/progress.go` | the `progressUI` mailbox and toasts |

The name matches ROADMAP Phase 5's own wording ("tempo progression over time") and reads
correctly at the call site. `internal/stats` would collide with `practice.Stats`.

## Persistence

A separate JSON file beside `config.json`, with `appconfig`'s conventions: atomic
temp-file-plus-rename, a versioned schema, an environment override as the test seam, and a
zero value that is a valid "nothing recorded yet" document. Add an `appconfig.Dir()` helper
so "where musicTutor keeps its state" is defined once.

The document holds settings, pieces keyed by ID, aliases, a capped session log, the streak,
badge state, and lifetime totals. Aggregates are lossless; only the per-session log is
capped, so the file stays a few hundred kilobytes no matter how long someone practices.

Settings are stored inverted (`noBadges` rather than `badges`) so the zero value is the
documented default — everything on — through a JSON round trip without pointer fields.

Days are local calendar dates, not UTC. "Did I practice today" is a local-calendar
question, and a UTC timestamp would break the streak for anyone practicing after 19:00 in
UTC-5.

**Three deliberate divergences from `appconfig`, because history is irreplaceable and a
device ID is not:**

1. **A file from a newer build is never overwritten.** Load returns the document plus an
   error; the caller warns and runs with recording disabled. Downgrading the exe must not
   truncate a year of history to an older schema.
2. **A corrupt file is quarantined, not replaced.** `appconfig` leaves a bad file in place
   and lets the next save overwrite it. Here it is renamed aside first, so a JSON glitch
   never silently deletes history.
3. **Version 0 on a non-empty file** is treated as version 1, since this release is the
   first writer.

## Piece identity

The genuine design problem: there are no piece IDs, no manifest, and no difficulty metadata
anywhere in `internal/score`. Pieces arrive as a file path on the command line.

**Chosen: a structural content hash**, over PPQ, the title, and each event's track, start,
end, and key — **excluding string and fret**. MIDI-imported fingerings come from the
swappable heuristic in `internal/fretting` (and are marked `Inferred`), so hashing frets
would detach every MIDI piece's history the day that heuristic is tuned. Excluding file
bytes means comment and whitespace edits to a `.gtab` don't split history, and the same
riff authored as `.gtab` and as `.mid` hashes identically — which the existing fixture
corpus can assert directly.

| Option | Verdict |
|---|---|
| Absolute path | Rejected as primary — a moved music folder wipes all history. Kept as re-link evidence. |
| Hash of file bytes | Rejected — a re-save or a comment changes the ID, and `.gtab` files are hand-edited constantly. |
| Title from the header | Rejected as primary — frequently empty after MIDI import, and it collides. Kept as evidence. |
| **Structural hash** | Path- and format-independent; survives cosmetic edits and fretting changes. **Cost: a genuine note edit splits history.** |

That cost is mitigated by storing identity evidence — title, last path, basename, bar and
event counts — and re-linking on a title match, or a basename match with bar count within
±2 and event count within 10%. Re-links are recorded as permanent aliases. A `\id` header
directive in the text format is the exact fix for authored pieces and is deferred to later
staging, as an override that beats the hash.

Sections key on their loop tick pair, which `loopSetA`/`loopSetB` already keep bar-aligned.

## Where it subscribes

There is exactly one `SetEventTap` slot; the scorer owns it; it runs on the render
goroutine with the engine lock held. **The meta layer does not touch it and does not need
to** — the musical schedule is available statically from `sc.Events()` at session start,
and every `NoteResult` already carries its `Event` and `OutFrame`.

So the recorder's only live inputs are the `[]practice.NoteResult` slice already produced
in `onNotes` — the same slice `app.OfferResults` receives — and cheap lock-free polls of
the engine passed in as a plain `EngineState` value. Zero new contact with the render
goroutine. A tap multiplexer stays deferred.

This matters for a reason beyond threading: the recorder becomes a third consumer of the
result **stream**, not a third `Stats` tally. There are already two (the scorer's and the
UI's) and a third would be a bug waiting to happen.

Practice time counts in **frames, not `time.Now()`**. One clock applies to the meta layer
too, and it makes every duration test deterministic.

## The advance lag

At any instant the last ~4 seconds of expectations are unresolved, so a live combo has to
choose what to show.

Hits and closes finalize *promptly* — the scorer matches the moment a detection closes, and
the round-trip test measures 20–60 ms. Only misses arrive late, from `Advance`, four
seconds behind. So an **optimistic** combo is nearly correct and errs in the player's
favour, which is the right direction. The honest artifact is that a combo can tick up and
then dip about four seconds later; the gentle quarter-decay makes that a dip rather than a
collapse, and milestone celebrations are toasts, which are asynchronous anyway.

**At shutdown, do not drain the pending expectations.** Calling `Advance` with a huge frame
would fabricate misses for notes the player very likely hit — the single worst thing this
feature could do. Losing the last few seconds from the statistics is strictly better than
inventing failures.

Loop passes need the same care. `PassCount()` has no callback and must be polled, results
for pass N keep arriving up to four seconds after the boundary, and `PassCount` resets to 0
whenever the loop changes. So per-pass statistics bin each result by its own `OutFrame`,
not by the pass count at arrival time, and a loop change opens a new practice block rather
than trusting a monotone counter.

## Badges

Ten, as a table. The design property worth stating out loud: **five need no detection at
all**, so the layer still has something true to say with the detector off, uncalibrated, or
untrusted.

| Badge | Criterion | Needs detection |
|---|---|---|
| Slow and Clean | a clean pass at ≤ 0.7x, accuracy ≥ 0.95, ≥ 8 judged notes | yes |
| Up to Speed | a tempo ramp spanning ≥ 0.3 ending at full speed, accuracy ≥ 0.9 | yes |
| Section Mastered | three consecutive credible passes of one loop region at ≥ 0.95, full speed | yes |
| Nailed It | one full-piece pass at ≥ 0.98, full speed | yes |
| Repertoire | five distinct pieces with at least one clean pass | yes |
| Five More Minutes | 20 minutes of transport-playing time in one session | no |
| Under the Speed Limit | 60 cumulative minutes at ≤ 0.75x | no |
| Friend of the Click | 60 cumulative minutes with the metronome on | no |
| Seven Days / A Month | streak of 7 / 30 | no |
| Back at It | returned after a gap of 14 days or more | no |

The detection-dependent ones reward *deliberate practice* — slow accurate passes, completed
ramps, mastered sections — rather than grinding. `Back at It` deliberately rewards the
thing a retention funnel would punish.

Criteria are evaluated against a metric snapshot built once per finished session, which is
what keeps evaluation a pure, table-testable function.

## In the UI

Mirror the `liveUI` pattern exactly — a mutex-guarded mailbox written by other goroutines,
drained by `syncLive` on the game loop, with a zero value that is fully inert. Reviewers
will expect it, and `TestNilSafety` already asserts that property for the live layer.

Against the real layout: **score and streak append to `line1` in `drawHUD`**, which already
composes conditionally (`| LOOP`, `| click`, `| ramp`), so there is no layout risk and it
is the cheapest first landing. **The combo goes top-centre at y=60** — deliberately not
near the playhead, where `COUNT-IN` and `WAITING` already sit. **Toasts stack bottom-right**
above the legend, three at a time, expiring on the existing frame counter. `unscored`
replaces the combo when the credibility gate is closed. `G` toggles the whole block.

The app quits immediately on `Q`, so an in-app end-of-session summary needs a new draw
branch. The first landing prints the summary to stdout instead — trivially testable, no UI
risk — and the overlay comes later.

## Testing

The store tests mirror `appconfig`'s suite in shape (env override honoured, missing file is
not an error, round trip, corrupt file, no temp litter), plus two new ones for the
divergences above.

The flagship is **`TestMissNeverReducesAnything`**: for a table of result sequences,
injecting an extra miss at every position never lowers the final score, the combo, any
best-ever value, or any badge's progress. That test *is* the contract in invariant 3, and
it is the one to write first.

Alongside it: `TestChordProducesNoMiss` against the exact 13-hits-and-3-chord-misses shape
`roundtrip_test.go` documents; `TestPassBinningUnderAdvanceLag`;
`TestLoopChangeStartsNewBlock`; `TestSeekDiscardsStaleResults`;
`TestUncalibratedSessionScoresNothing`; `TestBadgesAreMonotone` (raising any metric never
un-earns); pure date arithmetic for streaks with an injected time, covering freezes, DST,
year boundaries, and a backwards clock; and a concurrency smoke test mirroring
`TestFeedsConcurrent`, where `-race` in CI is the real assertion.

`TestPieceIDMatchesAcrossFormats` reuses the existing `.gtab`/`.mid` fixture corpus and
validates the exclude-string-and-fret decision end to end.

The money test for this layer lives in `internal/integration`: the existing offline round
trip through engine, synth, detector, tracker, and scorer, with its results fed to a real
recorder — asserting a non-zero score, the right number of counted notes, and **zero combo
damage from the chord**. It reuses machinery that already exists and runs headlessly and
deterministically on both CI platforms.

## Staging

- **Stage 1 — the practice log, with no gamification visible.** Store, schema, migration, piece identity, session capture, aggregates, and a streak driven by practice time. Prints a session summary to stdout; no UI changes. This alone delivers ROADMAP Phase 5's "Practice history" line and is worth shipping on its own.
- **Stage 2 — score, combo, HUD.** The credibility gate, the live score, the `line1` additions, the `G` toggle, `-progress=false`.
- **Stage 3 — badges.** The catalog, evaluation, persistence, and toasts.
- **Stage 4 — polish.** The in-app summary overlay, per-section tempo-progression sparklines (the literal Phase 5 wording), a `\id` directive in the text format, and piece-browser integration once that UI exists.

## Known dependencies

- `advanceLagFrames` is 4 seconds and lives in `cmd/musictutor`.
- `score.Score` has a title but no artist or composer field, so identity evidence is thinner than it looks and re-linking leans on basename plus structural size.

See the [ROADMAP](../ROADMAP.md) for phasing, and [DECISIONS](DECISIONS.md) D7 for why this
exists at all. The companion document for the highway view is [HIGHWAY.md](HIGHWAY.md).
