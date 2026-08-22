# UX review — the app as a first-time user meets it

Date: 2026-08-21. Method: the real binary, launched with a **fresh HOME**
(true first run) on a headless X display with **no audio hardware and no
`zenity`/`qarma` installed** — the worst-case Linux laptop a beginner
might own — driven by injected keystrokes and mouse clicks, with a
screenshot read at every step (`docs/ux/`). The lens throughout: someone
new to *both* the technology and their instrument.

Journeys walked: cold start → getting-started cards → settings →
new piece → instrument picker → writing a sax line (letters, arrows,
guitar-habit keys) → help overlay → save → quit-with-unsaved-work →
open-a-file → straight-to-practice on the sax fixture → play → tuner.

## What already works well for a beginner

- The **getting-started checklist** on first launch, with clickable cards
  (clicking "Choose your audio interface" really opens Settings).
- The **instrument picker** on every new piece — the first question is
  the right one, in plain words, with Escape doing the safe thing.
- The **"Writing a piece" first-steps** on a blank staff, in the
  instrument's own moves (A–G for a horn, fret digits for a guitar).
- **Refusals that teach**: pressing `h` on a sax says "no hammer-ons on a
  alto sax — l marks a slur"; a digit says "a wind part takes note names
  — A to G puts one down". Guitar habits get redirected, not ignored.
- The **tuner's no-input message** is the gold standard on this list:
  *"no live input — choose your capture device in settings (S)"* —
  states the fact, names the remedy, gives the key.
- The **help overlay per view** (`?`), the hover tooltips, the unsaved
  dot and green save button, the both-worlds pitch header
  ("written G#5 · sounding B4").

## Findings, prioritized

### P0 — a beginner is dead in the water

**1. Saving is silently impossible when the dialog helper is missing.**
On Linux the save dialog shells out to `zenity`/`qarma`; when neither is
installed, `pickSavePath` swallows the failure and returns the same `""`
as a user pressing Cancel. Every save route no-ops with **zero feedback**:
ctrl+S does nothing, shift+P does nothing, and — worst — the
quit-prompt's **Save button does nothing**, leaving the user a choice of
Discard (lose the piece) or killing the app (`docs/ux/10-ctrl-s.jpg`).
The Open path *does* surface the identical failure
(`docs/ux/15-open-dialog-fail.jpg`), so this is an asymmetry, not a
policy. Fix in two layers:
   - surface the dialog error through the editor's existing `ShowError`;
   - better, stop needing an OS dialog for the common case: the editor
     already has one-line prompts — a "name the piece" prompt saving into
     the library folder covers the beginner path with no external program
     at all (ctrl+shift+S can keep the OS dialog for save-elsewhere).

**2. Dead audio output plays silence with a straight face.**
With no working output device the transport reads **"playing"**, the
clock sits at 0:00, and nothing on any screen says why
(`docs/ux/16-practice-sax.jpg`; ALSA's errors go to a stderr no
double-clicked binary shows). A beginner cannot distinguish "app is
broken" from "my computer has no sound". The app already has the banner
plumbing (`SetLiveWarning`) and already detects capture problems — output
needs the same honesty: if the output stream fails to open **or the
playback clock is not advancing while "playing"**, raise a banner naming
Settings, and the tuner's phrasing is the template.

**3. Dialog and device errors speak developer, not human.**
`could not open the file dialog: exec: "zenity": executable file not
found in $PATH` — a beginner does not know what an executable or $PATH
is. Say what to do: *"musicTutor needs the 'zenity' program for file
dialogs — install it (on Ubuntu: sudo apt install zenity) — or drop a
file anywhere on this window."* Same for the settings device rows, where
ALSA's null device leaks its raw name — *"[1/1] Discard all samples
(playback) or generate zero samples (capture)"* — as the user's "audio
interface" (`docs/ux/02-click-audio-card.jpg`). Recognize it and label
it *"no real audio device found (silent)"*.

### P1 — friction a beginner feels every session

