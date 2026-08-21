# Bug sweep — round 2

Date: 2026-08-21, immediately after [round 1](ROUND1.md). Method: three
fresh reviews over the round-1-fixed tree, this time **allowed to cross
package seams** (round 1 was package-locked, which is exactly where its
unproven suspects lived). Each review re-attacked round 1's fixes in its
domain, was handed round 1's "suspected but unproven" list as promotion
targets, and hunted fresh — importers and the text format under renewed
mutation, structured, and round-trip fuzzing (the round-trip fuzzer alone
ran ~12M executions). **20 verified bugs found and fixed** in three
commits (`8cba5b5`, `eb7c6ea`, `425e677`). Every round-1 fix was
re-verified sound; one had a catchable sibling gap (finding 12 below).

## Findings

### Editor and shell (6) — commit `8cba5b5`

| # | Sev | Where | Bug | Fix |
|---|-----|-------|-----|-----|
| 1 | med | `ui/editorchrome.go` | Enter on an untouched prompt applied the seed it was showing — a no-op that marked the piece dirty, and for the tempo prompt applied the **rounded display over the exact value** (120.3 became 120) | Untyped numeric prompts dismiss; an unchanged title skips the apply |
| 2 | med | `edit/notes.go` `ClearNote` | Delete with nothing on the cursor's string still ran a mutation: dirty flag, one undo snapshot per key repeat, redo stack destroyed — for zero change | Returns before the mutate when there is nothing to clear |
| 3 | low | `ui/editor.go` `clickGrid` | A wheel flick and a click in one frame mapped the click through a scroll value no frame had drawn; past the ends the click went dead | `clickGrid` clamps the way Draw does |
| 4 | med | `ui` + `cmd` (promoted) | A practice open failing from the editor went to stderr only — invisible in a window | `Editor.ShowError`, the mirror of the browser's, wired from the shell |
| 5 | med | `appconfig` + `ui` (promoted) | Hand-edited `countInBeats: 999` reached the engine raw while settings displayed 8 | Clamped in config against the same exported bound the UI enforces, pinned by an agreement test like the sync-trim pair |
| 6 | low | `edit/notes.go` (promoted) | The too-high wind pitch refusal blamed "G9" — which is the accepted top, not the note asked for | Message names the offending note and the true ceiling |

### Engine and synthesis (1) — commit `eb7c6ea`

| # | Sev | Where | Bug | Fix |
|---|-----|-------|-----|-----|
| 7 | high | `engine/render.go` + `synth/soundfont.go` (promoted) | The engine suppressed the origin's NoteOff for every legato continuation, but the SoundFont voice **refuses** a takeover wider than its bend range and attacks afresh — the origin then sounded **forever**, unfindable even by score-end cleanup. Proven: a 20-semitone slide left the low E ringing through a written rest | The voice reports whether it actually continued (optional interface, `Articulator` contract untouched); the engine pays the owed NoteOff on refusal. Pinned in rendered audio |

### Importers and format (13) — commit `425e677`

| # | Sev | Where | Bug | Fix |
|---|-----|-------|-----|-----|
| 8 | high | `midiimport` | A hostile meta-event length in a **26-byte** SMF drove the MIDI library to allocate 512 MB before reading a byte (a 5-byte variant OOM-hangs) — memory exhaustion the round-1 panic wrapper cannot catch, because OOM is a throw, not a panic | Raw pre-scan of track chunks refuses any declared length larger than the file itself |
| 9 | med | `mxlimport` (promoted) | A slur stop authored on a rest was filtered out with the rest, leaving the arc open — every later note imported slurred | Rest slur stops processed before the rest is skipped |
| 10 | med | `mxlimport` (promoted) | A tie chain renumbered mid-note registered under the stop's number, so the next stop degraded to a fresh attack | Chain re-registers under the start's number |
| 11 | med | `gpimport` + `score` (promoted) | A beat listing one note id twice imported two attacks on one string at one tick — and `Validate` accepted the state the editor, parser and writer all forbid | Importer dedupes with a warning; `Validate` gains the duplicate-string rule (defense in depth) |
| 12 | med | `mxlimport` | Sibling of round 1's fingering fix: an authored fret ≤ 127-valid but past the text format's 30-fret ceiling was honored — importing a piece that can never save | Falls back to inference like the round-1 cases |
| 13 | med | `textfmt` parser (promoted) | `\tempo 140` before `\track` then `\tempo 100` after silently discarded the first — authored tempo lost | Conflicting pending directives at **different** anchors are a parse error; same-anchor stays last-wins |
| 14 | med | `textfmt` parser | A directive anchored at the very end of the piece parsed fine but could never save; one left pending at EOF was silently dropped from playback | Pending directives flush at EOF; end-anchored ones are refused at parse time, naming the line |
| 15 | med | `textfmt` scanner | A bare `\r` (classic-Mac or CRLF) was inline space to some paths and a line break to others, smuggling a `\r` into a track name — parses, never saves | Bare `\r` ends the line in all three scanner paths |
| 16 | med | all three importers + `textfmt` | An imported title or track name holding `//` or a line break flowed verbatim into the score — plays, never saves | New `textfmt.CleanLabel`; all importers clean labels with a warning |
| 17 | med | `mxlimport` (promoted) | Out-of-MIDI staff-tuning values (−24, 131…) were stored in `Track.Tuning` for the UI and synth to use | Rejected wholesale with a warning, mirroring gpimport |
| 18 | med | `mxlimport` (promoted) | `<capo>` was unbounded (gpimport clamps at 12) | Clamped to 0–12 with a warning |
| 19 | low | `textfmt` writer (promoted) | `pitchName` could spell a written C-1 in a wind beat token, which the beat parser cannot read back — unreachable via the registry, reachable by hand | Written pitches below C0 refused at Format |
| 20 | med | `midiimport` (round-1 recheck) | The round-1 SMPTE fix holds for panics; seven further hostile-header shapes probed clean | (covered by finding 8's pre-scan and pinned probes) |

## Round-1 fixes: re-verification verdicts

All 19 held. Highlights: the detector flux clamp survived a 400-config
adversarial sweep (windows 0–2048, MinHz to 1e300); the struck-key bend
fix survived mid-glide chaining, direction reversal, and voice stealing;
the wind-track refusals leak no sibling verb; the mailbox fix's
clear-before-read window fails safe. The one incompleteness found — the
authored-fret ceiling sibling (finding 12) — was fixed and pinned.

## Dismissed deliberately (with reasons, not silence)

- Held ↑/↓ pitch-nudge undo granularity: refusals never snapshot, a full
  compass hold is ≤ ~70 genuine edits against an undo depth of 200, and
  stepwise undo of a held slide is coherent.
- Same-pitch unison NoteOff (all voices release every voice on that key):
  real, pre-existing, needs a per-string voice handle — an interface
  redesign, recorded here rather than half-fixed.
- Voice-steal click at pool exhaustion, and vibrato clipping against the
  bend rail at full deflection: cosmetic, bounded, not worth retuning.
- CLI `play -countin 999`: an explicit request with no lying UI, unlike
  the config file case that was fixed.
