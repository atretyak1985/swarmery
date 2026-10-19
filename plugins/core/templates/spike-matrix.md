# Spike matrix — <guarantee under test>

Fill one matrix per engine guarantee a plan relies on — anything that promises a
person sees something (a permission dialog, a prompt, a notice). A guarantee
that held in one permission mode is not evidence for another: auto mode, for
one, can settle an `ask` without showing anything. See
`workspace-plans/resources/plan-format.md` → "Engine guarantees: spike every
mode".

- **Guarantee:** <what the plan promises, in one sentence>
- **Engine version:** <CLI version the matrix was measured on>
- **Date:** <YYYY-MM-DD>
- **Setup:** <isolated environment, settings file, plugin dir — no live state>

Observed verdict: `holds` | `fails` | `not measured` (with the reason). A cell
left `not measured` is a gap the plan must name, never an implied `holds`.

| Permission mode | Surface | Expected (what the person sees) | Observed verdict | Evidence file |
|---|---|---|---|---|
| default | `-p` | | | |
| default | interactive | | | |
| acceptEdits | `-p` | | | |
| acceptEdits | interactive | | | |
| plan | `-p` | | | |
| plan | interactive | | | |
| auto | `-p` | | | |
| auto | interactive | | | |
| dontAsk | `-p` | | | |
| dontAsk | interactive | | | |
| bypassPermissions | `-p` | | | |
| bypassPermissions | interactive | | | |
