# Upstream candidates — core-service changes (discussion branch)

**Status: NOT a PR.** This branch (`upstream-core-candidates`, based on
`upstream/main`) isolates the *core-service* changes from our XENEON EDGE fork
that may be worth upstreaming, so they can be discussed with the maintainer
before proposing anything. The Edge dashboard feature itself (widgets, kiosk,
Spotify, etc.) intentionally stays in the fork and is **not** here.

Fork's full feature: `hfyeomans/OpenLinkHub` branch `main` / `xeneon-edge`.

## Included here (clean, reusable, applies to current upstream/main)

1. **Multi-GPU + telemetry + storage in `src/systeminfo/systeminfo.go`**
   (upstream never modified this file, so it applies cleanly):
   - `GetGpuTemperatureIndex` companion `GetGPUUtilizationIndex(index)` —
     per-GPU utilization by index (multi-GPU support).
   - `GetGpuStats` / `GetGpuThroughput` / `GetGpuProcesses` (+ `gpuProcessType`,
     `enrichGpuProcessUtilization`, `enrichHostProcessInfo`) — per-GPU telemetry
     (clocks, power, fan, PCIe, rx/tx) and a GPU process table, via `nvidia-smi`.
   - `GetStorageTemperatureIndex(index)` — Nth hwmon nvme/drivetemp reading.
   - **Security fix applied:** `enrichHostProcessInfo` now stores the executable
     **basename** only, not the full argv (a full command line can leak secrets
     such as `--password=`/tokens, and any endpoint exposing it is unauthenticated).

2. **`src/templates/templates.go`** — adds `AssetVer` to the `Web` struct, the
   hook for a per-process static-asset cache-buster (generic; helps any page).

3. **`99-openlinkhub.rules`** — udev rule for Corsair `1d0d` (XENEON EDGE USB),
   needed for non-root device access. Pairs with enabling the device (below).

## Deferred — needs maintainer decision (documented, not committed)

- **Enable the XENEON EDGE device** (`src/devices/devices.go`: uncomment
  `7437 … xeneonedge.Init`). Upstream keeps it gated on purpose; enabling it is
  the maintainer's call and pulls in the device driver.
- **Widget-area model reconciliation.** Our fork normalized `WidgetArea`
  (`WidgetId`+`Span`, resolved at render); upstream keeps a `Widget` snapshot.
  These conflict directly and upstream owns the direction. This is the single
  largest merge decision — see `docs/xeneon-edge-upstream-pr.md` in the fork.
- **HTTP surface** (`src/server/server.go`, `src/server/requests/requests.go`).
  The generic bits worth upstreaming are the per-index GPU handlers
  (`getGpuTemperatureCleanIndex`/`getGpuLoadIndex`, factored via
  `indexedSensorHandler`) and the `no-cache` static handler + `?v=AssetVer`
  cache-buster. But upstream diverged heavily in both files (+304 / +73 lines),
  so these must be re-applied by hand onto upstream once scope is agreed —
  not force-merged.

## Not for upstream (fork feature)
`src/devices/xeneonedge/*`, `src/spotify/*`, `web/xeneon*.html`,
`static/js/xeneon.js`, `static/css/xeneon.css`, `static/js/overview.js` (Edge
config), `database/xeneon/*`, `static/edge-widgets/*`, the added language keys,
and the docs.

## How this branch was built
Branched from `upstream/main`; `src/systeminfo/systeminfo.go` and
`src/templates/templates.go` taken from the fork (upstream hadn't touched them),
the udev rule added, and the argv-truncation security fix applied. Builds with
`CGO_CFLAGS_ALLOW='-fno-strict-overflow' go build .`. The new systeminfo
functions have no in-tree callers here (the HTTP handlers that use them are
deferred), which is intentional for review — Go permits unused exported funcs.
