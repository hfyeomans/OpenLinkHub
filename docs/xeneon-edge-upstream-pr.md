# Xeneon Edge — Upstream PR Prep

Planning notes for eventually contributing the fork's Xeneon Edge work back to
`jurkovic-nikola/OpenLinkHub`. Not part of the feature itself — drop this file
from any actual PR branch.

## TL;DR

- Our fork's `xeneon-edge` branch and upstream both branched from `230c33ab`
  ("harpoon v2") and **diverged independently**.
- Upstream has since done its **own** Xeneon Edge work (still gated) and is
  **20 commits ahead**. A PR cannot be a clean diff of our branch — it must be
  **rebased/reconciled onto upstream's current Xeneon code first**.
- The biggest reconciliation point: **we removed the `WidgetArea.Widget`
  snapshot (normalized the model); upstream kept it.** These conflict directly.
- Before any PR: apply the **argv truncation** (see below) and get maintainer
  buy-in on **enabling the device** (upstream keeps `7437` commented out).

## Divergence snapshot (as of upstream fetch)

| | Our fork (`xeneon-edge`) | Upstream (`main`) |
|---|---|---|
| Device entry `7437` | **enabled** (uncommented) | still **commented out** (gated) |
| `src/devices/xeneonedge/xeneonedge.go` | ~774 lines | ~457 lines |
| `WidgetArea` model | normalized (`WidgetId` + `Span`, resolve at render) | **snapshot** (`Widget *Widget`) |
| `database/xeneon/xeneon.json` | 16 widgets | 9 widgets |
| `media.go` (media library) | present | absent |
| Multi-GPU / PSU / GPU Monitor (nvtop) widgets | present | absent |
| Own recent work | — | "xeneon date / time options", "xeneon changes" |

Upstream's other 20 commits are unrelated device fixes (k70pro KVM crashfix,
k70 core, lt100 hardware lights, sabre v2, nightsword dpi, macro/restore
fixups). None touch our hardware (HX1500i + Xeneon Edge), so they do **not**
need to land in the fork for daily use — they only matter for a clean PR base.

## What a PR needs (checklist)

1. **Rebase onto upstream/main.** Expect conflicts in every shared file:
   `xeneonedge.go`, `xeneon.json`, `devices.go`, `server.go`, `requests.go`,
   `systeminfo.go`, `templates.go`, `static/css/xeneon.css`, `web/xeneon*.html`.
2. **Reconcile the widget-area model.** Decide with the maintainer whether to
   keep upstream's snapshot or adopt our normalized `WidgetId`+`Span` +
   render-time resolver (`AreaWidget`/`SegmentSlots`). Our review found the
   snapshot forces manual re-sync; ours removes it — but upstream owns the
   direction. This is the single largest merge decision.
3. **Device-enable decision.** Upstream deliberately keeps `7437` gated. A PR
   that flips it on (and ships the udev rule for `0x1d0d`) needs explicit
   maintainer agreement, or should keep it gated behind a flag.
4. **Apply the argv truncation** (deferred security item — see snippet).
5. **Split the PR.** It's large (~2,600 LOC across the whole feature). Consider
   landing in stages the maintainer can review: (a) enable + widget config +
   save API, (b) multi-GPU + PSU, (c) media widgets + span, (d) GPU Monitor.

## Deferred security fix — argv truncation

Confirmed by the code review: `/api/gpuExtended` serves every GPU process's full
command line + owner, unauthenticated on the LAN. Command lines can carry
secrets (`--password=`, tokens). For an upstream PR, truncate to the executable
basename in `src/systeminfo/systeminfo.go` (`enrichHostProcessInfo`):

```go
// current: full argv
command: strings.Join(f[4:], " "),

// PR-safe: executable basename only (drop arguments)
command: filepath.Base(f[4]),
```

`filepath` is already imported. If the maintainer wants the full command kept
(nvtop parity), gate it behind a config option that defaults to basename.

## Feature summary (for the PR description)

Enables the CORSAIR Xeneon Edge (productId `7437`/`0x1d0d`) as a device whose
screen is a web kiosk page (`/xeneon`) served by the daemon. Widgets:

- Clock, Weather (configurable location), Calendar, Media Player, Battery, PSU
- CPU/GPU temp + usage ring gauges, **multi-GPU** (per-index endpoints)
- **Media** widgets (Image/Video, Slideshow, Web URL) + a media library
  (upload/list/serve/delete, MIME-sniffed, path-traversal guarded)
- **GPU Monitor** (nvtop-style): per-GPU telemetry, history graphs, process
  table; content scales with the widget's span (1×/2×/3×)
- Per-widget custom style (background/text color); spannable side-column widgets

Supporting: per-process config save API, catalog→profile migration, static
asset cache-busting, responsive kiosk CSS.

## Key new surfaces (for reviewers)

- `src/devices/xeneonedge/xeneonedge.go` — device + widget model, validation,
  `UpdateWidgetArea`/`UpdateWidgetSettings`/`UpdateWidgetSpan`, `SegmentSlots`.
- `src/devices/xeneonedge/media.go` — media library (mirrors `lcd.go` upload).
- `src/systeminfo/systeminfo.go` — `GetGpuStats`/`GetGpuThroughput`/
  `GetGpuProcesses` (nvidia-smi / dmon / pmon / ps), NVIDIA-only, `isNvidiaSmiFound`.
- `src/server/server.go` — `/api/xeneon/*`, `/api/gpu{Stats,Extended}`,
  static cache-buster (`assetVer`).
- `web/xeneon*.html`, `static/js/xeneon.js`, `static/css/xeneon.css`.

## Build / test

```
cd <repo> && CGO_CFLAGS_ALLOW='-fno-strict-overflow' go build .
```

No test suite in the repo. Verify by running the daemon and loading `/xeneon`.
Non-NVIDIA / no-Xeneon systems degrade cleanly (empty/`—`, no crash).

## Known follow-ups

- argv truncation (above)
- per-process GPU% shows `—` on drivers where `nvidia-smi pmon` reports `-`
  (needs NVML directly; out of scope for a CLI-only PR)
- AMD support for the GPU Monitor (currently NVIDIA-only)
