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

## Phase 0 — Foundations (done)

Decisions and scaffolding that are painful to retrofit.

- [x] Repository, module, license, README, roadmap
- [x] **Score model** (`internal/score`): `Score{ppq, tempoMap, timeSigs, tracks}`, `Track{name, tuning, capo}`, bars → beats → `Note{string, fret, tied, technique}`. Pitch is always *derived* from tuning + fret + capo, so altered tunings and transposition are free.
- [x] **Text tab format** (`internal/score/textfmt`): a small, alphaTex-*inspired* authoring format — header block (`\title`, `\tempo`, `\tuning`), bars separated by `|`, beats as `fret.string.duration`, chords in parens, technique suffixes (`h p s b v x`). Spec in [docs/TEXTFORMAT.md](docs/TEXTFORMAT.md); doubles as the test-fixture language. (Deliberately *not* claiming alphaTex compatibility — that would mean inheriting a large evolving grammar.)
- [x] **Fixture corpus** (`testdata/`): the same 4-bar riff authored as `.mid` and text tab, with a cross-format equality test (`internal/integration`); `.gp` and `.musicxml` renditions land with their importers in Phase 3. Every importer bug in this space is a timing-semantics bug; only a shared corpus catches them.
- [x] **UI spike** — resolved: **native Go with Ebitengine**. The Phase 1 scrolling-tab renderer was built natively at modest cost, which answers the question the spike existed to ask; embedding alphaTab (webview, JS↔Go IPC, second clock domain) is not needed. See DECISIONS D6.
- [x] CI: build + `go vet` + test on Windows and Ubuntu (`.github/workflows/ci.yml`; first run happens when the repo is pushed).

## Phase 1 — Practice player (pure Go, no guitar input yet) — mostly done

Roughly "Guitar Pro 8's speed trainer minus the editor" — a product people pay $50–70 for today, and 100% pure Go (single `.exe`, no toolchain drama for contributors).

**Import & playback**

- [x] Standard MIDI File import via `gitlab.com/gomidi/midi/v2/smf` (the one production-grade Go music library). MIDI has no string/fret data, so tab display uses a *swappable* fret-assignment heuristic (`internal/fretting`, a small Viterbi-style DP), and inferred fingerings are visually marked.
- [x] Text tab format parser (from Phase 0 spec)
- [x] Synthesis: a built-in Karplus-Strong plucked-string voice ships as the default (zero assets — nothing to download), with SoundFont SF2 synthesis via `go-meltysynth` when the user supplies a file (`-sf2`). See DECISIONS D2 amendment.
- [x] **Frame-counted sequencer** (`internal/engine`): our own scheduler over the parsed score — *not* meltysynth's built-in MIDI sequencer — driving NoteOn/NoteOff, metronome accents, loop boundaries, and the tab cursor from one frame counter, rendering directly into `ebitengine/oto` v3 (beep turned out to be unneeded until Phase 3's file decoding).
- [x] Audio pipeline standardized on 48 kHz float32 stereo
- [x] Offline WAV renderer (`guitartutor render`) — same engine code path, used by the end-to-end tests

**The practice loop**

- [x] A/B section looping, sample-accurate at loop boundaries — gapless by default, with optional count-in before each pass (engine-tested to repeat with an exact frame period)
- [x] Tempo scaling, relative (0.25×–2×), exact and pitch-true since playback is synthesized — *fixed-BPM entry in the UI still pending*
- [x] Progressive speed trainer: auto-increase tempo per completed pass up to target
- [x] Metronome: synthesized click (frame-scheduled, never `time.Ticker`), accent from the time-signature map
- [x] Track mute (keys 1–9) — *solo pending*
- [x] Count-in before playback and optionally between passes

**UI**

- [x] Scrolling 2D tablature with playback cursor, loop region, bar numbers, inferred-fingering marking (native Ebitengine per the Phase 0 spike)
- [ ] Piece browser (today: pass a file on the command line), fixed-BPM entry, track solo, in-app count-in toggle

**Exit criteria:** a guitarist can import a MIDI file or write a text tab, loop the hard four bars at 60% with a count-in, and ramp back to full speed — with looping that never stutters, drops the first note, or drifts. *Met, modulo the UI conveniences above; loop sample-accuracy is enforced by engine tests.*

## Phase 2 — The guitar plugs in — core landed

