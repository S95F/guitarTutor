fixture_riff.gp — provenance and an honesty note
================================================

This file is the canonical 4-bar fixture riff (docs/TEXTFORMAT.md) as a
Guitar Pro 7/8-style .gp archive: a plain ZIP holding Content/score.gpif,
the id-referential GPIF XML document. It is generated, not exported:

    go run ./tools/gengp

regenerates it byte-for-byte. Real Guitar Pro 8 exports also carry
sidecar entries (a VERSION marker, binary stylesheets, layout and part
configuration). Those cannot be reproduced faithfully here, so they are
omitted rather than faked; internal/gpimport ignores every archive entry
except the gpif, and its tests cover archives with extra entries.

The honest caveat: this fixture is self-authored. The importer
(internal/gpimport) and the generator (tools/gengp) are both written
clean-room from the publicly documented structure of the format
(docs/DECISIONS.md D3 — the reference implementations are MPL/LGPL and
were not ported), which means they necessarily share one understanding
of that structure. A file exported by Guitar Pro itself is the one thing
this corpus cannot verify.

If you have a real .gp file (exported by Guitar Pro 7 or 8) that fails
to import, imports with wrong notes/timing, or produces surprising
warnings, please file a bug report and attach the file (or a minimal
piece saved from it). Real-world files are exactly the validation gap
we want to close.
