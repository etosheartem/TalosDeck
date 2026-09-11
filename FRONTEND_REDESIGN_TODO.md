# TalosDeck frontend redesign

The frontend should read like an operator console: dense, calm, predictable, and useful during an incident. Visual decoration must never compete with cluster state.

## Design rules

- [x] Define one neutral color system and reusable surface, border, control, and status tokens.
- [x] Remove gradients, ambient glows, glass effects, pulsing healthy states, and ornamental shadows from the main shell.
- [x] Use semantic color only for health, warnings, destructive actions, and the current navigation item.
- [x] Keep operational values in a monospace font and interface copy in a sans-serif font.
- [x] Reduce corner radii and keep spacing on a compact 4/8 px rhythm.
- [x] Make keyboard focus visible on every interactive control.

## Information architecture

- [x] Simplify the sidebar into product identity, cluster context, navigation, and locale.
- [x] Make the top bar show the current section, endpoint, refresh cadence, identity, and refresh action.
- [x] Replace the overview card grid with a compact cluster status strip.
- [x] Replace node cards with a full-width operations table.
- [x] Keep node services, logs, and reboot actions available from each row.
- [ ] Move alert configuration from Operations into a dedicated Settings area.
- [x] Add a persistent problems panel to the cluster overview.
- [ ] Add a node details drawer so routine inspection does not open several modals.

## Data states

- [x] Distinguish demo data from live cluster data in the page status area.
- [x] Preserve separate empty states for a disconnected cluster and an empty filter result.
- [x] Surface refresh failures instead of only writing them to the browser console.
- [x] Show the timestamp of the last successful refresh.
- [x] Remove production fallback values from Proxmox metrics.
- [x] Add a loading skeleton for the primary nodes table.

## Remaining screens

- [x] Convert Storage to the shared page header, toolbar, table, and empty-state patterns.
- [x] Convert Workloads to the shared page header, toolbar, table, and status patterns.
- [x] Rework MachineConfig around a focused editor layout.
- [ ] Split Operations into etcd health, backups, maintenance, and audit sections with quieter controls.
- [ ] Normalize modal layout, button hierarchy, form fields, and destructive confirmations.
- [ ] Review Russian and English copy for short operator-oriented labels.

## Validation

- [x] Verify the production frontend build.
- [ ] Check desktop widths at 1280, 1440, and 1920 px.
- [ ] Check mobile navigation and horizontal table behavior at 390 px.
- [ ] Check keyboard navigation, focus order, and modal focus management.
- [ ] Check contrast for neutral, warning, error, and disabled states.
