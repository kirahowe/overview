# Panels and the keys between them

Written 2026-10-24. This is the design for drawing every part of the
window the same way, as a panel, and for moving the keyboard between
panels with h, j, k and l, as relay's chat client does.

## One look

Until now the cockpit drew its cards as closed boxes while the list used
open section rules, and the session beside the list had no frame at all.
A live session is a tmux pane, and the view cannot draw inside it, so
whatever frames a session has to come from tmux. tmux can draw a rule
along the top of every pane with a title set into it
(`pane-border-status top`), but not a closed box at the window's edges.
So the shared look is the one tmux can draw: a panel is a rule along its
top, `── title ───── note ──`, with single lines between panels.

- tmux draws the rules of its panes. The view sets the border options
  once as it starts (`tmux.Frame`), and gives each pane of its session a
  title in the pane option `@overview-title`, tmux styles and all, with
  every `#` doubled. The theme's colours are session options, so two
  homes on one server keep their own.
- The view draws the same rule for the panels it draws itself: the
  agents, under the projects in the list's pane, and the cockpit's cards.
  `panelRule` is that rule, and the cockpit joins rules to the lines
  between panels with `┬ ├ ┤ ┼ ┴`.
- The cockpit zooms the list's pane and draws the whole window itself,
  so the window's rules go while it is zoomed (`SetZoom` and `SetFrame`),
  and the cockpit draws the list's rule in their place.
- A project's section in the list keeps its own lighter rule, inset by a
  space, so a section never looks like a panel.

The panel with the keyboard has its rule in the accent colour. For a tmux
pane that is its active pane, and tmux lights it: `pane-active-border-style`
is the accent and `pane-border-style` the border colour, and
`pane-border-format` sets the title in the accent, bold, while the pane is
active (`pane_active`) and in the text colour otherwise. The view sets
nothing per pane to light one.

The border between two panes is lit along its whole length. tmux's
`pane-border-indicators` would light only half of it in a window of two
panes, so `tmux.Frame` turns them off. tmux then styles a shared border
from the active pane, and the active pane's rule fill is lit too. Inside the
view's own pane the view lights what it draws: the agents' rule while the
cursor is in the agents and the keyboard is in the view, and in the cockpit
the list's rule or the issues card's, whichever has the keys. Focus and its
reasons are in focus-design.md.

`frames` works out every pane's title from the model, and `reframe` sends
tmux only what changed, one Cmd at a time, as `restage` does for the
panes themselves.

## Moving between panels

Focus is tmux's active pane, and there is one. The keyboard starts out in
the list, where `j` and `k` move the cursor, crossing from the projects
into the agents, and `l`, `enter` and `tab` open the cursor's entry, which
moves the keys into its pane. In a tmux pane `esc` belongs to the program,
so `ctrl+[` comes back from there. However the keyboard comes back to the
view's pane, by `ctrl+[`, a tmux key or a click, it lands in the list on
the cursor, so `j` and `k` move it at once. Nothing is selected apart from
focus, so this holds without any bookkeeping.

To move between panes without typing into one, `esc` in the list puts the
client in the panels mode. It is the tmux key table `overview-panels`,
which `tmux.BindPanels` binds as the view starts: `h j k l` and the arrows
run `select-pane` and re-enter the table (a key of a table other than the
root drops back to it once it has run), `enter` and `esc` return to the
root, and `ctrl+[` selects the list's pane and returns to the root. A key
the table does not bind leaves the mode and tmux looks it up again in the
root table, so the mouse and the prefix work as ever, and a key bound
nowhere is dropped. A move off the window's edge wraps around, as
`select-pane` does. In the mode the focused pane's rule gets a note after its title,
`enter to act`, from `pane-border-format` reading `client_key_table`. The
cursor's row has no background and the footer starts with `panels`, so a
row with a background always means the keys move it.

The view puts the client there with `switch-client -T overview-panels`
through the mux seam (`EnterPanels`), and sets its `panels` flag at once.
It learns the mode otherwise from `list-clients`, which `tmux.Panes` folds
into the pane listing, and a key that reaches the view clears the flag,
since the mode keeps every key for tmux. The Cmd bumps `spawns` like a
spawn does, so a listing taken before the mode cannot undo it.

The cockpit has no panels mode, since it zooms the list's pane and there is
nothing to move to. `esc` there clears a filter, then closes the cockpit.
`tab` moves the keys between the list and the issues card, which is a
plain flag, `cockpit.issues`. The rest of the cards only show.

The earlier design kept a selected panel apart from the active pane, moved
it with `h j k l` in the view and lit it through the pane option
`@overview-select`. tmux styles a border cell from one pane, so the
workarounds lit cells in the wrong places and could not light the agents.
focus-design.md says why that went.

## One way in

Every key, mouse and focus event is translated into actions, and `apply`
is the only place they change the model. Where the user is, the keyboard
back from a pane (`keysBack`), gone to one (`keysLeft`), or the mouse over
the list (`pointerHere`), is an action of its own, applied before the
input is translated, so translation reads the model as it then is. Nothing
in a handler talks to tmux: what reaches tmux comes back from `apply`,
`restage` or `reframe` as a Cmd.
