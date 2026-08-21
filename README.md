# musicTutor
[![CI](https://github.com/S95F/musicTutor/actions/workflows/ci.yml/badge.svg)](https://github.com/S95F/musicTutor/actions/workflows/ci.yml)

Plug in your guitar — or mic up your sax. Load a piece. Practice it properly.

musicTutor is an open-source practice companion written in Go, built first for guitarists and now growing past them. You import a piece of music (or write one in a simple text format), and the app plays it back — synthesized from the score, with a metronome and scrolling notation: tablature for a fretted track, a pitch ladder of written note names for a wind one — while you play along on your real instrument. Loop the hard bars, slow them down, and let the app ramp the tempo back up as you nail each pass. It listens to your playing and tells you which notes you hit.

> **Status: Phases 1–4 core work plus the multi-instrument turn, pre-alpha.** The practice player is functional; the app can hear you (`play -listen`: live pitch detection, hit/close/miss on the notation, a tuner, wait mode); and it opens **Guitar Pro 7/8 `.gp`** and **MusicXML** files directly and plays **backing tracks** (WAV/FLAC/MP3) pinned to score time. Chords are scored by verifying each expected note against the strum's spectrum. The app also speaks its first non-guitar family now — **monophonic winds, soprano sax first** (the whole sax family, flute, clarinet and trumpet ride the same registry): wind parts import from MIDI/MusicXML at correct concert pitch, display and author in **written pitch** (a B♭ soprano sax reads a major second above what it sounds), play on a built-in sustained reed voice, and score through the same detector. The importers are built clean-room against the documented formats and validated on a self-authored corpus, and detection thresholds are tuned on synthesized audio — real Guitar-Pro/MuseScore exports and real recordings of either instrument are the remaining validation gaps, so files and bug reports are gold. The app shell (Phase 5) has since landed: run the binary bare and you get a start screen with recent pieces, opening through the system's own file dialog, drag-and-drop, and in-app settings for devices, latency calibration, SoundFont and count-in — the command line remains the fastest path for anyone who prefers it. See the [roadmap](ROADMAP.md) and [decisions](docs/DECISIONS.md) (D8 is the multi-instrument design).

## Why

The tools guitarists actually practice with are either editors that can't hear you (Guitar Pro, TuxGuitar), subscription apps with unreliable note detection (Yousician, the now-abandoned Rocksmith+), or web players that break mid-loop (Songsterr). Rocksmith 2014 was delisted in 2023 and Ubisoft closed its studio in January 2026 — there is a real vacuum for an **offline-first, no-subscription, import-your-own-music** practice tool.

musicTutor aims to fill it with the practice loop every musician converges on — the horn player woodshedding a line needs exactly what the guitarist does:

1. Pick a section of the piece.
2. Loop it — gaplessly, or with a count-in before each pass.
3. Slow it down — without pitch change, since playback is synthesized from the score.
4. Ramp the tempo up automatically, pass by pass. The app can hear you now; gating the ramp on accuracy instead of mere completion is next on the roadmap.
5. Get honest, forgiving feedback on which notes you actually played.

## Planned features

**Phase 1 — practice player, no microphone needed (landed)**

- Import Standard MIDI Files, or write pieces in a small text tab format. (MIDI loses fingering data, so the app infers fingerings and marks them as inferred.)
- Synthesized playback with scrolling tablature and a playback cursor — a built-in plucked-string voice out of the box, or any SoundFont you supply (`-sf2`)
- Sample-accurate A/B section looping, gapless or with optional count-in
- Tempo scaling (percentage or fixed BPM) with an automatic per-pass speed ramp
- Metronome (visual + audio), track mute/solo
- Single-binary Windows build, pure Go

**Phase 2 — the guitar plugs in (landed)**

- Live input from your audio interface (WASAPI, one duplex stream for input + playback)
- Monophonic pitch detection tuned for guitar (riffs, lines, solos, bends)
- Hit / close / miss feedback on the scrolling tab, per-section accuracy
- "Wait mode": playback pauses until you play the right note
- Tuner view, latency calibration wizard

**Phase 3 — real-world files (landed)**

- Guitar Pro 7/8 (`.gp`) and MusicXML (`.musicxml`/`.mxl`) import with authored fingerings
- Backing-track audio (WAV/FLAC/MP3) pinned to score time

**Phase 4 — chords and robustness (landed)**

- Expected-chord verification: every string of a strum scored, via chroma-template correlation against the notes the score expects — not blind transcription
- Palm-muted and damped notes credited for their attack instead of failed for an unclear pitch
- Optional SwiftF0 ONNX pitch backend behind a build tag (the default binary stays free of any runtime DLL)

**Phase 5 — the app shell (landed)**

- Start screen with recent pieces, native file-dialog opening, and drag-and-drop
- In-app settings: audio device pickers, latency calibration in-window, SoundFont, count-in
- Fixed-BPM entry, track solo, a help overlay, and live warnings surfaced in the UI

**The multi-instrument turn — monophonic winds (core landed)**

- A wind instrument registry — soprano/alto/tenor/baritone sax, flute, clarinet, trumpet — carrying each horn's range, written transposition, and General MIDI program
- Wind parts in `.gtab` (written-pitch notes, `\instrument`, slurs), from MIDI, MusicXML and Guitar Pro imports, on a built-in sustained reed voice, drawn as a pitch ladder of the names the player actually reads
- A wind notation editor: every new piece and new track asks what it will be played on, and a horn part gets the same pitch ladder as the practice view — type A–G to place a note, arrows move it by semitone or octave
- Detection fitted per instrument — a held long tone can never be force-missed by a decay assumption, and soft same-pitch tonguing splits on the breath's dip instead of merging
- Still open: real-recording validation (everything is synthesis so far; the test harness is ready for recordings)

**Later (Phase 6 and beyond)**

- macOS and Linux ports
- An opt-in [note highway](docs/HIGHWAY.md) — notes approaching down string lanes, for when you want to read what's coming rather than read notation. The tab stays the default
- A [practice log](docs/PROGRESS.md) with streaks and badges, built so a detection error can never cost you either

See the full [ROADMAP](ROADMAP.md) for phasing, acceptance criteria, and explicit non-goals.

## How it works

```mermaid
flowchart LR
    subgraph Input
        GP[".gp / MIDI / text format"]
        Ed["Editor<br/>(write it yourself)"]
        Lib["Your library<br/>(the app's own folder)"]
    end
    Lib --> Score
    GP --> Score["Score model<br/>(ticks + tempo map)"]
    Ed --> Score
    Score -.saves as .gtab.-> Ed
    Score --> Seq["Frame-counted sequencer"]
    Seq --> Synth["Synth<br/>(built-in pluck & reed / SoundFont)"]
    Seq --> Tab["Notation view<br/>(tab / pitch ladder)"]
    Seq --> Click["Metronome"]
    Synth --> Out["Audio out"]
    Click --> Out
    Inst["Your instrument<br/>(guitar DI or mic)"] --> Detect["Pitch detection<br/>(MPM / YIN-FFT)"]
    Detect --> Scorer["Scorer"]
    Seq --> Scorer
    Scorer --> Tab
```

A few load-bearing design rules, distilled from research into why comparable apps succeed or fail (details in [docs/DECISIONS.md](docs/DECISIONS.md)):

- **The score is tick-based** (PPQ 960 + tempo map, SMF semantics). Wall-clock time is always derived, never stored — this is what makes looping, tempo scaling, and scoring drift-free.
- **One clock.** With live input, capture and playback run in a single full-duplex audio callback so scoring timestamps and playback share a frame counter. That only truly holds when both run on the same physical interface — so the app steers you to plug headphones into the interface your guitar is plugged into, and treats split-device setups (which drift) as a case to detect, not ignore.
- **Synthesis is the primary playback path.** Tempo change is exact and free when the piece is rendered from the score; audio backing tracks are a secondary import.
- **Scoring is forgiving and optional.** Detection latency has a physics floor set by the instrument's lowest note (~25–50 ms at a guitar low E's 82.4 Hz; a soprano sax, sounding two octaves higher, is comfortably faster); feedback is scored against timing windows, never marketed as instant. Nothing blocks your practice if detection misfires.
- **Pure Go until it can't be.** Phase 1 builds with no C toolchain at all. cgo arrives with audio capture in Phase 2 — release builds require it from then on, but a pure-Go playback-only backend stays maintained for contributor and CI builds. ONNX (for ML pitch) remains optional behind a build tag.

## What you'll need

**Guitar:**

- An electric guitar and a USB audio interface (Focusrite Scarlett-class is plenty). Any interface with a clean DI signal works — no proprietary cable required. (A Rocksmith Real Tone cable enumerates as an ordinary USB capture device and should work for input — mono, no direct monitoring — though it's untested.)
- Use your interface's **direct monitoring** to hear yourself; the app deliberately does not software-monitor your guitar (the round-trip latency would be unpleasant).
- **Want your distorted tone while you practice?** Hardware modelers or pedals in front of the interface work transparently. Software amp sims can run alongside if your interface's driver is multi-client (common on Focusrite-class gear: your sim takes the ASIO path while the app captures via WASAPI shared mode). Either way, scoring works best on the clean DI signal — heavy distortion degrades pitch detection.

**Soprano sax (or any wind the app knows):**

- A microphone into the same interface, and — this is not optional the way it is for a DI'd guitar — **headphones**. A mic hears the app's own playback and metronome from your speakers, and nothing in the analysis path can untangle your horn from that bleed yet; with headphones the mic hears only you. Direct monitoring is unnecessary: you can hear a saxophone.
- The app shows **written pitch** everywhere — the note names on the scrolling view, the tuner, the text format are all what you read on a chart for your B♭ (or E♭) instrument, while playback and scoring use the sounding pitch underneath.
- Honesty note: wind scoring is validated on synthesized audio so far. It should work — a sax is a strong, clean, monophonic signal, which is the easy case for this detector — but nobody has proven it against a real horn in a real room yet. Be the first; file what you find.

**Either way:** Windows first; macOS and Linux are planned once the practice loop is solid.

## Building & running

Requires Go 1.26+. Phase 1 is pure Go — no C toolchain, no assets to download.

```bash
go build ./cmd/musictutor
```

Run it with no arguments — or double-click the binary — and it opens the start screen, which lands on your pieces in three lists:

- **Recent** — what you have opened lately, from wherever it lives.
- **Written here** — what you have made in the editor lately.
- **Your library** — the app's own folder, *described*: every piece in it has been read, so it is listed by its title and what the music is (`4/4 · 92 BPM · 8 bars · drop D`) rather than by file name. Nothing falls off the end of it.

**Tab** and the arrow keys move between the lists; the wheel scrolls whichever one the cursor is over. Under them are the four things the screen does — open a file (the system's own dialog, filtered to the formats it imports), write a new piece, edit the selected one, and settings. Drag-and-drop onto the window still works.

A getting-started strip across the top tracks three steps — choose your audio interface, measure the round trip, open (or write) a piece — and ticks each off against the real configuration rather than just listing them. Press **H** to put it away, and **H** again to bring it back; the choice is remembered, and everything it points at is a row in settings.

```bash
./musictutor
```

Or go straight to a piece:

```bash
./musictutor play testdata/fixture_riff.gtab
```

### Writing your own

Not everything you practise exists as a file. The start screen's **Write a new piece** button (or **N**) opens the editor on a tablature grid. The rhythm of writing a piece in it is three keys:

- **0–30** types a fret onto the highlighted string (two digits within a moment make one number),
- **↑ ↓** choose the string, **[** and **]** the note value,
- **space** moves on to the next beat — and past the last bar, makes another, so writing never stops to ask for room.

The toolbar is **notation, not syntax**. Note lengths are noteheads with stems and flags — pick one, don't step through a fraction. A tie is an arc between two heads, a slide is a line between them, a bend is an arrow curving up, a dead note is a cross. They are grouped under captions that say what each group acts on — *note length*, *on this note*, *this bar*, *history* — and resting on any of them names it and gives its key. Nothing on it is labelled with a character out of the file format.

The staff is labelled with your tuning down the left, note lengths are drawn as stems and flags underneath, rests are rests, ties are arcs, and the header names the pitch the note under the cursor actually *sounds* — which is not the fret number, and is not the same with a capo on. **E** on the start screen opens whatever is selected, so an import you want to fix up is two keys away.

Pieces are saved as `.gtab` — the small, git-diffable [text format](docs/TEXTFORMAT.md) — into the app's own pieces folder by default, where they show up in your library. **shift+P** saves and opens the piece for practice; **esc** comes back to the editing, so writing a bar, hearing it, and fixing it is a loop rather than a round trip.

**F2** swaps the notation for the raw `.gtab` text, parsed as you type, with a legend beside it explaining every piece of the format — so the escape hatch does not require having read the format documentation first. That is where the parts of the format the toolbar has no button for live: a General MIDI program, a backing-track flag, an unusual tuning, a comment to yourself.

**Wind parts get their own grid.** A new piece (or a new track) first asks what it will be played on; choose a horn and the strings-by-frets surface becomes a pitch ladder — the same written-pitch reading the practice view teaches, with a line at every octave's C. Type **A–G** to place a note nearest the one before it, **↑/↓** to move it by a semitone (an octave with shift), **l** for a slur where a guitarist would hammer on. The text view spells the same part as `\instrument soprano sax` and pitch-name beats — `D5.8 E5.8 G5.4l` ([the format page](docs/TEXTFORMAT.md) has the whole grammar).

Bars stay exactly full, across every track at once — the editor refuses an edit that would overflow a bar rather than spilling it into the next one, and says what would not fit.

### Playing a piece

Or your own piece: `musictutor play song.gp` — also `.mid`, `.musicxml`/`.mxl`, and `.gtab`. Useful flags: `-scale 0.7` start slowed down, `-countin 4`, `-met` metronome, `-sf2 your.sf2` SoundFont synthesis instead of the built-in pluck.

Play along with the original recording under the synth:

```bash
musictutor play -backing song.flac -backing-offset 1.2 song.gp
```

The backing audio is pinned to score time, so looping and seeking stay aligned; slowing down pitch-shifts it (the synthesized parts stay pitch-true — no pure-Go time-stretch exists yet, a documented limitation). `-backing-offset` is the file position, in seconds, at the piece's first tick.

Legacy Guitar Pro formats (`.gp3`–`.gp5`, `.gpx`) aren't parsed directly — convert them once with free [MuseScore](https://musescore.org):

```bash
MuseScore4.exe song.gp5 -o song.musicxml
```

In the practice view: **space** play/pause, **←/→** seek by bar, **↑/↓** tempo ±5%, **shift+B** exact BPM, **A/B** set loop points at the current bar, **L** clear loop, **M** metronome, **R** progressive speed ramp, **C** count-in, **+/−** zoom, **1–9** mute tracks (**shift+1–9** solo), **T** tuner, **W** wait mode (live), **S** settings, **F5** re-open the piece, **esc** back, **Q** quit. Press **?** or **F1** on any screen for the full list.

The mouse works everywhere too: a transport bar and toggles across the top, a timeline under the tab showing where you are in the whole piece — click or drag it to move — and loop ends you can drag on either the tab or the timeline, snapping to the beat unless you hold shift. Click a track chip to mute it, right-click to solo it.

The playhead is drawn where the music is **sounding**, not where it is being rendered. Those are not the same place: rendered audio waits its turn in the output buffer, so a playhead taken straight from the sequencer runs ahead of what you can hear — by a sixteenth note at 150 BPM, which is exactly the error that makes you think you are dragging when you are not. The app subtracts the buffering it can measure; **Settings → audio / visual sync** trims the last few milliseconds inside the driver, which no software can measure.

### Live mode — the app listens

```bash
musictutor devices
```

lists your audio endpoints. Pick the interface your guitar — or your mic — is plugged into (a unique name fragment is enough), calibrate the round trip once, then practice with the app listening:

```bash
musictutor calibrate -in scarlett -out scarlett
```

```bash
musictutor play -listen -in scarlett -out scarlett testdata/fixture_riff.gtab
```

Notes you play are matched against the score: green = hit, amber = close (loose intonation, wrong octave, or a damped note whose attack landed but whose pitch never spoke), red = miss. **T** opens the tuner; **W** enables wait mode, which holds playback at each note until you actually play it.

**Chords are scored too — with a caveat worth reading.** The pitch tracker is monophonic, so rather than guess at a chord the app verifies the notes it already expects: each strum's pitch-class spectrum is checked against the expected chord, every string independently. Weak evidence is never a miss — a string the app can't clearly hear scores *close*, and a chord whose spectrum doesn't match convicts nobody. On synthesized chords across eight voicings and four strum speeds, correct playing produces a false miss on about 1% of strings, and no wrong chord scores a clean sweep of hits. But all of that is measured on **synthesized** guitar: chord scoring against a real instrument is unvalidated, and [ROADMAP.md](ROADMAP.md) lists the specific shapes that remain marginal. Heavy distortion degrades detection; feed the clean DI signal.

**A wind session listens differently, on purpose.** There are no chords to verify and no palm mutes to credit, so none of that machinery runs; the detector's search range is fitted to your horn instead of a guitar neck, and the miss deadline stretches to cover the longest note in the piece at the slowest practice speed — a correctly held long tone must never be called a miss just because you were still holding it. Wear headphones (see *What you'll need*): a mic that can hear the app's own playback is feeding your score somebody else's notes.

Live mode needs a cgo build (the default when a C compiler is present — on Windows install [mingw-w64](https://www.mingw-w64.org/) or build with [MSYS2](https://www.msys2.org/); if cgo builds misbehave in PowerShell, run the build from Git Bash). On Windows a `CGO_ENABLED=0` build still works fully as a playback-only practice player; on Linux and macOS Ebitengine itself needs cgo, so a C toolchain is required either way there.

There is also an offline renderer — useful for checking a piece without opening the UI:

```bash
./musictutor render -o out.wav testdata/fixture_riff.gtab
```

To write your own pieces, see the [text format](docs/TEXTFORMAT.md).

## Non-goals

- **No Rocksmith `.psarc`/CDLC ingestion** — legally hazardous (Ubisoft issued a DMCA takedown against a similar project in 2026). Import your own Guitar Pro, MIDI, and MusicXML files instead.
- **No bundled songs or catalog** — licensing is how practice apps die.
- **No amp modeling** — use your amp sim of choice alongside; the app only needs your dry signal.
- **No dynamic difficulty** — slow-then-ramp at full difficulty is the practice loop that works.
- **No XP grind, no loss-framing streaks, no leaderboards.** A practice log and additive recognition are planned ([docs/PROGRESS.md](docs/PROGRESS.md)), built so that misses cannot subtract and the streak never reads a note verdict — a detection error must never cost you anything. This is a practice tool, not a retention funnel.
- **The 2D notation stays the primary view.** A perspective [note highway](docs/HIGHWAY.md) is planned as an opt-in second view for fretted tracks, because reading tab transfers to reading real tab and that's worth keeping.
- **No subscriptions, ever.**

## Contributing

The project is at the "argue about the roadmap" stage — issues and discussion are very welcome, especially from guitarists and horn players who practice with existing tools, and from anyone who has fought Go audio or DSP before. Real recordings are the standing gap on both families: a mic'd sax take of a known line is as valuable a bug report as any stack trace. Start with [ROADMAP.md](ROADMAP.md) and [docs/DECISIONS.md](docs/DECISIONS.md). The two Phase 6 features have their designs written up ahead of the code, and both reverse an earlier non-goal — [docs/HIGHWAY.md](docs/HIGHWAY.md) and [docs/PROGRESS.md](docs/PROGRESS.md) are where to argue with them.

## License

[MIT](LICENSE)
