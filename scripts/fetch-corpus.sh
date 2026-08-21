#!/usr/bin/env bash
# Fetch the real-instrument test corpus into testdata/real/ (gitignored).
#
# Nothing here is committed to musicTutor: the score corpora are copyleft
# (MPL-2.0 / GPL-3.0) and stay legally clean precisely because they are
# never redistributed, and the audio datasets are gigabytes with their own
# attribution terms. See docs/TESTDATA.md for the full licence reasoning
# and for what each source actually contains.
#
#   ./scripts/fetch-corpus.sh scores     # ~21 MB, do this one first
#   ./scripts/fetch-corpus.sh guitarset  # ~1.4 GB
#   ./scripts/fetch-corpus.sh techs      # ~1.3 GB
#   ./scripts/fetch-corpus.sh egfx       # ~431 MB
#   ./scripts/fetch-corpus.sh wind       # nothing to download — scaffolds
#                                        # the record-your-own wind layout
#   ./scripts/fetch-corpus.sh list       # show what each target fetches
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dest="$root/testdata/real"

need() { command -v "$1" >/dev/null || { echo "error: $1 is required" >&2; exit 1; }; }

usage() {
  cat <<'EOF'
usage: fetch-corpus.sh <target>...

targets:
  scores      Guitar Pro + MusicXML fixtures from the alphaTab and MuseScore
              test suites (~21 MB). Real files emitted by the applications
              our clean-room importers claim to read. MPL-2.0 / GPL-3.0 —
              local test use only, never commit them.
  guitarset   GuitarSet: 360 annotated acoustic excerpts, per-string pitch
              and chord ground truth (~1.4 GB). CC BY 4.0.
  techs       Guitar-TECHS: electric DI + mic'd, split into techniques and
              chords, per-string MIDI truth (~1.3 GB). CC BY 4.0.
  egfx        EGFxSet clean tones: every fret, every string (~431 MB).
              CC BY 4.0.
  wind        The wind corpus is record-your-own — no dataset we could
              point at covers a mic'd sax with the app's own playback
              bleeding in. Creates testdata/real/wind/{tones,
              articulation,rooms} and prints the naming contract, so the
              first take lands where internal/pitch's corpus test looks.
  list        Print the URLs each target would fetch and exit.

Everything lands in testdata/real/ and is gitignored.
EOF
}

# fetch_repo_dir <repo> <ref> <path-in-repo> <destdir> [exclude-substring]
# Downloads one directory of a GitHub repo via its tarball, without cloning
# the whole history (the MuseScore repo is enormous).
fetch_repo_dir() {
  local repo="$1" ref="$2" path="$3" out="$4" exclude="${5:-}"
  local tmp; tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  echo "  fetching $repo/$path"
  curl -fsSL "https://codeload.github.com/$repo/tar.gz/$ref" -o "$tmp/src.tgz"
  local prefix; prefix="$(tar -tzf "$tmp/src.tgz" | head -1 | cut -d/ -f1)"
  tar -xzf "$tmp/src.tgz" -C "$tmp" "$prefix/$path" 2>/dev/null || {
    echo "  warning: $path not found in $repo@$ref (upstream layout moved?)" >&2
    return 0
  }
  mkdir -p "$out"
  if [ -n "$exclude" ]; then
    (cd "$tmp/$prefix/$path" && find . -type f -not -path "*$exclude*" -print0) |
      while IFS= read -r -d '' f; do
        mkdir -p "$out/$(dirname "$f")"
        cp "$tmp/$prefix/$path/$f" "$out/$f"
      done
  else
    cp -r "$tmp/$prefix/$path/." "$out/"
  fi
}

# fetch_zenodo <record-id> <filename> <destdir>
fetch_zenodo() {
  local rec="$1" file="$2" out="$3"
  mkdir -p "$out"
  if [ -f "$out/$file" ]; then echo "  have $file"; return 0; fi
  echo "  fetching $file from Zenodo record $rec"
  curl -fL --progress-bar "https://zenodo.org/records/$rec/files/$file?download=1" -o "$out/$file"
}