Live capture, and with it the cgo build event. This phase is where the two-clock trap is dodged for good.

**Audio I/O migration**

- [x] `audio.Backend` interface (device enumeration, duplex stream open); Phase 1's oto path remains the playback-only fallback for `CGO_ENABLED=0` builds
- [x] `gen2brain/malgo` (miniaudio) backend: **one full-duplex WASAPI shared-mode stream** — guitar capture and playback in one callback. 48 kHz float32, 480-frame periods negotiated on real hardware. (Found upstream: malgo v0.11.25's Backend enum omits `ma_backend_custom`, so its `BackendNull` requests the wrong backend — worked around locally; worth filing upstream.)
- [x] Callback discipline: the callback renders the engine and memcpys capture into a race-free SPSC ring (drop-newest overflow, backpressure signal); analysis runs in an ordinary goroutine
- [x] Device selection by name fragment (`guitartutor devices`, `-in`/`-out`), remembered in config — *in-app picker UI, buffer-size setting, and hot-unplug recovery still pending*
- [x] Same-device steering: documented in `devices` output and README — *automatic split-device warning still pending*
- [x] CI builds cgo on both platforms (runners ship gcc) plus a `CGO_ENABLED=0` fallback build check and a Linux `-race` leg; contributor toolchain documented in README

**Pitch detection (`internal/pitch`)**

- [x] In-house **MPM** (McLeod) with YIN-FFT cross-check on `gonum/dsp/fourier`: 2048-sample window / 480 hop at 48 kHz (4096 for tunings below ~70 Hz), NSDF peak picking with parabolic interpolation
- [x] RMS onset/energy gate (−55 dBFS floor, 8 dB rising edge, 50 ms refractory) — spectral-flux upgrade deferred
- [x] Per-frame f0 + clarity → causal 5-hop median → nearest-key quantization with cents; note tracker with hysteresis and bend-following
- [x] Octave-error guard (verified load-bearing against strong-second-harmonic signals); clean/DI signal recommended in README
- [ ] **WAV fixture regression harness with real recorded guitar** per technique and pickup — thresholds are tuned on synthesized signals only (KS plucks, sines) until real recordings exist

**Scoring & feedback**

- [x] Tuner view (T in the practice view): note name + cents bar from the live detector
- [x] Latency calibration (`guitartutor calibrate`): click train over the duplex stream, cross-correlated, per-device-pair offset stored with confidence — verified end-to-end through a VB-Cable software loopback (94.6 ms at 0.87 confidence). *Manual nudge slider pending.*
- [x] Scoring against timing windows (±150 ms default) with the calibrated offset reconciling the input/output clocks; misses finalize behind a lag because the tracker reports notes only when they close
- [x] Hit / close / miss feedback tinting the tab; running accuracy and input meter in the HUD — *per-section/per-pass accuracy breakdown pending*
- [x] **Wait mode** (W): playback holds at each user note until the detector hears it (octave-exact, intonation-lenient)
- [ ] Bend scoring via the cents trajectory (the tracker follows bends; the scorer does not yet judge "reached target")
- [ ] Progressive speed trainer gating the ramp on accuracy (currently completion-gated)

**Exit criteria:** with a $100 interface on stock Windows drivers, single-note riffs score accurately enough that a wrong "miss" is rare, and wait-mode practice feels fair. *The loop is machine-verified — the engine's own output looped back through the real detector scores ≥ 80% with 12+ hits (`internal/integration`) — but the exit criteria proper need a human with a guitar.*

## Phase 3 — Content: real-world files — landed

- [x] **Guitar Pro 7/8 `.gp` importer** (`internal/gpimport`): zip + `score.gpif` XML, clean-room from public format documentation (the reference implementations are MPL/LGPL; no code ported). Tracks, tuning, capo, first voice per bar, dots/tuplets, ties, tempo automations, authored string/fret. *Validation gap: the fixture corpus is self-authored — real Guitar-Pro-exported files are wanted as bug reports (`testdata/README-gp.txt`).*
- [x] MusicXML subset importer (`internal/mxlimport`): `.musicxml` + zipped `.mxl` with proper container resolution, exact `backup`/`forward` cursor handling (the documented silent-corruption trap, pinned by exact-tick tests), divisions rescaling, staff-tuning/capo, authored or inferred fingering. MuseScore-flavored fixture; real MuseScore exports are the same validation gap.
- [x] Documented MuseScore CLI bridge for legacy formats (`.gp3`–`.gp5`, `.gpx` → MusicXML) in the README, instead of binary parsers for four dead formats
- [x] Audio backing-track import (`internal/audiofile` + `Engine.SetBackingTrack`): WAV/FLAC, MP3 best-effort; pinned to score time so seeks, loops, and tempo scaling stay aligned and frozen positions are silent; slowdown pitch-shifts (no mature pure-Go time-stretch exists — documented limitation, synth path remains primary). `-backing`/`-backing-offset`/`-backing-gain` on play and render.
- [x] Cross-format fixture corpus complete: the canonical riff as `.gtab`, `.mid`, `.gp`, `.musicxml`, and `.mxl`, with an equality test over events **and authored fingerings** (`internal/integration`)

## Phase 4 — Chords and detection robustness

- [ ] **Expected-chord verification** — not blind polyphonic transcription (which is not feasible in real time). On strum onset, check the spectrum for each expected note's fundamental/harmonics via chroma-template correlation against the expected chord; report pass/fail and which expected notes are missing. (Chroma-template matching follows the published academic literature — Oudre et al. — rather than Ubisoft's patented in-game method.)
- [ ] Optional ML pitch backend: **SwiftF0** (MIT, ONNX, ~40× faster than CREPE on CPU) via `yalue/onnxruntime_go`, behind the same `PitchDetector` interface — build-tagged so the base app stays DLL-free. Buys robustness on noisy/distorted input.
- [ ] Palm-mute-aware scoring rules; per-technique tolerance tuning against the WAV fixture corpus

