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

The selected panel's rule is in the accent colour. For a tmux pane that is
the pane option `@overview-select`, which the style formats read; tmux
lights the pane with the keyboard by itself, except the view's own,
which has the keyboard whenever the user is choosing a panel.

`frames` works out every pane's title and selection from the model, and
`reframe` sends tmux only what changed, one Cmd at a time, as `restage`
does for the panes themselves.

## Moving between panels

The keyboard starts out on the panels. `h j k l` select the nearest panel
that way, `enter` goes into the selected one, and `esc` comes back out.
In a tmux pane `esc` belongs to the program, so `ctrl+[` comes back from
there, as it always has, and however the keyboard comes back, the pane it
left is the one selected.

The panels are the projects and the agents, the tmux panes of the list's
window, and in the cockpit the cards that take keys: the header and the
issues. The pane showing the cursor's entry is one panel, the stage,
whichever pane that is, so selecting it survives the cursor moving.

- `navStep` is pure: from the panels' rectangles it picks the nearest
  panel wholly in the direction, those facing the one left before those
  off to the side, and on a tie the one the selection came from, so a move
  back returns.
- `keepNav` keeps the selection on a panel that is there, after every
  update, as `keepCursor` does for the cursor.
- Inside the list the cursor walks from the projects into the agents as
  before, and the selection follows it.
- The keys that act on the cursor's entry work out on the panels too.
  `n` there selects the stage, so `n` then `enter` goes straight to what
  needs you.

## One way in

Every key, mouse and focus event is translated into actions, and `apply`
is the only place they change the model. Where the user is, the keyboard
back from a pane (`keysBack`), gone to one (`keysLeft`), or the mouse over
the list (`pointerHere`), is an action of its own, applied before the
input is translated, so translation reads the model as it then is. Nothing
in a handler talks to tmux: what reaches tmux comes back from `apply`,
`restage` or `reframe` as a Cmd.
