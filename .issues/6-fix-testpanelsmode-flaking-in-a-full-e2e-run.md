# Fix TestPanelsMode flaking in a full e2e run

## Context

## Acceptance criteria

- [ ] 

## Plan

## Log
## Context

`TestPanelsMode` in `e2e/e2e_test.go` failed once in a full `scripts/e2e`
run on 2026-11-17, at line 1024:

    timed out after 30s waiting for the keys in the list; last saw: %2

The test selects the list's pane itself and then waits for it to be the
active pane, but the session's pane, `%2`, stayed active. The test passed
when run alone twice, eight times in a row with `-count=8`, and in a
second full run. The failing run came with a change to `tmux.Move` that
shows the window with `switch-client -c` in place of `select-window`, so
that change hasn't been ruled out, though the repeat runs point to a
race: something in the view moving the keyboard after the test's own
`select-pane`.

Related: #3, the same kind of race in other real-tmux tests.

## Acceptance criteria

- [ ] The cause of the failure is found and named.
- [ ] `TestPanelsMode` passes in 20 full `scripts/e2e` runs in a row.
