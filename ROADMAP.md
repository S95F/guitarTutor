# guitarTutor Roadmap

Phases are ordered so that every phase ends with something a guitarist can actually practice with. Phase 1 is deliberately a complete, useful product *before* any live audio input exists — the research behind this ordering is captured in [docs/DECISIONS.md](docs/DECISIONS.md).

Guiding principles (violating these is a bug, not a style choice):

- **Ticks are authoritative.** The score model stores ticks (PPQ 960) plus a tempo map; wall-clock is always derived. Mid-piece tempo changes, looping, speed scaling, and scoring all flow through this.
- **One clock for audio.** All sounding sources mix in one place, scheduled by frame count — never `time.Ticker`. When capture arrives, input and output share a single duplex callback — and the single-clock property only truly holds when both endpoints are on the same physical interface, so the app steers users there and treats split-device setups as a drift case to detect.
- **The audio callback never allocates.** Real-time callbacks do a memcpy into a preallocated ring buffer; all DSP happens in ordinary goroutines.
- **Sample-accurate looping is a correctness feature.** MuseScore's and Songsterr's loop bugs are the most-complained-about failures in comparable tools; users test the loop in minute one.
- **Pure Go until it can't be.** Phase 1 needs no C toolchain. cgo arrives with audio capture in Phase 2 (release builds are cgo from then on; the pure-Go playback-only backend remains maintained for contributor and CI builds). ONNX stays optional behind a build tag.
- **Forgiving, optional scoring.** False "you missed" feedback is the #1 rage-quit cause in this category. Detection must be calibratable, tolerant, and never block practice.

---

## Phase 0 — Foundations (in progress)

Decisions and scaffolding that are painful to retrofit.

