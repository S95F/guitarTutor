# Bug sweep — round 1

Date: 2026-08-21. Method: five parallel adversarial reviews over disjoint
package sets, each required to demonstrate a failing input before fixing
anything, each fix pinned by a test and verified against a clean checkout
before landing. Importers were additionally mutation- and structured-fuzzed
(~100k generated inputs). **19 verified bugs found and fixed**, in five
commits (`5ef606c..4fa80ff` on this branch; one commit per review domain).

## Findings

Severity is impact-based: **high** = crash or silent data loss, **medium** =
wrong behavior a user hits, **low** = wrong behavior needing unusual input.

### Editing model and text format (8)

| # | Sev | Where | Bug | Fix |
|---|-----|-------|-----|-----|
| 1 | high | `edit/notes.go` `SetFret` | On a wind track, indexed the nil tuning and **panicked** | Refuses with a message, mirroring `SetWindPitch` on a guitar |
| 2 | med | `edit/structure.go` `SetCapo` | Accepted a capo on a wind track → document could never validate or save again | Refused up front |
| 3 | med | `edit/structure.go` `SetTuning` | Same leak: tuning stored on a wind track → unsaveable document | Refused up front |
| 4 | med | `edit/notes.go` `ToggleTech` | Pull-off/dead-note bits toggled onto a wind note → unsaveable document | Those two bits refused on winds; slur/slide/bend/vibrato still allowed |
| 5 | high | `edit/edit.go` `squareUp` + 3 callers | `fillBar`'s "unreachable" panic was reachable: a Validate-passing 1/5 meter makes a bar no rest combination fills; `Open`, `AppendBar`/`InsertBar`, `AddTrack` all **panicked** | New bars go in empty; the refit pass that already follows lays rests and returns the refusal |
| 6 | low | `edit/edit.go` `New` | BPM clamp missed NaN (every NaN comparison is false) → invalid tempo map | Negated-range clamp, same form the parser uses |
| 7 | med | `textfmt/write.go` + edit | A title/track name containing `//` was written verbatim; reparse truncated it ("AC//DC" → "AC") or failed outright | `Format` refuses; the editor refuses the marker at typing time |
| 8 | med | `textfmt/write.go` | `Format` wrote meters `\time` cannot parse back (e.g. 4/3) — a file that can never reopen | Every meter checked through `parseTimeSig` itself before writing |

### Importers (4)

| # | Sev | Where | Bug | Fix |
|---|-----|-------|-----|-----|
| 9 | high | `midiimport` | An SMF with the SMPTE bit set in its division word **panicked inside the MIDI library** before the importer's own "not supported" error could fire — one flipped bit crashed the process (found by mutation fuzzing the fixture) | Raw header pre-check; a wrapper converts any residual library panic into an error |
| 10 | med-high | `mxlimport` fingering inference | Inferred positions had no MIDI ceiling: a huge `<capo>`, a negative staff-tuning octave, or a note over a G9 tuning derived pitch outside 0–127 and the **whole import errored** instead of degrading | Out-of-range positions dropped with a warning, like every other unplayable note |
| 11 | med | `mxlimport` | A `<beats>` numerator near 2⁵⁴ overflowed the bar-length product and returned a **successful empty score** — silent loss of the piece | Numerator bounded by the existing score-too-long check |
| 12 | low | `gpimport` | A present-but-unparsable tuning warned both "bad tuning" and "no tuning" — the second was a lie | Single truthful warning |

### Detection and synthesis (2)

| # | Sev | Where | Bug | Fix |
|---|-----|-------|-----|-----|
| 13 | med | `pitch/detector.go` | The spectral-flux band's low bin was clamped from below but not above; a search range near Nyquist on a tiny window read past the Hann coefficient table — **panic** on the first loud hop | Low bin clamped into the same range as the high bin |
| 14 | med-high | `synth/soundfont.go` | Chained legato/slide computed each link's pitch bend from the note it replaced, not from the key the channel was **struck** at — a slur run 60→64→67 ended sounding 63, and the bend-range guard measured from the wrong key too | Bend and guard both measure from the struck key; out of reach falls back to a fresh attack (the documented policy). Pinned via an in-memory minimal SoundFont so the tests actually run |

### Shell and CLI (4)

| # | Sev | Where | Bug | Fix |
|---|-----|-------|-----|-----|
| 15 | med | `cmd/musictutor/shell.go` | A piece that loads and practises but cannot open in the editor failed to stderr only — in the windowed shell, pressing E **silently did nothing** | The refusal lands on the start screen's status band, like the unreadable-file case beside it |
| 16 | med | `cmd/musictutor/shell.go` | **Data race**: the SoundFont dialog goroutine wrote the settings-touched flag the game loop reads every frame | The pick persists through the locked prefs and arms an atomic; the game loop delivers the mark on its own frame |
| 17 | low | `cmd/musictutor/live.go` | Calibrating with no flags, nothing remembered, and no system default measured for ~9 s, stored the offset under the empty-pair key **every lookup refuses**, then claimed success | Refuses up front, before touching the device, naming the remedy |
| 18 | trivial | `tools/uishot` | Package doc's screen list omitted `glyphs` | Doc matches the flag and the switch |

### UI (1)

| # | Sev | Where | Bug | Fix |
|---|-----|-------|-----|-----|
| 19 | low | `ui/editorwind.go` | The wind help overlay taught `b` as the bend key — on a wind track `b` deliberately **types the note B**; following the help would rewrite the note's pitch | The row names `l / s / v` and says the bend mark lives on the toolbar |

## Verified clean

Each reviewer also traced its domain's scariest paths and reported them
sound: picker modality and the applyPick document rebuild; ladder geometry
for all seven registry instruments; the dip-recovery onset state machine;
the reed allocator; scorer finalization (no double-finalize, no hit+miss);
engine loop boundaries; the config deep-copy-under-lock discipline; the
config-directory migration edges; every `editPiece` file-type × parse-failure
route but the one fixed above.

## Suspected but unproven (carried to round 2)

- SoundFont voice can refuse a legato takeover the engine already committed
  to → origin note held with no NoteOff (cross-seam; promoted to round 2).
- Hand-edited `countInBeats` above the UI's cap reaches the engine unclamped
  (promoted).
- Editor practice-open failure invisible in the window (promoted).
- mxlimport stores out-of-MIDI staff-tuning values; `<capo>` unbounded;
  slur stop on a rest leaves the arc open; mixed tie numbers (promoted).
- gpimport: duplicate note ids in one beat import as duplicate same-string
  notes (promoted).
- textfmt: pending tempo/meter overwrite across `\track`; `pitchName` octave
  −1 asymmetry; `Note.Inferred` has no .gtab spelling (promoted/judged).