## Phase 5 — Platforms and polish

- [ ] macOS and Linux ports (malgo's CoreAudio/ALSA/PulseAudio backends compile from the same vendored miniaudio — the cost is CI toolchains and testing, not code)
- [ ] Advanced audio settings: WASAPI exclusive-mode toggle with automatic fallback (opt-in only — exclusive mode is driver-dependent and sometimes unstable)
- [ ] Offline "transcribe this recording" import via `spotify/basic-pitch` ONNX (well-suited offline; unusable live — its CQT needs >1 s of context)
- [ ] Export: text tab → MIDI; consider emitting/consuming [OpenSongChart](https://github.com/mikeoliphant/ChartPlayer) for OSS-community interop
- [ ] Research spike (unscheduled): ASIO backend. Newly legally possible (Steinberg dual-licensed the SDK GPLv3 in late 2025) but no maintained Go path exists; only worth pursuing if real users hit drivers where WASAPI can't deliver — and then ideally as its own standalone library, since the whole Go ecosystem lacks one.

## Phase 6 — Reading and returning

Two features this roadmap previously ruled out, revisited with their objections answered rather than waived. See DECISIONS D7 for the argument; the non-goals table below is amended, not deleted.

**Note highway** (`internal/ui/highway.go`, design in [docs/HIGHWAY.md](docs/HIGHWAY.md))

- [ ] Perspective view: string lanes receding to a vanishing point, notes growing as they approach a hit line. An **opt-in second view** (`H`), never a replacement — the 2D tab stays the default, because reading it transfers to reading real tabs
- [ ] **Depth is real seconds, not ticks** — approach velocity is constant at every BPM and every practice speed, so slowing down spreads notes apart instead of making the whole view crawl. `score.TempoMap.TimeAt` already does the integral
- [ ] **Verdicts only ever render behind the hit line.** Misses finalize ~4 s late by construction (the tracker reports a note when it *closes*); an approaching note is therefore always neutral and nothing green ever appears in front of the player
- [ ] Wait-mode rendering (nearly free — depth derives from `PosTick()`, which freezes), impact flashes, sustain trapezoids, per-lane tuning letters
- [ ] Engine prerequisite: `readBlockFrames` is 2048, so `PosTick()` advances in ~43 ms steps on the playback-only path — subtle on the tab, visible judder on a highway

**Practice history** (`internal/progress`, design in [docs/PROGRESS.md](docs/PROGRESS.md))

- [ ] Per-piece, per-section accuracy and tempo progression over time *(moved here from Phase 5 — badges are meaningless without it, so it lands first)*
- [ ] Versioned, atomically-written store beside `config.json`, with stricter durability rules than `appconfig`: never overwrite a newer schema, quarantine a corrupt file rather than replacing it
- [ ] Piece identity by structural content hash — excluding string and fret, since MIDI fingerings come from a swappable heuristic — plus evidence-based re-linking when a piece is genuinely edited

**Recognition**

- [ ] Session score, combo, streak, and badges, all opt-out, all behind a credibility gate that withholds scoring when the session is uncalibrated, dropping samples, or below the input gate
- [ ] **The never-punish invariants, as tested contract:** the streak never reads a verdict; no subtraction exists in the scoring path; chords and unjudgeable techniques are filtered before arithmetic; badges are set-only bits; a missed day consumes a freeze and the streak floors at 1, never 0
- [ ] Badge catalog where **half the badges need no detection at all**, and the detection-dependent ones reward deliberate practice — slow clean passes, completed ramps, mastered sections — not grinding

**Exit criteria:** a guitarist can read an unfamiliar piece off the highway without looking at the tab, and comes back the next day to a practice log that has never once told them they missed a note they actually played.

---

## Explicit non-goals

| Not doing | Why |
|---|---|
| Rocksmith `.psarc`/CDLC import | Ubisoft DMCA'd a similar project in 2026; legal gray zone |
| Bundled song catalog | Licensing kills these apps (Rocksmith delisting, Yousician's vanishing songs) |
| Real-time blind polyphonic transcription | Not feasible (~>1 s context + ~120 ms model latency); expected-chord verification covers the actual need |
| Dynamic difficulty | Community consensus: 100% difficulty + slow tempo beats auto-leveling |
| Highway as the *primary* view | The 2D tab stays first-class and stays the default — reading it transfers to reading real tabs. A perspective highway ships in Phase 6 as an opt-in second view over the same score model; see D7 and [docs/HIGHWAY.md](docs/HIGHWAY.md) |
| Amp/effect modeling | Use your amp sim of choice; the app wants your dry signal |
| ABC / ASCII-tab import | Wrong abstraction / no rhythm information; ASCII tab may return as an *export* target |
| Retention-funnel gamification: XP grind, loss-framing streaks, leaderboards | Practice tool, not a retention funnel — that judgment stands. What it over-rejected was the practice log Phase 5 already wanted, and recognition for deliberate practice; those arrive in Phase 6 with recognition that is additive only and a streak that never reads a verdict. See D7 and [docs/PROGRESS.md](docs/PROGRESS.md) |
| Bass guitar (for now) | Detection windowing is sized for guitar range; bass low E is 41 Hz and needs different windows. Worth revisiting once guitar scoring is solid — file an issue if you want it |

## Known risks (tracked, not ignored)

- **cgo on Windows** (Phase 2+): contributors need mingw-w64 or zig cc; cross-compilation gets harder. Mitigation: pure-Go Phase 1, CI images prepared before the migration, documented setup.
- **WASAPI capture latency floor**: shared-mode capture often sits at 10 ms periods regardless of playback's negotiated size. Acceptable for scoring (see latency budget); exclusive mode stays opt-in.
- **Split-device clock drift**: capture and playback devices that aren't the same physical interface run on independent sample clocks that drift apart over a practice session — enough to matter against a ±100–150 ms scoring window, and uncorrectable by a static offset. Mitigation: same-device steering by default; if split setups prove common, periodic re-correlation of the calibration click.
- **Dormant dependencies**: `go-meltysynth` (last commit 2023) is vendored/forked from day one; `go-mp3` is unmaintained — MP3 import is best-effort, WAV/FLAC preferred.
- **MIDI import lacks fingering**: fret-assignment heuristics can suggest unplayable fingerings; heuristic is swappable and inferred fingerings are marked in the UI. `.gp` import (Phase 3) is the real fix.
- **Recognition amplifies every false negative** (Phase 6): a grey note on a tab is a shrug, but the same detection error costing a streak is a reason to close the app — and the detector has known, documented failure modes (monophonic chord handling, uncalibrated sessions, split-device drift). Mitigation: the streak is driven by practice time rather than verdicts, scoring is withheld entirely behind a credibility gate, and misses cannot subtract. All three are contract-tested, not conventions.
- **Webview-vs-native spike** (Phase 0) is load-bearing: choosing alphaTab embedding would eliminate the GP importer and tab renderer from this roadmap but add a clock-domain sync problem and a webview dependency. Decided by prototype, not preference.
