# The Note Highway

Design for the perspective note-highway view (ROADMAP Phase 6). The highway is a **second**
practice view, not a replacement: the scrolling tab stays first-class and stays the default.

This document amends a non-goal. "3D note highway" was rejected in ROADMAP's non-goals
table on two grounds — cost, and that a 2D tab transfers to reading real tabs. Both
survive here: the highway is one file over the existing score model, and the tab it sits
beside is untouched. See DECISIONS D7 for the full argument.

---

## Why, when the tab already scrolls

`drawTab` (`internal/ui/ui.go`) maps ticks to pixels linearly — `pxPerTick() = zoom * 96 /
PPQ` — so scroll speed is proportional to BPM. At 60 BPM the tab crawls; at 200 BPM it
flies. That is correct for *notation*, where bar spacing is musically meaningful, and
wrong for *reading approach timing*, where the question is "how long until my hand has to
move." Those are different questions and they want different views.

The highway answers the second one. Notes approach the player down string lanes and grow
as they near a hit line, so distance on screen means time, always, at every tempo.

## The depth axis is real seconds, not ticks

The single load-bearing decision in this document.

```go
k  := eng.TempoScale()
t0 := sc.Tempos.TimeAt(pos)
dt := (sc.Tempos.TimeAt(noteStart) - t0) / k   // real seconds; < 0 means passed
```

`score.TempoMap.TimeAt` and `TickAt` (`internal/score/tempomap.go`) already integrate
tick↔seconds across tempo changes, and they are documented as working at *nominal* tempo —
practice-speed scaling is deliberately the engine's concern. That is why the `/k` is ours
and not theirs.

What falls out, all of it wanted:

- Approach velocity is identical at 60 and 200 BPM. Only note *density* changes.
- Slowing to 0.5× keeps approach velocity identical and spreads notes twice as far apart —
  precisely what a speed trainer is for. Ticks on the depth axis would instead make the
  whole highway crawl, which is the opposite of useful.
- A mid-piece tempo change becomes a smooth density change, not a velocity discontinuity.
- Nothing here is a clock. `pos` comes from `eng.PosTick()`, polled in `Draw`; `TimeAt` is
  a pure function of the score. The rule in the `internal/ui` package doc — the UI never
  keeps its own notion of time — holds unchanged.

## Projection

One-point perspective. Making depth linear in real time collapses the whole projection to
a single scalar with one tunable:

```
s(dt) = 1 / (1 + dt/tau)                        // s(0) = 1, s(tau) = 0.5
x(dt) = vpX + (laneNearX(i) - vpX) * s(dt)
y(dt) = vpY + (hitY         - vpY) * s(dt)
```

Everything contracts toward the vanishing point by `s`, which is exactly one-point
perspective — so straight world lines stay straight on screen. Lane rails are plain
`vector.StrokeLine` calls from the near deck to the vanishing point, and bar rungs are
horizontal lines whose endpoints use the same contraction.

`Layout` returns a fixed 1280x720 logical size, so these can be constants:

| Constant | Value | Why |
|---|---|---|
| `hwVanishX` | 640 | screen centre |
| `hwVanishY` | 210 | clears `drawHUD`'s track list, which reaches y≈181 at 9 tracks |
| `hwHitY` | 520 | leaves a 310 px perspective band |
| `hwDeckHalfW` | 470 | near deck spans x ∈ [170, 1110] |
| `hwLookaheadSec` | 2.5 | seconds of music on the highway at zoom 1 |
| `hwTauFrac` | 0.45 | `tau = 0.45 * L`, so the curve's shape is zoom-invariant |
| `hwApronH` | 130 | the past region; its bottom at y=650 clears the legend at 676 |
| `hwApronTau` | 1.2 s | past compression rate |
| `hwPastSec` | 5.0 | how far back notes stay drawn — see Latency honesty |

Derived: `L = hwLookaheadSec / zoom`, `tau = hwTauFrac * L`, `laneW = 2*hwDeckHalfW/nStr`.

Zoom scales `L` and `tau` together, so zoom only sets how many seconds fill the highway
and apparent speed comes out exactly linear in zoom — `dy/d(dt)` at the hit line is
`275.6 * zoom` px/s. That matches the tab, where zoom likewise doubles scroll speed. The
existing `+/-` handling and its [0.3, 4] clamp need no change; `L` then ranges 0.63–8.3 s.

**The vertical budget is tight.** `drawHUD` owns y ≲ 181 (the track list) and y ≥ 676 (the
legend and help line), leaving the highway roughly y ∈ [200, 660]. Changing the constants
above means re-checking against those.