- [x] Repository, module, license, README, roadmap
- [ ] **Score model** (`internal/score`): `Score{ppq, tempoMap, timeSigs, tracks}`, `Track{name, tuning, capo}`, bars → beats → `Note{string, fret, tied, technique}`. Pitch is always *derived* from tuning + fret + capo, so altered tunings and transposition are free.
- [ ] **Text tab format** (`internal/score/textfmt`): a small, alphaTex-*inspired* authoring format — header block (`\title`, `\tempo`, `\tuning`), bars separated by `|`, beats as `fret.string.duration`, chords in parens, technique suffixes (`h p s b v x`). Documented as its own spec; doubles as the test-fixture language. (Deliberately *not* claiming alphaTex compatibility — that would mean inheriting a large evolving grammar.)
- [ ] **Fixture corpus** (`testdata/`): the same 4-bar riff authored as `.mid`, text tab, `.gp`, and `.musicxml`, collected now; round-trip tests land with each importer (MIDI/text in Phase 1, `.gp`/MusicXML in Phase 3). Every importer bug in this space is a timing-semantics bug; only a shared corpus catches them.
- [ ] **UI spike** (timeboxed): native Go rendering (Ebitengine is the leading candidate — same ecosystem as our audio output library) vs. embedding [alphaTab](https://github.com/CoderLine/alphaTab) in a webview. alphaTab would gift us Guitar Pro import + tab rendering + synth, at the cost of a webview, JS↔Go IPC, and a second clock domain to reconcile against scoring. This decision reorders half the roadmap — make it here, on evidence, not later.
- [ ] CI: build + test on Windows; lint; `go vet`.

## Phase 1 — Practice player (pure Go, no guitar input yet)

Roughly "Guitar Pro 8's speed trainer minus the editor" — a product people pay $50–70 for today, and 100% pure Go (single `.exe`, no toolchain drama for contributors).

**Import & playback**

- [ ] Standard MIDI File import via `gitlab.com/gomidi/midi/v2/smf` (the one production-grade Go music library). MIDI has no string/fret data, so tab display uses a *swappable* fret-assignment heuristic, and inferred fingerings are visually marked.
- [ ] Text tab format parser (from Phase 0 spec)
- [ ] SoundFont synthesis via a vendored/forked `go-meltysynth` (pure-Go SF2 synth; upstream is complete but dormant — we own our copy). Ship with a freely-licensed GM SoundFont.
- [ ] **Frame-counted sequencer** (`internal/engine`): our own scheduler over the parsed score — *not* meltysynth's built-in MIDI sequencer — driving NoteOn/NoteOff, metronome accents, loop boundaries, and the tab cursor from one frame counter. Output through `gopxl/beep` v2 streamers into `ebitengine/oto` v3.
- [ ] Audio pipeline standardized on 48 kHz float32 stereo; convert at boundaries (beep is float64, meltysynth renders float32).

**The practice loop**

- [ ] A/B section looping, sample-accurate at loop boundaries — gapless by default, with optional count-in before each pass
- [ ] Tempo scaling: relative % and fixed BPM (exact and pitch-true, since playback is synthesized)
- [ ] Progressive speed trainer: auto-increase tempo N% per completed pass
- [ ] Metronome: synthesized click (frame-scheduled, never `time.Ticker`), visual beat indicator, accent from the time-signature map
- [ ] Track mute/solo (practice against the band, drop the guitar track)

**UI**

- [ ] Scrolling 2D tablature with playback cursor (renderer per the Phase 0 spike decision)
- [ ] Piece browser, section selection, transport controls

**Exit criteria:** a guitarist can import a MIDI file or write a text tab, loop the hard four bars at 60% with a count-in, and ramp back to full speed — with looping that never stutters, drops the first note, or drifts.

## Phase 2 — The guitar plugs in

Live capture, and with it the cgo build event. This phase is where the two-clock trap is dodged for good.

**Audio I/O migration**

- [ ] Define `AudioBackend` interface (device enumeration, duplex stream open, timestamped frame delivery); port Phase 1 output onto it
- [ ] `gen2brain/malgo` (miniaudio) backend: **one full-duplex WASAPI shared-mode stream** — guitar capture and playback in one callback. 48 kHz float32, periods of 256–480 frames (5–10 ms; expect the ~10 ms shared-mode capture floor in practice — see Known Risks). Comfortably sufficient for scoring; guitarists monitor through their interface, not the app.
- [ ] Callback discipline: DataProc does a memcpy into a preallocated SPSC ring buffer, nothing else
- [ ] Device picker + buffer-size setting; handle hot-unplug and default-device switches
- [ ] Same-device steering: default playback to the same physical interface the guitar is plugged into (WASAPI has no native duplex device — capture and render are paired streams, and only same-device pairs share a clock). Warn on split-device setups; their clocks drift over a session, which a static calibration offset cannot fix.
- [ ] CI grows mingw-w64 (or zig cc) for Windows cgo builds; document contributor toolchain setup

**Pitch detection (`internal/pitch`)**

- [ ] In-house port of **MPM** (McLeod pitch method, designed for guitar-like signals) with YIN-FFT as cross-check, on `gonum/dsp/fourier`. 2048-sample window / 256–512 hop at 48 kHz; 4096 window when tunings drop below ~70 Hz (drop C).
- [ ] Onset/energy gate (spectral flux or RMS delta) in front of the tracker — without it, silence produces garbage pitch; with it, palm-muted notes can be credited on onset timing even when pitch confidence is low
- [ ] Per-frame f0 + confidence → causal median filter → nearest-note quantization with cents tolerance (±35¢ to start)
- [ ] Octave-error guard (distortion makes autocorrelation lock onto 2× f0); recommend clean/DI signal in docs
- [ ] **WAV fixture regression harness before tuning any threshold**: recorded electric guitar per technique (open notes, fretted, bends, slides, palm mutes) per pickup type. Real guitar, not synthetic sines.

**Scoring & feedback**

- [ ] Tuner view: the pitch detector pointed at open strings — it doubles as the detector's debug/confidence UI, and the app warns when open-string tuning error would eat into the scoring tolerance (a guitar 20¢ flat halves the ±35¢ window)
- [ ] Latency calibration wizard: play a click through the output, detect it in the input, cross-correlate; store per-device offset, plus a manual nudge slider
- [ ] Score detected notes against expected score notes within timing windows (~±100–150 ms to start; total input-to-verdict latency budget is 40–80 ms, so feedback renders against the timeline, not "instantly")
- [ ] Hit / close / miss feedback on the scrolling tab; per-section and per-pass accuracy
- [ ] Bends scored as "reached target pitch within window" via the cents trajectory
- [ ] **Wait mode**: playback holds until the correct note is played (the most-loved learning feature in comparable OSS tools)
- [ ] Progressive speed trainer gates tempo ramp on accuracy, not just completed passes

**Exit criteria:** with a $100 interface on stock Windows drivers, single-note riffs score accurately enough that a wrong "miss" is rare, and wait-mode practice feels fair.

## Phase 3 — Content: real-world files

- [ ] **Guitar Pro 7/8 `.gp` importer**: the container is a zip holding `score.gpif` XML — `archive/zip` + `encoding/xml`, clean-room from public format documentation (the reference implementations are MPL/LGPL licensed; we don't port their code). Subset: tracks, tuning, capo, bars, voices, beats, notes, ties, tempo automations. This single importer unlocks the modern tab ecosystem.
- [ ] MusicXML subset importer (`.musicxml` + zipped `.mxl`), MuseScore output as the compatibility target; validated against the fixture corpus with known-good MIDI renderings
- [ ] Documented MuseScore CLI bridge for legacy formats (`.gp3`–`.gp5`, `.gpx` → MusicXML) instead of writing binary parsers for four dead formats
- [ ] Audio backing-track import (WAV/FLAC, MP3 best-effort): time-aligned to the score by tap-tempo/offset; slowdown pitch-shifts (no mature pure-Go time-stretch exists — documented limitation, synth path remains primary)

## Phase 4 — Chords and detection robustness

- [ ] **Expected-chord verification** — not blind polyphonic transcription (which is not feasible in real time). On strum onset, check the spectrum for each expected note's fundamental/harmonics via chroma-template correlation against the expected chord; report pass/fail and which expected notes are missing. (Chroma-template matching follows the published academic literature — Oudre et al. — rather than Ubisoft's patented in-game method.)
- [ ] Optional ML pitch backend: **SwiftF0** (MIT, ONNX, ~40× faster than CREPE on CPU) via `yalue/onnxruntime_go`, behind the same `PitchDetector` interface — build-tagged so the base app stays DLL-free. Buys robustness on noisy/distorted input.
- [ ] Palm-mute-aware scoring rules; per-technique tolerance tuning against the WAV fixture corpus

## Phase 5 — Platforms and polish

- [ ] macOS and Linux ports (malgo's CoreAudio/ALSA/PulseAudio backends compile from the same vendored miniaudio — the cost is CI toolchains and testing, not code)
- [ ] Advanced audio settings: WASAPI exclusive-mode toggle with automatic fallback (opt-in only — exclusive mode is driver-dependent and sometimes unstable)
- [ ] Offline "transcribe this recording" import via `spotify/basic-pitch` ONNX (well-suited offline; unusable live — its CQT needs >1 s of context)
- [ ] Practice history: per-piece, per-section accuracy and tempo progression over time
- [ ] Export: text tab → MIDI; consider emitting/consuming [OpenSongChart](https://github.com/mikeoliphant/ChartPlayer) for OSS-community interop
- [ ] Research spike (unscheduled): ASIO backend. Newly legally possible (Steinberg dual-licensed the SDK GPLv3 in late 2025) but no maintained Go path exists; only worth pursuing if real users hit drivers where WASAPI can't deliver — and then ideally as its own standalone library, since the whole Go ecosystem lacks one.

---

## Explicit non-goals

| Not doing | Why |
|---|---|
| Rocksmith `.psarc`/CDLC import | Ubisoft DMCA'd a similar project in 2026; legal gray zone |
| Bundled song catalog | Licensing kills these apps (Rocksmith delisting, Yousician's vanishing songs) |
| Real-time blind polyphonic transcription | Not feasible (~>1 s context + ~120 ms model latency); expected-chord verification covers the actual need |
| Dynamic difficulty | Community consensus: 100% difficulty + slow tempo beats auto-leveling |
| 3D note highway | 2D scrolling tab is cheaper and transfers to reading real tabs |
| Amp/effect modeling | Use your amp sim of choice; the app wants your dry signal |
| ABC / ASCII-tab import | Wrong abstraction / no rhythm information; ASCII tab may return as an *export* target |
| Gamification meta (XP, streaks) | Practice tool, not a retention funnel |
| Bass guitar (for now) | Detection windowing is sized for guitar range; bass low E is 41 Hz and needs different windows. Worth revisiting once guitar scoring is solid — file an issue if you want it |

## Known risks (tracked, not ignored)

- **cgo on Windows** (Phase 2+): contributors need mingw-w64 or zig cc; cross-compilation gets harder. Mitigation: pure-Go Phase 1, CI images prepared before the migration, documented setup.
- **WASAPI capture latency floor**: shared-mode capture often sits at 10 ms periods regardless of playback's negotiated size. Acceptable for scoring (see latency budget); exclusive mode stays opt-in.
- **Split-device clock drift**: capture and playback devices that aren't the same physical interface run on independent sample clocks that drift apart over a practice session — enough to matter against a ±100–150 ms scoring window, and uncorrectable by a static offset. Mitigation: same-device steering by default; if split setups prove common, periodic re-correlation of the calibration click.
- **Dormant dependencies**: `go-meltysynth` (last commit 2023) is vendored/forked from day one; `go-mp3` is unmaintained — MP3 import is best-effort, WAV/FLAC preferred.
- **MIDI import lacks fingering**: fret-assignment heuristics can suggest unplayable fingerings; heuristic is swappable and inferred fingerings are marked in the UI. `.gp` import (Phase 3) is the real fix.
- **Webview-vs-native spike** (Phase 0) is load-bearing: choosing alphaTab embedding would eliminate the GP importer and tab renderer from this roadmap but add a clock-domain sync problem and a webview dependency. Decided by prototype, not preference.
