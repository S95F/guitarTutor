# The musicTutor text format (`.gtab`)

A small, git-diffable format for authoring practice pieces by hand. It is
*inspired by* [alphaTex](https://alphatab.net/docs/alphatex/syntax) —
same core idea of `fret.string.duration` beats — but is deliberately its
own, much smaller grammar (see ROADMAP Phase 0: compatibility with
alphaTex's full grammar is a non-goal). A fretted track writes tab-style
beats; a wind track (`\instrument`) writes the pitch names its player
reads — the two note grammars are covered below.

It is also what the in-app editor writes. `internal/edit` holds
a piece in the `internal/score` model and `textfmt.Format` renders it back
to this text, so everything below round-trips: what the editor saves,
the parser reads back as the same piece, field for field. Three things the
model can hold and this format cannot are refused with a message rather
than approximated — a beat that is not a plain, dotted or triplet note
value, a tempo change that does not land on a barline, and two notes on
one string in the same beat.

Files are UTF-8 text, extension `.gtab` (the extension predates the
app's wind support and stays for every file already on disk). `//`
starts a comment that runs to end of line.

## Header and directives

Directives start with `\`. Header directives appear before the first bar;
`\tempo` and `\time` may also appear later, between bars, and take effect
at the bar that follows them.

| Directive | Meaning | Default |
|---|---|---|
| `\title <text>` | Piece title | file name |
| `\tempo <bpm>` | Tempo in BPM (may repeat between bars) | 120 |
| `\time <n>/<d>` | Time signature (may repeat between bars) | 4/4 |
| `\track <name>` | Starts a new track; bars that follow belong to it | one unnamed track |
| `\tuning <notes...>` | Open strings for the current track, **low to high** (e.g. `E2 A2 D3 G3 B3 E4`), or MIDI numbers | standard E |
| `\capo <fret>` | Capo for the current track | 0 |
| `\instrument <name>` | Makes the current track a wind part (see below); refuses to mix with `\tuning`/`\capo` | fretted |
| `\program <0-127>` | General MIDI program hint for synthesis | 25 (steel guitar), or the wind instrument's own |
| `\backing` | Marks the current track as accompaniment (not the practice part) | user track |

Note names are letter + optional `#`/`b` + octave, scientific pitch
notation with middle C = C4 = MIDI 60 (guitar low E = E2 = MIDI 40).

## Bars and beats

Bars are separated by `|`. A bar's beats must exactly fill its time
signature — the parser rejects underfull and overfull bars with a line
and column. Newlines are insignificant except for ending comments.

Each beat on a **fretted track** is one of:

- **Note** — `fret.string` or `fret.string.duration`
- **Chord** — `(fret.string fret.string ...)` or `(...).duration`
- **Rest** — `r` or `r.duration`

**Strings** are numbered tab-style: 1 is the highest-pitched string, 6 the
lowest on a standard guitar.

**Durations** are the note-value denominator: `1` whole, `2` half, `4`
quarter, `8` eighth, `16`, `32`. Append `.` for dotted, `t` for triplet
(`8t` = one note of an eighth-note triplet). When omitted, the duration
**sticks** from the previous beat (initially `4`).

**Ties**: prefix `~` marks a beat's note as the continuation of the
previous beat's note on the same string and fret — it sustains instead of
being attacked again. `0.6.2 ~0.6.2` is a whole-note low E.

**Techniques** are single letters suffixed to the token, after the
duration: `h` hammer-on, `p` pull-off, `s` slide, `b` bend, `v` vibrato,
`x` dead/muted note. Example: `5.3.8h`, `7.3.4b`.

## Wind tracks

`\instrument soprano sax` (before the track's first bar) makes the track
a monophonic wind part. The app knows the instruments in
`score.WindInstruments` — the saxophone family, flute, clarinet, trumpet
— and each brings its range, its written transposition, and its General
MIDI program as the `\program` default.

On a wind track a note is a **written pitch name**, exactly what its
player reads off their own chart: `D5.8` is a written D5 eighth on a
B-flat soprano sax, sounding a major second lower (the app handles the
transposition; playback and scoring use sounding pitch, the file and the
screen use written). Durations, sticky durations, rests, ties and bars
all work as above. What changes is what the instrument can do:

- **No chords** — one note at a time; `(...)` is refused.
- **No `\tuning`, no `\capo`** — the instrument is the pitch reference.
- **Techniques** are `l` slurred (no fresh tongue), `s` slide/scoop,
  `b` bend, `v` vibrato. The fretted letters `h p x` are refused, with a
  message that says why.
- **Range**: notes below the instrument's lowest written note are
  refused. Above the standard range is *altissimo* — hard, real, and
  accepted (the editor's palette stops at the standard range; the format
  does not).

```text
\title Sax Line
\tempo 96
\instrument soprano sax

D5.8 E5.8 G5.4 A5.4l B5.8v A5.8 |
G5.4 E5.4l D5.2v
```

## Example: the canonical fixture riff

This exact riff (E minor, 120 BPM, 4/4) is the cross-format fixture
required by ROADMAP Phase 0 — it exists in `testdata/` in every format the
app imports, and importers are tested against it.

```text
\title Fixture Riff
\tempo 120
\time 4/4
// tuning defaults to E standard, string 6 = low E

0.6.8 3.6.8 5.5.8 0.6.8 3.6.8 5.5.8 3.6.8 0.6.8 |
2.5.4 0.5.4 2.5.2 |
(0.6 2.5 2.4).2 r.4 3.6.4 |
0.6.2 ~0.6.2
```

Bar by bar: eight eighth-note riff notes; three beats with a sticky-ish
explicit rhythm (quarter, quarter, half); an E5 power chord for a half
note, a quarter rest, a quarter note; a whole-note low E written as two
tied halves.

## Editing this by hand vs. in the app

The editor's toolbar has controls for what a guitarist reaches for most:
frets, note lengths, ties, the six techniques, bars, the meter, the tempo,
a handful of tunings. It shows them as NOTATION rather than as this
format's characters — a filled notehead with a flag rather than `8`, an
arc rather than `~`, a cross rather than `x` — because the letters below
are the right way to write a file down and the wrong way to label a
button.

The format has more than the toolbar does: a General MIDI program,
whether a track is backing, an arbitrary tuning, a wind instrument, a
comment explaining a passage to yourself.
**F2** in the editor shows this text instead, parsed as you type, with a
legend beside it covering everything on this page in one column. Neither
view is the real one; both are the same piece. A wind piece gets a grid
of its own — a pitch ladder in written pitch, notes typed as the letters
A–G — and its text view spells the same part in this format's wind
grammar.

## Semantics

Parsing produces the `internal/score` model directly: PPQ 960 ticks,
tempo/meter maps from the directives, `Note{String, Fret, Tied, Tech}`
per beat — where a wind track's one lane is String 1 and its Fret counts
semitones above the instrument's lowest note, so pitch stays derived
either way. Everything the parser accepts must `Validate()`; everything
it rejects must carry a position. There is no layout or display
information in the format — rendering decides that.
