# Design notes

README.md at the root says what overview does. These notes say why it is
built that way. Each describes the design as it is, checked against the
code on 2026-10-25.

- [model-design.md](model-design.md): where each fact comes from, the one
  place facts are joined, and how the view turns that into the screen.
  Current.
- [focus-design.md](focus-design.md): tmux's active pane as the one focus,
  the panels mode, and where the keys land. Current.
- [panels-design.md](panels-design.md): the titled rule every panel is
  drawn with, and who draws it. Current.
- [workstreams-design.md](workstreams-design.md): grouping sessions by
  issue, the list, the cockpit and the task picker. Current; phase 4
  (tabs) is open.
- [review-design.md](review-design.md): the diff viewer, notes, and
  sending them to a session. Current.
- [archive/ghostty-handoff.md](archive/ghostty-handoff.md): the plan for
  driving stock Ghostty. Historical; the code was removed on 2026-10-18.
