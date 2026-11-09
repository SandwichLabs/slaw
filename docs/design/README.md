# Designs

Studio's designs are kept as Claude design artifacts, not as files in this folder. Each one is built from the shipped code: the `--slaw-*` roles in `studio/frontend/src/theme/`, the SchoolHouse kit in `studio/frontend/src/schoolhouse/`, and the icons in `src/icons.ts`. **The code is the source of truth.** Where an artifact and the code disagree, the code wins and the artifact gets updated.

| Artifact | What it holds |
|---|---|
| [Slaw Design System](https://claude.ai/artifact/LrBzD4cQ6BYYoseLnuFP9a) | Colour tokens for the four built-in themes (named exactly as the `--slaw-*` CSS variables, plus the bridged `--sh-*` aliases), type, spacing, radii and shadows. Also 21 components with previews (buttons, fields, status icons, scope badges, banners, toasts, dialogs, menus, the shell, tables, tool chips, icons and the A2UI stat, callout and charts), and the voice and theming rules. Built from `f0cfa45`. Contrast failures it found are tracked in #252. |
| [Workspaces & Connections](https://claude.ai/artifact/B6QJxYhRkgSm1tiAT1jVH9) | The vibe-app#106 mockups (launcher, new workspace, import, shell and switcher, connections rail and editor, states, query editor and cells, settings, agent approval) moved onto a canvas: 39 artboards with every state from the spec. A note under each screen records where it now follows shipped Studio rather than the 2026-10 mockup: the Library nav, the age-encrypted bundle import, the twelve connection types, Decline on approval cards, and so on. |
| [Answers You Can Check](https://claude.ai/artifact/3rVxGVVeKW6CR14JCPCGVT) | Proposed screens for [`plans/answers-you-can-check.md`](../plans/answers-you-can-check.md) (#250). Nothing on it is built yet. Screens include: the evidence drawer, its states and Settings > Workspace > Receipts (#241, design pass 2026-11-09 for `plans/run-receipts.md`), receipt chips, the verifier's checking, agreement and disagreement states (#243), semantic proposals (#244), the Check workspace report (#245), as-of and snapshot diff (#246), watchers (#247), and the aggregate-only tier (#249). |

The artifacts are private to the maintainer until shared. The original HTML mockups and their spec, which are public, are in [`../archive/design/workspaces-connections/`](../archive/design/workspaces-connections/SPEC.md).

To change a design, edit the artifact, or ask Claude Code to update it. When a design ships, the code becomes the record and the artifact's notes should say what changed. Re-sync the design system from the code after theme or kit changes.
