# Panels and the keys between them

Written 2026-10-24. This is the design for drawing every part of the
window the same way, as a panel. Where the keyboard is and how it moves
between panels, including the panels mode, is in
[focus-design.md](focus-design.md).

## One look

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

Focus is tmux's active pane, and the panels mode moves it with h, j, k
and l. Both are in focus-design.md. The look depends on them in one way:
the rule of a pane in the panels mode carries the note `· enter to act`
after its title, from `pane-border-format` reading `client_key_table`.

## One way in

Every key, mouse and focus event is translated into actions, and `apply`
is the only place they change the model. Where the user is, the keyboard
back from a pane (`keysBack`), gone to one (`keysLeft`), or the mouse over
the list (`pointerHere`), is an action of its own, applied before the
input is translated, so translation reads the model as it then is. Nothing
in a handler talks to tmux: what reaches tmux comes back from `apply`,
`restage` or `reframe` as a Cmd.
