# Bug sweep and minimization — summary

Date: 2026-08-21. Two full sweep-and-fix rounds over the whole codebase,
followed by a code-minimization pass and a repo-wide comment strip. Every
fix was demonstrated failing before it was fixed, pinned by a test, and
verified against a clean checkout before landing; the branch ends with the
full suite green under the race detector.

## The numbers

| Phase | Result | Commits |
|-------|--------|---------|
| [Round 1](ROUND1.md) | **19 verified bugs fixed** (5 domain reviews, ~100k fuzz inputs on importers) | `5ef606c..4fa80ff` |
| [Round 2](ROUND2.md) | **20 verified bugs fixed** (3 cross-seam reviews; all 19 round-1 fixes re-verified; ~12M round-trip fuzz executions on the text format) | `8cba5b5`, `eb7c6ea`, `425e677` |
| Minimization | 17 of 19 dead-code candidates removed (−196/+74 lines); 2 kept deliberately as test observables | `f06dbd8` |
| Comment strip | All comment prose removed from 174 Go files (−17,254/+2,242 lines); compiler directives and the cgo preamble kept | `3f5e490` |

**39 bugs total.** By severity: 5 high (two panics, a memory-exhaustion
DoS, a note leak, silent piece loss), 22 medium, 12 low/trivial.

## What the bugs had in common

Three classes account for most of the list:

1. **The save contract** — states that play fine but can never be saved,
   or files that save but can never reopen. The wind/fretted seam leaked
   verbs across (a capo on a sax), importers accepted labels and pitches
   the format refuses, the writer spelled meters the parser rejects, and
   `Validate` was missing a rule everything else assumed. Both rounds
   closed the loop from every side: importers clean or drop, the model
   refuses, the writer checks itself against the parser.

2. **Hostile input** — a flipped SMPTE bit crashed the process, a 26-byte
   file allocated half a gigabyte, a 2⁵⁴ time signature silently emptied a
   piece, a near-Nyquist config read past an array. All found by fuzzing,
   all now refused before the dangerous code runs.

3. **Wrong-by-one-frame / wrong-by-one-key** — the UI and audio seams: a
   prompt applying its own seed, a click through an unclamped scroll, a
   pitch bend measured from the wrong key, a NoteOff owed but never paid,
   a dialog goroutine racing the game loop.

## What was deliberately not "fixed"

Recorded in the round docs with reasons: unison NoteOff semantics (needs a
per-string voice handle — an interface redesign), voice-steal clicks at
pool exhaustion (cosmetic, bounded), undo granularity of held-key nudges
(coherent as is), and CLI flags that do exactly what the user typed.

## Minimization and the comment strip

The minimization pass applied the dead-code candidates the reviews had
collected — dead stores, always-true clauses, one-caller helpers, a
hand-rolled `itoa`, unreachable guards, alias constants, an unread XML
field. Two candidates that looked dead were kept: `Engine.TotalFrames`
and `Engine.WaitGeneration` are the observables that other packages'
tests synchronize on.

The comment strip then removed all comment prose — roughly 15,000 net
lines, about a quarter of the codebase — mechanically through the Go AST
(so `//` inside string literals was untouchable), preserving exactly two
kinds of comment a build needs: `//go:` compiler directives and the cgo
preamble in `internal/audio`. The design record lives in maintained
places instead: **docs/DECISIONS.md** for the choices and their
rationale, **docs/** for the format, testdata, and roadmap contracts,
**docs/sweep/** for what the reviews proved, and the commit history for
the measurements (the onset-dip calibration table, the flux-band bounds,
the chroma-timing grid searches).

## Gates the branch ends on

- `go build ./...`, `go vet ./...`, `gofmt -l` all clean
- Full test suite green under `-race` (every package, GUI tests under xvfb)
- The wind practice round trip still scores 14/14 at accuracy 1.000
- The screenshot tool still renders every screen (spot-checked the editor
  on the sax fixture after the strip)
