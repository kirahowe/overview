# Show all of the help when the list pane is short

## Context

`?` draws the keys as a box over the list's own pane (`helpBox` in
`internal/tui/view.go`). The list pane is narrow, so the help goes in one
column, and the box is cut to the pane's height. On a 45-row terminal the
bottom of it is lost: the mouse and general sections, with `q` and `Q`.
This already happened before the panels landed on 2026-10-24, and their
section made the help six lines longer. The help cannot scroll.

## Acceptance criteria

- [x] Every key in the help can be read at any window size the list
      works at, whether by giving the help more room, by scrolling it,
      or by both.
- [x] A property or table test covers a short, narrow list pane.

## Plan

Keep the help over the list's pane and let it scroll when it does not fit.

## Log

- 2026-10-25: The help scrolls with `j`/`k`, the arrows, `ctrl+d`/`ctrl+u`,
  `pgdn`/`pgup`, `g`/`G` and the wheel. A dim `↑ more` or `↓ more` marks the
  cut edge, and any other key closes it. The scroll is UI state changed by
  actions and clamped in `settle` after a resize. `TestPropHelpScrollsToEveryLine`
  checks every line is reachable and none is wider than the pane;
  `TestHelpScrollsOnAShortPane` covers a 36x12 pane.
