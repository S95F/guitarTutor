# Technology Decisions

Lightweight decision records for guitarTutor. Each records what was chosen, what was rejected, and why — based on a survey of the Go audio/music ecosystem and comparable applications conducted in August 2026.

---

## D1 — Audio capture & duplex: `gen2brain/malgo` (miniaudio)

**Decision:** All live audio I/O (Phase 2+) goes through [gen2brain/malgo](https://github.com/gen2brain/malgo), using a single full-duplex device so guitar input and app output share one clock.

**Why:** There is no pure-Go cross-platform audio *capture* library — every viable option is cgo. Among them, malgo is the clear winner: actively released (v0.11.25, May 2026), vendors miniaudio so it links against nothing external on Windows/macOS (no DLLs, no pkg-config), spans WASAPI/CoreAudio/ALSA/PulseAudio and more with automatic fallback, and exposes the latency knobs we need (period size, share mode, WASAPI flags). Its duplex device type — input and output delivered in one callback — is the cleanest foundation for aligning detected notes against playback for scoring; splitting capture and playback across two libraries guarantees two drifting clocks and systematically wrong timestamps. One honesty note: WASAPI has no native full-duplex device — miniaudio pairs a capture stream and a render stream under one callback — so the single-clock property holds *when both endpoints are on the same physical interface*. The app steers users to same-device duplex (headphones into the interface the guitar is plugged into); split-device setups drift and are treated as a case to detect (see ROADMAP Known Risks).

**Rejected:**
- [gordonklaus/portaudio](https://github.com/gordonklaus/portaudio) — canonical but slow-moving; requires an installed PortAudio library (MSYS2/DLL pain on Windows); doesn't expose `PaWasapiStreamInfo`, so WASAPI exclusive/low-latency modes are unreachable from Go.
- RtAudio Go bindings — upstream healthy, but the Go binding is an unversioned second-class citizen; the ASIO-enabled fork is dead since 2020.
- [moutend/go-wca](https://github.com/moutend/go-wca) — the one cgo-free path, but Windows-only and stale (2024); would still need a second backend for macOS/Linux.
- ASIO in any form — legally unblocked (Steinberg dual-licensed the SDK GPLv3 in late 2025) but zero maintained Go path exists. Parked as an unscheduled research spike; WASAPI shared mode (~10 ms) is sufficient for scoring, and guitarists hear themselves through their interface's direct monitoring, not through the app.

**Consequences:** cgo enters the build at Phase 2 (mingw-w64/zig cc on Windows; CI toolchains per OS). The audio callback must be allocation-free — memcpy into a preallocated SPSC ring buffer, DSP in goroutines. A per-device latency offset is measured by a calibration wizard and applied when aligning detected notes to the score timeline.

## D2 — Playback & synthesis: `gopxl/beep` v2 + `ebitengine/oto` v3 + vendored `go-meltysynth`

**Decision:** Phase 1 playback is 100% pure Go: [beep v2](https://github.com/gopxl/beep) as the DSP/streamer graph (mixer, looping, decoders for WAV/MP3/FLAC/Vorbis), [oto v3](https://github.com/ebitengine/oto) as the output driver, and a vendored fork of [go-meltysynth](https://github.com/sinshu/go-meltysynth) (pure-Go SoundFont SF2 synthesizer) rendering the score. beep streamers are kept pullable so Phase 2 can swap the I/O layer to a malgo duplex callback without touching musical logic.

**Why:** This stack ships a single Windows `.exe` with no C toolchain, which keeps Phase 1 contributor-friendly. go-meltysynth is real-time capable, MIT, stdlib-only, and drops directly into any callback (`NoteOn/NoteOff` → `Render([]float32)`); it's dormant upstream (2023) but complete — hence vendored/forked, not merely imported.

**Rejected:**
- FluidSynth bindings — best sound quality, but LGPL DLL distribution pain on Windows and hobby-grade bindings.
- TinySoundFont bindings — abandoned toys; we'd own the binding for no gain over meltysynth.
- meltysynth's built-in `MidiFileSequencer` — no live tempo change or loop points. We write our own frame-counted sequencer over the parsed score; the same scheduler drives synth, metronome, loop boundaries, tab cursor, and (later) the scorer's expected-note windows from one clock.

**Consequences:** Pipeline standardizes on 48 kHz float32 stereo (beep is float64 — convert at the boundary). Metronome clicks are synthesized and frame-scheduled, never `time.Ticker`-driven. No pure-Go pitch-preserving time-stretch exists, so *synthesis is the primary playback path* — tempo change is free when rendering from the score — and audio backing tracks are secondary (slowdown pitch-shifts; documented).

## D3 — Piece formats: MIDI + own text format first; Guitar Pro 7/8 next; MusicXML later

**Decision:** Phase 1 imports Standard MIDI Files via [gomidi/midi v2](https://gitlab.com/gomidi/midi) and a small in-house text tab format (alphaTex-*inspired*, deliberately not alphaTex-compatible). Phase 3 adds a clean-room Guitar Pro 7/8 `.gp` importer, then a MusicXML subset.

**Why:** gomidi is the only production-grade Go music library in the entire notation space (v2.3.24, June 2026; SMF read/write, tempo-map math built in). Everything else — MusicXML, Guitar Pro, ABC, ASCII tab — has no viable Go parser, so each additional format is code we write. GP7/8 `.gp` is the cheapest high-value one: the container is a zip holding `score.gpif` XML with full tab semantics, far easier than the GP3–5 binary formats or GP6's custom-compressed `.gpx` (both formats get a documented MuseScore-CLI conversion path instead of parsers).

**The internal model** copies SMF semantics: ticks are authoritative (PPQ 960 — divides cleanly for triplets/dotted/32nds), tempo and time-signature maps convert ticks to wall-clock, notes store string+fret with pitch always *derived* from tuning+fret+capo. Storing wall-clock instead would make looping and scoring drift and force a painful rewrite.

**Rejected as import targets:** ABC (melody-centric, no string/fret concept), ASCII tab (no rhythm information — timing is visual spacing; possible *export* target later), `.gpx`/GP6 (bit-stream decompressor for one dead app generation), `.psarc` (Ubisoft DMCA territory).

**License note:** the best reference implementations (alphaTab: MPL-2.0, PyGuitarPro: LGPL-3.0) are copyleft — importers are implemented clean-room from the public format documentation, not ported from their code.

## D4 — Pitch detection: in-house MPM/YIN-FFT port; expected-chord verification; SwiftF0 as optional ML backend

**Decision:** Phase 2 ships an in-house Go port (~200–400 LOC) of MPM (McLeod, primary — designed for guitar-like signals) with YIN-FFT as cross-check, on `gonum/dsp/fourier`, gated by an onset/energy detector. Chord scoring (Phase 4) is *expected-note verification* via chroma-template correlation — never blind polyphonic transcription. An optional ONNX backend ([SwiftF0](https://github.com/lars76/swift-f0) via [yalue/onnxruntime_go](https://github.com/yalue/onnxruntime_go)) can be swapped in behind the same `PitchDetector` interface for robustness on distorted input.

**Why:** No maintained Go pitch/onset/chroma library exists; the reference C++ (and tutorial Go) implementations in [sevagh/pitch-detection](https://github.com/sevagh/pitch-detection) are MIT and small enough to own. Physics sets the latency floor: low E is 82.4 Hz (12 ms/cycle) and autocorrelation-family detectors want 2–4 cycles, so ~25–50 ms algorithmic latency — the audio driver is not the bottleneck, and scoring must use timing windows rather than promise instant feedback. Real-time blind polyphonic transcription is out of reach (basic-pitch needs >1 s of spectral context) — but the app *knows the expected notes*, reducing chords to a far easier verification problem.

**Rejected:** aubio (GPL — viral; bindings dead), cycfi/Q (superb guitar-specific C++, but a cgo shim we'd own; revisit if the MPM port disappoints), CREPE (~1× realtime on CPU — too slow), PESTO (LGPL, no ONNX artifact), existing Go YIN toys (unmaintained, untested on live buffers).

**Consequences:** A WAV fixture corpus of real recorded guitar (per technique, per pickup) is built *before* any threshold tuning. Octave-error guards for distorted signals; docs recommend a clean/DI signal for scoring. Ubisoft's Rocksmith-era detection patents are sidestepped by implementing chord verification from the published chroma-template literature (Oudre et al.), which predates them as prior art.

## D5 — Product scope: practice player first, detection second

**Decision:** Phase 1 contains no microphone/input code at all — it is a Guitar-Pro-8-speed-trainer-class practice player (loop, slow down, count-in, progressive ramp, scrolling tab). Live detection lands in Phase 2 as an enhancement, and is always optional and forgiving.

**Why:** Survey of comparable apps shows the universally-valued core is the practice loop, while unreliable note detection is the #1 complaint machine (Yousician false positives; Rocksmith+ via mic "wholly futile"). Sample-accurate looping is a correctness feature users test in minute one (MuseScore's and Songsterr's loop bugs are their most-cited failures). Meanwhile the market gap is wide open: Rocksmith 2014 delisted (2023), the Rocksmith studio closed (Jan 2026), Yousician subscription-fatigued. An offline, free, import-your-own-music tool with a rock-solid loop is already valuable before it can hear anything; PickHero and ChartPlayer (both OSS) prove the lean scope works.

**Rejected:** detection-first ordering (the Yousician/Rocksmith model — ship the listening gimmick, backfill the practice tools). It front-loads the hardest, most complaint-prone engineering, forces cgo into the first release, and produces nothing usable until detection is good, which is exactly the failure mode of most abandoned open-source attempts in this space.

**Consequences:** "Wait mode" (pause until the right note) ships with detection — the most-loved learning feature in comparable OSS tools. Dynamic difficulty, 3D highways, catalogs, amp modeling, and gamification are explicit non-goals (see [ROADMAP](../ROADMAP.md)).

## D6 — Open: UI/rendering — native Go vs. embedded alphaTab (Phase 0 spike)

**Status: undecided — resolved by a timeboxed prototype in Phase 0.**

Native Go (Ebitengine is the leading candidate — game-loop rendering suits a scrolling tab, and it shares an ecosystem with oto) means writing our own tab renderer and importers but keeps one language, one clock domain, and easy cross-compilation. Embedding [alphaTab](https://github.com/CoderLine/alphaTab) in a webview (e.g. Wails) would gift GP3–8 + MusicXML import and mature tab rendering — at the cost of a webview dependency, JS↔Go IPC, MPL-2.0 boundary management, and a second clock domain that must be reconciled with Go-side scoring timestamps. This is the single biggest fork in the architecture; it gets decided by evidence in week one, because it determines whether Phases 1 and 3 include a renderer and importers at all.

Two constraints the spike must respect, whichever way it lands:

1. **Audio stays Go-side.** The frame-counted sequencer and synthesis remain in Go per the one-clock rule even under alphaTab — alphaTab's bundled synth would go unused, and the webview would be display-only, driven by the Go clock.
2. **The Phase 1 single-binary, no-external-runtime promise in the README is load-bearing.** If the webview route can't ship as a self-contained Windows executable with no install-time dependencies, it loses regardless of its other gifts.
