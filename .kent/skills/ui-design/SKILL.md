---
name: ui-design
description: Kent GUI design guide for desktop/web UI work. Use when designing or implementing Kent GUI screens, layouts, visual tokens, animations, writing React or Compose UI code.
---

## Product Principles

- 3 Big Principles: **Clean, elegant, effective.** . Everything reachable, everything dynamic.
- GUI is a remote-control surface. Server owns workflow/runtime truth; UI presents read models and sends explicit actions. Never try to circumvent server communication in GUI clients. Assume server api expansion as needed is part of feature work.
- Every visible state must explain what the operator can do next or why they cannot continue. Example: errors include "Try again" or "Go back" CTAs. Terminal states include "Return" or "Close" (for modals). Empty states include "Create project"/"Create task" etc. Error or empty state without at least one button is a failure.
- NEVER hardcode fonts or color HEX values. Always use theme tokens instead. If new color/typography style is added, introduce runtime-overridable theme token, not a compilation constant and not hardcoded string.

## Visual Model

- Use island-style UI: every major surface is a floating rounded island over a native blurred/glass window background.
- Keep island nesting low. Maximum is 4 island stack, with 4 reserved only for truly floating elements. Keep it at 1 or 2 for 80% of UI.
- Avoid flat full-bleed panels except for the underlying glass/material background.
- Use generous radius, soft borders, translucent fills, and subtle shadows to separate islands.
- Follow design system constants, do not hardcode colors, paddings, spacing, font settings, styles, shadows, animations.
- Keep density high enough for professional workflow tracking; island style must not waste board space.
- Floating islands should express hierarchy:
  - Main work island: Kanban board or current full-screen destination.
  - Secondary islands: navigation, filters, diagnostics, task detail dialogs/panes.
  - Transient islands: toasts, command/status popovers, loading/error overlays.

## Native Window

- Native window shape, border, and platform controls are part of the app surface.
- macOS uses blurred glass/vibrancy. Windows should map the same principle to acrylic surfaces.
- Window background adapts to light/dark theme.
- Feature components must not import Tauri/native APIs directly. Native window/material commands stay behind bridge/shell boundaries.

## Typography

- Use mono for IDs, paths, command snippets, status codes, session IDs, branch names, and log-like values.
- Use main font for headings, navigation, controls, and normal body text.

## Theme And Palette

- Always support both light and dark themes.
- Treat the palette as easy to change. Build semantic tokens first; do not hardcode final color choices in feature components.

## Layout

- Every widget, page, and destination is built using **adaptive layouts**. Define ONE layout that resizes dynamically to whatever window size. Always define proper text ellipsis, wrapping, truncation policy, content wrapping, relayout rules for different window sizes, assume range from mobile vertical to 4k ultrawide desktop. **Avoid dynamically sizing fonts and UI breakpoints.**
- Relaunch restores last known state, build state restoration support, including restoring content of input fields, forms, unsaved changes.
- Dialogs/sheets/popups are terminal destinations: A modal does not open another page on main surface, does not navigate to another modal, does not stack destinations, does not have an embedded navgraph. If user asks for a design that violates this principle, warn them and confirm they want to build bad UX.
- Every page, modal, and sometimes widgets has Loading and Error states. Reuse generic loading/error layouts defined in UI kit, but **always** account for screen-wide loading state. The user shouldn't have to mention that loading and error states should be built, build them yourself, don't let the app crash, don't show blank screens or stale content.
- Implement progressive loading whenever something is loaded in parallel, for example different lists, status labels etc. if you have or need async promise/coroutine fanout in logic, then you also need progressive loading on UI.
- Never allow unbounded growth of collections in memory - implement pagination and use it as part of feature work if dealing with any sort of collections that can grow beyond fixed points. Pagination uses infinite scroll, not buttons or page numbers.

## Navigation

- Use typed destinations and destination lifecycle.
- Screens/dialogs own subscriptions, view models, native processes, and cleanup through destination lifecycle.
- Dialog close/back behavior must be deterministic.
- Server disconnect/reconnect must not corrupt selected project/task route state.
- Prefer single-window product flow for MVP.

## Motion

- Animate every user-visible transition unless user/system reduced-motion says otherwise.
- Prefer quick, purposeful transitions over decorative motion.
- Use shared element transitions where they clarify continuity, especially:
  - board card to task detail dialog/pane
  - project card to project board
  - status badge movement across task/card/detail surfaces
  - dialog open/close from selected card
- Loading, reconnecting, queued, and progress states should animate subtly.
- Keep motion deterministic in tests; provide reduced/disabled motion mode for snapshots and accessibility.

## Components

- Never invent single-use widgets. First check the list of existing components before any layout work. If needed component is missing, define it not inside the feature, but directly in the UI Kit code. Built a card? Make it customizable and place it in the ui kit. When the user asks for a new feature, assume expanding and reusing ui-kit is part of the feature work, plan for it, account for it.
- Every disabled interactive control must have a tooltip that explains the exact typed reason when known, or the possible reasons only when one exact reason is unavailable. Use concise localized product language and reuse the UI-kit tooltip treatment established by Workflow Editor.