### The past region

`s(dt)` diverges at `dt = -tau`, so negative depth cannot use the same formula. Below the
hit line the deck flattens into an orthographic apron — the road passing under the camera:

```
dt < 0:  s = 1
         x = laneNearX(i)                                  // parallel, no convergence
         y = hitY + hwApronH * (1 - exp(dt/hwApronTau))    // monotone, bounded
```

Recent past moves fast and old past compresses into the bottom margin. This is where
verdicts live, and it is why the apron exists at all.

## Latency honesty

**The hit line makes no claim about the player. Verdicts only ever exist behind it.**

This is the most important rule in the view, and it is forced by numbers already in the
tree, not by taste:

- `advanceLagFrames = 4 * sampleRate` in `cmd/musictutor/live.go`. A miss is finalized
  **four seconds** after the note sounded, deliberately, because the tracker only reports
  a note when it *closes* and a sustained note's detection arrives its own duration late.
- Even a perfect hit carries `ErrFrames` of +1000..3000 (21–63 ms) — measured in
  `internal/practice/roundtrip_test.go` with synthetic audio and zero real-world latency.
- ROADMAP Phase 2 already states the principle: feedback renders against the timeline, not
  "instantly."

So the color rule is:

| Condition | Appearance |
|---|---|
| engine is waiting on this note | pulse (`pulseCol`), filled |
| `dt >= 0` and the note is sounding now | `colSounding`, filled |
| `dt >= 0`, inferred fingering | `colInferred`, outline |
| `dt >= 0` otherwise | `colNote`, outline |
| `dt < 0` with a verdict | `verdictColor(v)`, filled |
| `dt < 0` awaiting judgment | neutral, outline |

Three properties follow. The impact animation at the hit line fires on the engine's own
"this note is sounding" test — instantaneous and true, and never a claim about the player.
An approaching note is always a neutral outline; nothing green ever appears in front of
you. And verdicts visibly *settle* in the apron, seconds later, at the timeline position
the note actually occupied — so the player learns where the latency is instead of being
lied to about it.

`hwPastSec` must therefore exceed the advance lag with slack, or misses arrive after their
note has been culled. 5.0 s against a 4 s lag. That constant currently lives in `cmd` and
`internal/ui` cannot see it — exporting it from `internal/practice` is the tidy fix and is
listed under Known dependencies.

**The same rule already fixed a bug in the tab.** `syncLive` never clears `a.verdicts` and
`practice.NoteResult` carries no pass number, so on loop pass 2 the tab used to paint
pass 1's verdict onto a note you had not played yet. The fix was this rule, one dimension
down: `verdictAt` in `internal/ui/live.go` refuses to return a verdict for a note the
playhead has not reached. Clearing the map at the loop boundary would not have been enough
on its own — the previous pass's results are still arriving four seconds into the next
one. The highway's `dt < 0` gate is the same idea, so both views share one rule.

## What this reuses

Nothing in `internal/ui/live.go` changes. `noteKey`, `verdicts`, `verdictColor`,
`pulseCol`, `waitingKeys`, `keyNames`, `drawText`, `drawTextScaled` and the
`colHit`/`colClose`/`colMiss`/`colWaitLo`/`colWaitHi` palette are all reused as they are.

**Wait mode is nearly free.** Depth derives from `PosTick()`, and wait mode freezes it, so
the waited-on note pins at `dt = 0` — full size, on the hit line, highway stopped. No
special case. It needs only a pulsing note head, a beam up the lane rail, and the existing
`WAITING` banner. Call `waitingKeys()` once per `Draw` and thread the result through: it
allocates a map, and `Engine.WaitingOn` allocates a slice, so it is already two allocations
per frame while waiting.

**String order** follows `drawTab`: string 1 is the highest pitch, and `Track.Tuning` is
high-string-first. `laneOf(str, nStr, mirror)` puts the low strings outside-left, the way a
right-handed player sees the neck. Keep `mirror` unexported and false until someone asks.

Tuning and capo are a real gap — `drawTab` shows neither — and the highway has the natural
place for them: a note letter under each lane's near end, from the existing `keyNames`
table, plus `capo N` in the HUD.

## Update versus Draw

`Update` runs at 60 TPS; `Draw` runs at display refresh. The split matters:

- **Motion** comes from `eng.PosTick()` read in `Draw`, so it stays smooth at 144 Hz.
- **Animation envelopes** come from `liveUI.frame`, so decay rates don't change with the
  monitor. `pulseCol` already works this way.