**4. "a alto sax"** — refusal templates prepend "a" blindly; alto sax
needs "an". One `an()` helper, every message template through it.

**5. "Measure the round trip" assumes the user knows what a round trip
is.** The card's subtitle helps, but lead with the plain word:
*"Calibrate timing — one pass of clicks, so scoring knows when you
actually played."* And the first two getting-started cards are clickable
but show no key hint — add "(S)" the way every other control names its
key.

**6. A track with no name shows "untitled" where the instrument is the
obvious name.** The practice track chip reads "1 untitled" for a soprano
sax part (`docs/ux/16-practice-sax.jpg`); the library already names wind
pieces by instrument — the chip should too.

**7. "soundfont — built-in pluck"** in Settings: two pieces of jargon and
one half-truth (wind tracks get the reed, not the pluck). Label the row
"instrument sounds" and the value "built-in (pluck / reed per track)".

**8. Count-in defaults to 0.** First press of play starts *instantly*,
which flusters exactly the user who needs the count-in most. Default 2–4;
experts turn it off once.

### P2 — worth doing, not urgent

**9. Seed the first run with a demo piece or two** (a guitar riff and a
sax line). Every panel of the first screen is empty
(`docs/ux/01-first-launch.jpg`); with no file to open, "press play and
see what this app does" — the single fastest way to understand it — is
impossible until the user authors or imports something. The screenshot
tool already builds demo pieces; ship two into the library on first run.

**10. Hearing the note you just placed.** The editor is deliberately
silent until shift+P opens the whole piece for practice — but for a
learner, placing a note and hearing it is the confidence loop. Even a
single short tone through the existing voices on note entry (with a
toggle) would transform writing for a beginner. Roadmap-sized, flagged
here because every trivial-user journey bumped into it.

**11. The footer hint lines run ~24 items wide** — complete but
wall-like. The `?` overlay already holds the full list; the footer could
show the six most-used keys per view and end with "? more". Low priority:
the current line is honest, just dense.

## Follow-up: what was fixed (same day)

Re-verified end to end in the same rig — fresh HOME, no audio, no zenity:

- **P0-1 fixed.** Plain save no longer touches an OS dialog: ctrl+S (and the
  quit prompt's Save, and shift+P on an unsaved piece) opens an in-window
  "name the piece" prompt that writes into the library, refuses collisions
  by name, and carries the pending leave/practice through. ctrl+shift+S
  keeps the OS dialog for save-elsewhere, and its failures now land on the
  status line instead of vanishing.
- **P0-2 fixed.** A piece "playing" while the audio clock is frozen for
  ~2.5 s raises a dismissable banner naming the remedy; it clears itself
  the moment audio moves, restoring whatever warning it covered.
- **P0-3 fixed.** Dialog failures say what to install and what works
  instead; the ALSA null device is labelled "no real audio device
  (silent)" in both device rows.
- **P1 all fixed.** `score.An` ends the "a alto sax" class everywhere
  (including "a eighth"); the getting-started cards say "Calibrate the
  timing", carry their key hints, and fit; unnamed tracks are chipped by
  their instrument; the settings row reads "instrument sounds — built-in
  voices (pluck for strings, reed for winds)"; a fresh config defaults to
  a 2-beat count-in (existing configs untouched).
- **P2-9 fixed.** First run seeds the library with two demo pieces (one
  guitar, one soprano sax with a slur), only when the pieces folder does
  not exist yet, so a curated library is never re-seeded.
- **P2-10 fixed.** The editor auditions each note as it is placed or
  nudged — the track's own voice (pluck or reed), a short tone, skipped
  for dead notes and silently absent when audio is.
- **P2-11 left as is.** The dense footer stays; `?` remains the full list.

## Verified fine under adversity

The instrument picker, first-steps guidance, and wind editor all behaved
exactly as designed under real keystrokes; the unsaved-changes prompt
appears and its Discard/Cancel work; Open surfaces its errors; the tuner
degrades perfectly; the practice ladder, transport, loop hints, and
help overlays all render correctly on the real window (screenshots in
`docs/ux/`).
