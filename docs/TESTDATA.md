# The real-instrument test corpus

Everything musicTutor's audio and importer code is validated against in
this repository is **synthesized or self-authored**: Karplus-Strong plucks
for pitch, chroma and chord tests, and hand-built fixtures for the `.gp`
and MusicXML importers. That is fast, deterministic, and it has already
flattered the project once — Phase 4 shipped a chord-verification claim
measured on E-shaped voicings that produced *false misses on a correctly
played open G*. The corpus described here is the standing answer to that
class of mistake.

None of it is committed. `testdata/real/` is gitignored, `internal/corpus`
locates it, and `corpus.Require` **skips** when it is absent — a fresh
clone stays green, and a developer who has fetched the corpus gets real
coverage. Fetch it with:

```bash
./scripts/fetch-corpus.sh scores
```

## Layout

```
testdata/real/
  notes/       single-note recordings — pitch accuracy, octave errors
  chords/      strummed chords by shape — chord verification
  techniques/  palm mutes, bends, slides, hammer-ons — the hard cases
  scores/      .gp from Guitar Pro, .musicxml/.mxl from MuseScore
```

The wind side (D8) has the same gap and, as yet, no categories: every
sax number in the tree is measured on the synthesized reed voice. When
wind recordings arrive they will want their own directories — long
tones, tonguing and slurring at speed, altissimo, and a mic'd room with
the app's own playback bleeding in, which is the failure mode a DI'd
guitar can never show. `corpus.Category` is an open string type, so
adding them is a constant and a directory, not a redesign.

## What to fetch, and why these

### Scores (5.6–21 MB — do this one first)

The `.gp` and MusicXML importers were written **clean-room** from format
documentation. Nothing in the repo proves they can read what Guitar Pro
and MuseScore actually emit. These two corpora close that gap for the
price of a few megabytes, which makes them the highest-value fixtures in
this document by a wide margin.

| Source | What it gives | Licence |
|---|---|---|
| [alphaTab `test-data`](https://github.com/CoderLine/alphaTab) | 341 real Guitar Pro files spanning GP3–GP8 (207 `.gp`, 56 `.gp5`, 37 `.gpx`, 23 `.gp4`, 18 `.gp3`), plus `conversion/full-song.{gp,gp5,gpx}` — one score in three containers, a ready-made cross-format oracle. Feature-named files: `bends.gp5`, `hammer.gp5`, `dead.gp5`, `harmonic-types.gp5` | MPL-2.0 |
| [MuseScore `musicxml/tests/data`](https://github.com/musescore/MuseScore) | 231 `*_ref.xml` files that are **MuseScore's own MusicXML serialiser output** — precisely the artefact our importer must consume. Guitar-relevant: `testTablature1–5.xml`, `testTabs_ref.xml`, `testGuitarBends_ref.xml` | GPL-3.0 |
| [MuseScore `guitarpro/tests`](https://github.com/musescore/MuseScore) | A second GP corpus whose `bend.gp3/.gp4/.gp5/.gp/.gpx` quintet is a cross-version regression matrix on one feature | GPL-3.0 |

**Why copyleft is not a problem here, and where it would be.** MPL-2.0 and
GPL-3.0 are *distribution* licences. GPLv3 §2 permits unlimited private
use of copies you never convey; MPL-2.0 §3's obligations likewise trigger
on distribution. Fetching these onto a developer's machine to run tests
conveys nothing, so no copyleft obligation attaches and musicTutor's MIT
licence is untouched. This only works because they stay gitignored — do
not vendor the GPL corpus into the repository. (MPL-2.0 is per-file, so
vendoring *those* would merely make those files MPL; GPL vendoring is the
one to actually avoid.)

If you would rather have fixtures you could redistribute:
[OpenScore Lieder](https://github.com/OpenScore/Lieder) is CC0 and 18.6 MB
of genuine MuseScore-exported `.mxl`, and the
[Unofficial MusicXML Test Suite](https://github.com/cuthbertLab/musicxmlTestSuite)
is MIT and under 2 MB with systematic spec coverage.

> **Excluded deliberately:** alphaTab's `test-data/musicxml-samples/`
> mirrors musicxml.com's contributed set, whose copyright holders granted
> permission to *musicxml.com*, not to us. The fetch script skips it.

### Audio (1.4 GB and up)

| Source | What it gives | Licence |
|---|---|---|
| [GuitarSet](https://zenodo.org/records/3371780) | 360 × 30 s excerpts with JAMS annotations: per-string pitch contours, per-string MIDI notes, beats, tempo, and two chord-annotation layers. Acoustic, but isolated and hexaphonic. Fetch `annotation.zip` (39 MB) + `audio_mono-mic.zip` (657 MB) + `audio_mono-pickup_mix.zip` (683 MB) | CC BY 4.0 |
| [Guitar-TECHS](https://zenodo.org/records/14963133) | The **electric** counterpart, already split into techniques / chords / single notes, with DI, mic'd amp, and per-string MIDI ground truth. `P1_techniques.zip` + `P1_chords.zip` ≈ 1.3 GB | CC BY 4.0 |
| [EGFxSet](https://zenodo.org/records/7044411) | Optional brute-force pitch oracle: every fret on every string, 5 pickup configurations. `Clean.zip`, 431 MB | CC BY 4.0 |

CC BY's attribution obligation attaches on sharing, not on local use, but
cite them anyway — see [FIXTURES.md](FIXTURES.md).

**Rejected, with reasons.** *IDMT-SMT-Guitar* is the best technique corpus
going and is CC **BY-NC-ND** — usable for an unmonetised hobby project
today, but it would become unusable the moment musicTutor is sold or used
commercially, and our MIT licence explicitly invites that downstream. ND
also means a CI job uploading a trimmed clip as an artefact is a
violation. Guitar-TECHS covers the same axis under CC BY 4.0, so take
that instead. *EGDB* and the *NTU GPT* dataset state **no licence at all**
(so default copyright applies, and downloading is unlicensed copying)
despite excellent ground truth. *DadaGP* is 26,181 Guitar Pro files
scraped from Ultimate-Guitar — copyrighted arrangements of copyrighted
songs; never fetch it. *MedleyDB* and bulk *Freesound* need registration
or OAuth and cannot be scripted.

### Or record your own

For musicTutor's specific weak spots, five minutes of your own playing
beats any dataset, because you can record exactly the cases the tests say
are marginal: **open G, open C and A major** (the voicings where
synthesized chroma does not separate cleanly), a **downstroke strummed
slowly enough to hear the sweep**, a **chord change over a still-ringing
chord**, and **palm-muted** and **bent** notes. Clean DI, mono, 48 kHz
WAV, one file per case, named for what it is (`open-g-slow-strum.wav`).
Drop them in `testdata/real/chords/` and `techniques/`.

## Adding a test that uses the corpus

```go
files := corpus.Require(t, "../..", corpus.Chords, ".wav", ".flac")
```

`Require` skips the test when the corpus is missing, so never guard with
`os.Stat` yourself and never fail on absence. Keep corpus-backed tests
*additive*: the synthesized suites stay the fast default, and CI — which
has no corpus — must remain green.
