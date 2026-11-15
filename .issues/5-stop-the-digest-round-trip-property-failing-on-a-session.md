# Stop the digest round-trip property failing on a session named do

## Context

`TestPropParseDigestReadsWhatTheInstructionsAskFor` in
`internal/fleet/props_test.go` fails now and then, and `scripts/check`
fails with it. It failed once on 2026-11-14 during the work on the link
reader, which does not touch the digest. With more cases and no saved
seed it fails every time:

```
go test ./internal/fleet -run TestPropParseDigestReadsWhatTheInstructionsAskFor -rapid.checks=200000 -rapid.nofailfile -count=1
```

On main at `61a4b7c` that failed after 1710 cases. At the 100 cases of
an ordinary run, that one sample comes to about one run in 17.

The case rapid shrinks it to is three empty sections and then a section
with two items: plain text `a`, then an item whose only name is `do`,
with no project, issue or fields. `formatDigest` writes the second item
as `- **do**: `. `ParseDigest` reads that line as the `Do:` field of the
item above it and makes no second item:

```
read  {Title:a Items:[{Names:[] Issue:a}]}
want  {Title:a Items:[{Names:[] Issue:a} {Names:[do]}]}
```

Two things meet here. `digestField` in `internal/fleet/digestdoc.go`
takes `**do**:` for a bold label, and `ParseDigest` files a labelled
line under the item above it "wherever it stands, as a model does not
always indent". `genDigestDoc` draws names from
`[a-z0-9][a-z0-9-]{0,7}`, which can be `do`, `why` or `since`. The
comment on its `words` helper says the text it draws is never the label
of a field, but names do not go through `words`.

AGENTS.md asks that a failing property be traced to the code or the
property before either changes. The question is whether a session can
be named `do`, `why` or `since`. If it can, the parser has to tell the
item `**do**:` at the left edge from a `**Do:**` field. If it cannot,
the generator should not draw those names.

## Acceptance criteria

- [ ] The Log says which of the parser and the generator was wrong, and why
- [ ] The command above passes at 200000 cases
- [ ] A named regression test covers a digest item named `do`, `why` or `since`, whichever way that is decided
- [ ] `scripts/check --deep` passes

## Plan

## Log

- 2026-11-14: filed from the session that changed the link reader. The
  failure and the shrunk case above are from that session. Nothing was
  changed, and no `.fail` seed was kept.
