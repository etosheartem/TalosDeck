# Interface rebuild — operator-console

This is a replacement of the presentation layer, not a theme over legacy components.

- [x] New application shell and navigation: overview, nodes, workloads, storage, configuration, etcd, backups, maintenance, audit, settings.
- [x] New shared table, status, empty/loading/error states, native accessible dialogs, node inspector.
- [x] New resource screens with filtering, sorting and detail inspection.
- [x] Preserve services, live logs, reboot, maintenance, backups, worker provisioning, alerts and authentication.
- [x] Strict API reads: no synthetic cluster data on connection failure.
- [x] Isolated styles with readable 13–14 px body text and responsive layout.
- [x] Verify build, browser errors, navigation, filters, dialogs, mobile and failure states.
- [x] Push only the redesign branch; include an unmistakable UI revision indicator.

## Implementation

The entry point loads `console/ConsoleApp.vue` and `console/console.css`. No legacy
view, modal, shell component or legacy stylesheet is imported by the new UI.
Backend actions reuse existing API functions; reads use the strict console client.

## Verification

- `cd web && bun run build`
- `cd web && bun run test:ui` (Chromium at `/usr/bin/chromium`; override with `CHROMIUM_PATH`)
- Browser scenarios use intercepted fixture responses. They do not mutate a real cluster.
- Tests cover all ten pages, login, filters, node inspection, log stream and pause,
  disk/etcd response normalization, dialog Escape, provisioning, cancelled reboot,
  alerts save, mobile navigation, horizontal overflow and unavailable APIs.
- Screenshots are written to a temporary directory printed by the test runner.
  `UI_SCREENSHOT_DIR` can select another destination.

## Preview

Run `TALOSCONFIG=/path/to/talosconfig make run` on `redesign/operator-console`,
then open `http://localhost:8080/#overview`. Stop any previously running binary first.
The new interface has ten navigation entries and the footer reads **Console UI 2**.

## Scope notes

The new interface uses Russian labels with standard infrastructure terms in English.
Legacy RU/EN translation files remain in the repository but are not loaded by the new UI.
Real reboot, provisioning, backup and notification side effects were not executed
against the user's cluster; their UI/request flows were checked with fixtures.
