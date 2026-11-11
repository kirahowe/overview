# Focus and the panels mode

Written 2026-10-24. This is the design for where the keyboard is, how it
moves between panels, and how the window shows where it is. How the
panels are drawn is in [panels-design.md](panels-design.md).

## Why

An earlier version kept a selected panel apart from tmux's active pane:
h, j, k and l moved a selection while the keys stayed in the view's pane,
and the view lit the selected pane through a pane option and the border
formats. tmux styles each border cell from one pane, the active one when
it touches it, in one colour for all that pane's cells. The workarounds
lit cells in the wrong places and could not light the side of the agents,
which are inside the view's pane. Working against tmux's borders does not
pay, so the design uses its active pane as the one focus.

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
pane to light one. The view has two panes of its own, the list's and the
agents' under it, and both are tmux panes like the rest.

**Inside a pane of the view's.** Where the view draws more than one
panel in a pane, it shows its own focus there: in the cockpit the list
or the issues card, whichever has the keys (tab moves between them). The
view lights that part's rule while its pane has the keys. This level
never uses tmux's borders.

## The agents' pane

The agents were once a panel drawn at the foot of the list's pane, and
the cursor walked from the list on into them. That left them out of
everything that moves tmux's focus: the panels mode and the user's own
bindings are `select-pane`, which goes from pane to pane and knows
nothing of what a pane draws inside itself. So the agents have a tmux
pane, and nothing has to pretend.

- `tmux.Under` splits it off the foot of the list's pane and marks it
  with `@overview-agents`. `tmux.Move` takes it along when the list
  changes window, in the same tmux command, and leaves the keyboard in
  whichever of the two had it. The pane is part of the list's column, so
  it is not company for the list: `panesIn` and `tmux.DiedHook` leave it
  out when they ask whether the list is alone.
- One process still decides what both panes show. The pane runs
  `overview agents <socket>`, which is `tui.Relay`: it passes each key,
  click, focus event and its size to the view over a unix socket beside
  tmux's, a line of JSON each, and shows the screen the view sends back.
  The view takes them as it takes its own pane's, tagged with the pane
  they came from, and `apply` stays the one place they change the model.
- `keepAgents` holds the pane to what the listing calls for, a step at a
  time, as `agentsPlan` decides: open it when sessions are listed, size
  it to its lines, close it with the last session, and close any that is
  not under the list.
- The footer is the last line of the column, so it is the last line of
  the agents' pane while that is open, and the list's pane has none.
- The cursor stays one cursor, which is what the stage follows. j and k
  stop at the end of the side they are on (`side`). When the keyboard
  arrives in one of the two panes the cursor goes to that side, to the
  same session's entry there when it was on a session (`across`), so the
  stage does not move because the focus did.
- The list's pane draws the agents itself, as it used to, whenever the
  pane is not showing them (`apart` is false): before the pane's program
  has connected, in a window too short for it, and while the cockpit or
  the task picker has the list zoomed, since a zoom hides every other
  pane. There the cursor walks on into the agents as before.

Two panes of one program can each tell the view that the keyboard has
come or gone, and the two reports of one move arrive in either order. So
a pane's blur counts only while the view thinks the keyboard is in that
pane.

## The panels mode

The panels mode is a mode on the focused pane. In it, keys move the
focus and do not reach the program. It is tmux's key table
`overview-panels`, which `tmux.BindPanels` binds as the view starts
(the default tmux.conf does not hold it):

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
title (`pane-border-format` reads `client_key_table`) with the note
`· enter to act`.

esc in the list, with no filter to clear, puts the client in the mode.
ctrl+[ in the list sends esc, so it does too. `tmux.EnterPanels` does it
with `switch-client -T overview-panels`.

The cockpit has no panels mode: it zooms the list's pane, so there is no
pane to move to. esc there clears a filter, then closes the cockpit. Its
cards are drawn by the view, so the keys move between them without tmux:
`l` or the right arrow moves them from the list into the issues card,
`h` or the left arrow back, and tab does either. Where they are is a
plain flag, `cockpit.issues`. The other cards only show.

The view reads whether a client of its session is in the mode as it
lists the panes, and right away when it puts one there. While the mode
is on and its pane is focused, the cursor's row has no background, so a
row with a background still means j and k move it. While the mode is
on, whichever pane has the focus, the footer starts with "panels" and
shows the mode's keys, `ctrl+[ list` among them, since the list's pane
is where the user looks for the way back.

One view serves every client attached to its session, and the listing
says only that some client of the session is in the mode, not which.
So while one client is in the mode, every client's footer shows the
panels keys. tmux's own rule, which reads `client_key_table` per
client, is the one place that tells them apart.

## Where the keys land

The keys start in the list. However they come back into the list's pane,
by ctrl+[, by tmux's keys or by a click, they land in the list on the
cursor, so j and k move it at once. In the agents' pane they land on the
cursor among the agents. If that pane goes while it has the keys, they
are taken to be outside the view until a pane reports them. With no
selection apart from focus, these hold with no more bookkeeping than
which of the view's two panes has the keys.

## Details that bite

- tmux redraws the rules by itself when a key in a binding changes the
  key table or the active pane. It does not when `switch-client -T` comes
  from outside a client, as the view's esc does, so `EnterPanels` follows
  it with `refresh-client`.
- The view reads the mode from `list-clients`, which `tmux.Panes` runs in
  the same call as `list-panes` and folds into `Pane.Panels`. A key that
  reaches the view clears its flag, since the mode keeps every key for
  tmux. The Cmd that enters the mode holds listings off until it lands,
  as a spawn does, so an older listing cannot undo it.
- A move that carries the agents' pane with the keys in it would make
  the list active on the way, and tmux would report the keyboard to
  both programs. `tmux.Move` moves the list with `-d` then. tmux still
  tells the agents' pane the keyboard went and came back, so while a
  move is in flight the cursor does not follow the list's window, as it
  otherwise does while the keyboard is outside the view.
- A split or a resize ends a window's zoom, and a pane that is being
  split off is not listed yet. So while a step on the agents' pane is
  out (`agentsBusy`), the list does not move and the cockpit and the
  task picker wait to zoom it.
- ctrl+[ and esc are two keys to tmux only when the terminal sends
  extended keys, so `overview-panels` binds both. Without them ctrl+[
  arrives as esc and leaves the mode in the pane that has the focus.
- Typing a motion key right after esc races the view: it has to reach
  tmux with `switch-client` before the next key does. A key typed inside
  those few milliseconds is the list's own, so `l` there opens the entry.