target_scores() {
  need curl; need tar
  local out="$dest/scores"
  # alphaTab: 341 real Guitar Pro files, GP3 through GP8. musicxml-samples/
  # is excluded deliberately -- it mirrors musicxml.com's contributed set,
  # licensed to them and not to us (docs/TESTDATA.md).
  fetch_repo_dir CoderLine/alphaTab main \
    packages/alphatab/test-data "$out/alphatab" musicxml-samples
  # MuseScore's own MusicXML serialiser output, plus its GP import corpus.
  fetch_repo_dir musescore/MuseScore master \
    src/importexport/musicxml/tests/data "$out/musescore-musicxml"
  fetch_repo_dir musescore/MuseScore master \
    src/importexport/guitarpro/tests "$out/musescore-guitarpro"
  echo "scores: $(find "$out" -type f | wc -l) files in $out"
}

target_guitarset() {
  need curl
  local out="$dest/annotated/guitarset"
  fetch_zenodo 3371780 annotation.zip "$out"
  fetch_zenodo 3371780 audio_mono-mic.zip "$out"
  fetch_zenodo 3371780 audio_mono-pickup_mix.zip "$out"
  echo "guitarset: $out (unzip what you need; CC BY 4.0, cite it)"
}

target_techs() {
  need curl
  local out="$dest/annotated/guitar-techs"
  fetch_zenodo 14963133 P1_techniques.zip "$out"
  fetch_zenodo 14963133 P1_chords.zip "$out"
  echo "guitar-techs: $out (CC BY 4.0, cite it)"
}

target_egfx() {
  need curl
  fetch_zenodo 7044411 Clean.zip "$dest/annotated/egfxset"
  echo "egfxset: $dest/annotated/egfxset (CC BY 4.0, cite it)"
}

target_wind() {
  # Nothing is fetched: the wind categories exist for recordings only a
  # developer's own room can produce (docs/TESTDATA.md "What to record"),
  # so the target's whole job is to put the directories where
  # internal/corpus looks and to restate the naming contract at the
  # moment someone is about to save a file.
  mkdir -p "$dest/wind/tones" "$dest/wind/articulation" "$dest/wind/rooms"
  cat <<EOF
wind: created (record your own — mono 48 kHz WAV, one file per case)
  $dest/wind/tones          chromatic long tones (one per note) + one altissimo note
  $dest/wind/articulation   tongued same-pitch repeats at two tempi, a slurred scale, a scoop
  $dest/wind/rooms          takes with the app's own playback audible from speakers

Name each file for its CONCERT (sounding) pitch — note name or MIDI key,
then free description after "-": c5.wav, c5-mf-vibrato.wav,
72-altissimo.wav. A sax player reads written pitch, so convert first:
written D5 on a soprano sax = concert C5 = key 72.
EOF
}

target_list() {
  cat <<'EOF'
scores:
  https://github.com/CoderLine/alphaTab  packages/alphatab/test-data  (minus musicxml-samples/)
  https://github.com/musescore/MuseScore src/importexport/musicxml/tests/data
  https://github.com/musescore/MuseScore src/importexport/guitarpro/tests
guitarset:
  https://zenodo.org/records/3371780  annotation.zip, audio_mono-mic.zip, audio_mono-pickup_mix.zip
techs:
  https://zenodo.org/records/14963133 P1_techniques.zip, P1_chords.zip
egfx:
  https://zenodo.org/records/7044411  Clean.zip
wind:
  (no URLs — record your own; creates testdata/real/wind/{tones,articulation,rooms})
EOF
}

[ $# -gt 0 ] || { usage; exit 2; }
for t in "$@"; do
  case "$t" in
    scores)    target_scores ;;
    guitarset) target_guitarset ;;
    techs)     target_techs ;;
    egfx)      target_egfx ;;
    wind)      target_wind ;;
    list)      target_list ;;
    -h|--help) usage ;;
    *) echo "unknown target: $t" >&2; usage; exit 2 ;;
  esac
done
