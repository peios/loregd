# Documentation Maintenance

The maintained runtime documentation lives in `docs/learn/` and
`docs/man/loregd.8`. Repository build instructions live in `docs/building.md`.
The public manual describes storage and protocol behavior; it does not
define requirements for other RSI sources.

Follow Panorama's Peios Style Code technical-writing rules. Keep named
anchors and the `~peios/loregd/...` article references stable. Add runtime
behavior changes to the relevant manual article and the man page when they
affect an operator.

## Validate documentation

Set `TRAIL` to the pinned Trail executable used by CI, then run:

```sh
tools/check-docs
```

The component check lints the Learn tree and builds it strictly. Its only
allowed unresolved links are the exact external diagnostics recorded in
`docs/external-links.json`. An added or removed diagnostic fails the check;
the baseline is not a general exemption for broken links.

Validate the complete publication context before merging documentation:

```sh
tools/check-docs --context ../panorama/docs
```

This copies the small publication source tree without generated output,
resolves its merge paths against the original root, and runs a strict
combined build. It resolves the component's kernel references without
copying cross-component documentation into loregd.

Render the manual page with groff:

```sh
groff -man -Tutf8 docs/man/loregd.8
```

## Archived sources

`docs/archive/pre-bazel/source-map.json` maps the original staging manual
and man page to their maintained replacements. Each entry includes the
SHA-256 of its archived original. Archived prose is preserved as evidence
and excluded from the published Learn tree and its lint pass.

Legacy named anchors retain their original spelling so existing references
continue to resolve. Narrow lint directives on those identifier-only lines
preserve that compatibility; prose around them follows the house style.
