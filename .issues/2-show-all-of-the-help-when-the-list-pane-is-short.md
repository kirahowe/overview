# Show all of the help when the list pane is short

## Context

`?` draws the keys as a box over the list's own pane (`helpBox` in
`internal/tui/view.go`). The list pane is narrow, so the help goes in one
column, and the box is cut to the pane's height. On a 45-row terminal the
bottom of it is lost: the mouse and general sections, with `q` and `Q`.
This already happened before the panels landed on 2026-10-24, and their
section made the help six lines longer. The help cannot scroll.

## Acceptance criteria

- [ ] Every key in the help can be read at any window size the list
      works at, whether by giving the help more room, by scrolling it,
      or by both.
- [ ] A property or table test covers a short, narrow list pane.

## Plan

Undecided. The options so far: draw the help over the whole window
rather than the list's pane, as the cockpit does when it zooms; let
`j`/`k` scroll it; or tighten its sections.

## Log