Getting this backwards produces either judder or refresh-dependent animation speed. Note
detection (which notes crossed the hit line since last tick) belongs in `Update` too, so
`Draw` stays pure and the logic stays headlessly testable. The "previous position" it
compares against is *the last sample of the engine's clock*, never an independent one — it
cannot advance on its own.

## Files

| File | Contents |
|---|---|
| `internal/ui/highway.go` | constants, `hwProj`, projection, `updateHighway`, `drawHighway` |
| `internal/ui/highway_test.go` | headless projection, window, and verdict-timing tests |
| `internal/ui/ui.go` | five small edits: an `hw` field, a `KeyH` case (**H is free**), the `updateHighway()` call, a `Draw` dispatch extracted as a testable `mode()`, and help text |

## Staging

- **Stage 0 — smoothness prerequisite.** `readBlockFrames = 2048` in `internal/engine/engine.go` means `PosTick()` advances in ~43 ms steps on the oto path, which is 12 px of stair-step at highway speeds. Subtle on the tab, glaring here. `internal/live` is already fine at `DefaultPeriodFrames = 480`.
- **Stage 1 — the honest playable view.** Projection, deck, lane rails, hit line, bar rungs, note heads, the apron, the verdict rule, wait mode, tick window, painter order. Complete and usable.
- **Stage 2 — highway feel.** Sustain trapezoids from `beat.Dur`, tied-beat tails, impact flash rings, loop-region deck shading, tuning letters, fret labels.
- **Stage 3 — polish.** Display smoothing if stage 0 wasn't enough, the exported advance-lag constant, `sort.Search` bar lookup and a `TimeAt` prefix cache for dense tempo maps, per-pass verdicts.

## Testing

`internal/ui/ui_test.go` never opens a window — it builds a real engine over a fixture
score and calls methods directly. Every projection function above is pure or touches only
`eng`/`sc`/`zoom`, so the same pattern covers all of it. Extracting `mode()` exists
precisely because `inpututil.IsKeyJustPressed` is untestable.

The load-bearing tests:

- `TestHighwaySpeedIndependentOfBPM` — a note 1.0 real second ahead projects to the same screen y in a 60 BPM and a 180 BPM score. This is the direct test of the depth-axis decision.
- `TestHighwaySpeedIndependentOfTempoScale` — at 0.5× the same holds, while a note at a fixed *tick* moves farther away.
- `TestHighwayNoVerdictBeforeHitLine` — pins the latency rule: a recorded verdict must not color a note at `dt > 0`.
- `TestHighwayStaleVerdictOnNextPass` — the same note re-approaching renders neutral.
- `TestHighwayNoAllocs` — via `testing.AllocsPerRun`, because this runs every frame.

Plus projection monotonicity, horizon safety, apron bounds, zoom behaviour, tick-window
clamping, flash-ring bounds, no flash burst on seek, lane bijection, and view precedence.

## Gotchas

1. **`sc.Events()` allocates** — a slice and two maps per call. Never call it from `Draw`; walk bars and beats the way `drawTab` does, with its cheap bar-range skip.
2. **Iterate bars descending.** Ascending tick means increasing depth, and the painter's algorithm needs far drawn first so near notes overdraw. This is the one structural difference from `drawTab`.
3. **`image/color.RGBA` is alpha-premultiplied** and `vector` calls `clr.RGBA()` directly, so a fading color must be built premultiplied. Note `colLoop = {60,160,90,60}` violates this today (R > A), so the existing loop shading already renders wrong — fix it if the highway reuses it.
4. **`basicfont` is a bitmap face.** Only integer `GeoM.Scale` looks right; `drawTuner` only ever uses 3.0 and 6.0. Fret labels need integer scales, dropping to no label when notes get small.
5. **`barAt` is an O(bars) linear scan** already running every `Update`. Fine now; `sort.Search` is the fix if long pieces ever matter.
6. **`TimeAt` is O(tempo changes)** per note, so a densely-automated MIDI import costs O(visible notes x tempos) per frame. Microseconds today, prefix-sum cache later.

## Known dependencies

- `readBlockFrames` (stage 0 above).
- `advanceLagFrames` lives in `cmd/musictutor`, where `internal/ui` cannot see it; the apron is sized against it by hand until it moves to `internal/practice`.
- `colLoop` is not alpha-premultiplied.

See the [ROADMAP](../ROADMAP.md) for phasing, and [DECISIONS](DECISIONS.md) D7 for why this
view exists at all. The companion document for scores and badges is
[PROGRESS.md](PROGRESS.md).
