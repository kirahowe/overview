# Focus and the panels mode

Written 2026-10-24. This revises how the keyboard moves between panels
and how the window shows where it is. It replaces the selection that
panels-design.md describes, and keeps the panels' look.

## Why

The view kept a selected panel apart from tmux's active pane: on the
panels, h, j, k and l moved a selection while the keys stayed in the
view's pane. To light a pane that was selected but not active, the view
set `@overview-select` and the border formats worked around how tmux
styles borders. tmux styles each border cell from one pane, the active
one when it touches it, in one colour for all that pane's cells. So the
workarounds lit cells in the wrong places, and could not light the side
of the agents, which are inside the view's pane. We were fighting tmux.

Other programs keep two things apart. Focus is which pane you are in,
and there is one. Mode is whether your keys go to the program in that
pane or to the program around it. Neovim's terminal windows, Zellij's
locked mode and tmux's own copy mode all work this way. A selection that
is not focus is not among them.

## Two levels

**tmux's panes.** Focus is tmux's active pane, and tmux draws it:
`pane-active-border-style` in the accent, `pane-border-style` in the
border colour, and `pane-border-indicators off`, so the whole border of
the active pane is lit and not half of it. The view sets nothing per
pane to light one. The view's own pane is a tmux pane like the rest.

**Inside the view's pane.** The view draws everything there, so it shows
its own focus there: the projects or the agents, whichever holds the
cursor, and in the cockpit the list or the issues card, whichever has
the keys (tab moves between them). The view lights that part's rule
while its pane has the keys. This level never uses tmux's borders.

When the keys are in the agents, tmux lights the view's pane, the rule
along its top included, and the view lights the agents' rule: the pane
has the keys, and in it the agents do.

## The panels mode

The panels mode is a mode on the focused pane. In it, keys move the
focus and do not reach the program. It is tmux's key table
`overview-panels`, bound when the view starts, beside `tmux.Frame`
(not in the default tmux.conf, which is the user's after the first
launch):

- h, j, k, l and the arrows: `select-pane -L/-D/-U/-R`, then back into
  the table. The focus, and tmux's highlight, move at once.
- enter and esc: back to the root table. The keys now land in the
  focused pane.
- ctrl+[: to the list (`select-pane -t '{top-left}'`), back to root.
- Any other key leaves the mode, and tmux looks it up again in the root
  table: the mouse, the prefix and the user's own `bind -n` keys work as
  they always do. A key bound nowhere is dropped, so a letter typed by
  mistake does not reach a session.
- A move off the edge of the window wraps around, as `select-pane`
  does: `h` in the list goes to the rightmost pane.

While a client is in the mode, the focused pane's rule says so after its
title (`pane-border-format` reads `client_key_table`), so the highlight
reads "enter to act here".

esc in the list, with no filter to clear and nothing else open, puts the
client in the mode. ctrl+[ in the list sends esc, so it does too. The
cockpit has no panels mode: it zooms the list's pane, so there is nothing
to move between. esc there clears a filter, then closes the cockpit.

The view reads whether a client of its session is in the mode as it
lists the panes, and right away when it puts one there. While the mode
is on and its pane is focused, the cursor's row has no background and
the footer starts with "panels", so a row with a background still means
j and k move it.

## What stays

From the commit before this one: the keys start in the list; however
they come back into the view's pane, by ctrl+[, by tmux's keys or by a
click, they land in the list on the cursor; and a list panel that goes
away leaves the keys in the projects. With no selection apart from
focus, these hold by construction.

## What goes

- `nav`'s selection (`panel`, `prev`, `inside`), `navMove`, `navEnter`,
  `navOut`, `navStep`, `beyond`, `panels()`, `stagePanel`, the panels'
  h, j, k, l and enter in the view, and `n` selecting the stage there.
- `@overview-select`, the `lit` and `edge` formats, and the rule fill
  that `pane-border-format` drew. The format keeps the title, lit by
  `pane_active`.
- The selection in `frames()`. It keeps the titles.
- The cockpit's first esc going out to the panels.

## Notes from the build

- tmux redraws the rules by itself when a key in a binding changes the
  key table or the active pane. It does not when `switch-client -T` comes
  from outside a client, as the view's esc does, so `EnterPanels` follows
  it with `refresh-client`.
- The view reads the mode from `list-clients`, which `tmux.Panes` runs in
  the same call as `list-panes` and folds into `Pane.Panels`. A key that
  reaches the view clears its flag, since the mode keeps every key for
  tmux. The Cmd that enters the mode holds listings off until it lands,
  as a spawn does, so an older listing cannot undo it.
- ctrl+[ and esc are two keys to tmux only when the terminal sends
  extended keys, so `overview-panels` binds both. Without them ctrl+[
  arrives as esc and leaves the mode in the pane that has the focus.
- Typing a motion key right after esc races the view: it has to reach
  tmux with `switch-client` before the next key does. A key typed inside
  those few milliseconds is the list's own, so `l` there opens the entry.
