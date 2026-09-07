# XENEON EDGE — code review results

End-to-end YAGNI / duplication / maintainability review of the authored XENEON EDGE
feature code, and the fixes applied on branch `xeneon-edge-review-fixes` (off
`xeneon-edge`). Review scope excluded upstream code, the vendored
`static/edge-widgets/` (Apache-2.0), and docs.

## Method

A multi-agent review fanned out finders per subsystem plus cross-cutting duplication
and YAGNI/dead-code specialists, deduplicated, then adversarially verified each
finding against the actual code and synthesized a ranked report.

- Raw findings: **41** → deduped **41** → after verification: **27 confirmed +
  11 plausible**, **3 rejected**.
- No high-severity defects; issues clustered around dead code, drift-prone
  duplication, and a few consistency/correctness smells.

## Net reduction

| Metric | Value |
|---|---|
| Files changed | 26 |
| Lines added | 258 |
| Lines removed | 538 |
| **Net change** | **−280 lines** |

Functionality is preserved (and slightly improved — see correctness fixes below);
the reduction comes from removing dead code and collapsing copy-paste behind shared
helpers/partials.

Notable shrinkage:
- The 5 ring widget templates + shared partial: **110 → 51 lines** (each ring
  template 22 → 6 lines).
- `static/js/xeneon.js`: −107 lines (five ring pollers → one helper).
- `src/server/requests/requests.go`: −199/+82 (three widget processors → one).
- `src/server/server.go`: −114 net (three index handlers → one factory).

### Per pass

| Pass | Area | Δ |
|---|---|---|
| 1 | Dead code + shadowing | +10 / −39 |
| 2 | Go backend dedup | +82 / −199 |
| 3 | Spotify token plumbing | +55 / −30 |
| 4 | Template dedup | +47 / −110 |
| 5 | Kiosk JS ring pollers + nvtop escaping | +34 / −107 |
| 6 | Config JS dedup + correctness | +25 / −50 |
| 7 | media sniff read + clearable city | +6 / −4 |

## Problems solved

### Dead code / YAGNI (removed)
- `systeminfo.GetStorageCount` — exported, zero callers, walked all of
  `/sys/class/hwmon` to return a length.
- `xeneonedge.getWidget` — dead (superseded by `getProfileWidget`).
- `xeneonedge.ColumnAreas` — dead exported wrapper.
- `spotify.NowPlaying.Album` — serialized but never rendered (also dropped from the
  decode struct).
- `AreaSpanChoices` — dead `max := ceil; if p.avail < max` seed (`p.avail <= ceil`
  always) simplified to `max := p.avail`.
- Unused `$root` bindings in the ring templates.

### Duplication (factored)
- **Three Xeneon widget request processors** (`ProcessXeneonWidgetArea/Span/Settings`)
  → one `processWidgetCall` (decode + device validation + dispatch + result mapping).
- **Three indexed sensor HTTP handlers** (gpu temp/load + storage) → one
  `indexedSensorHandler` factory; removed the now-unused `getGpuIndexVar`.
- **Card background/text style block** copy-pasted across 16 widget templates →
  `widget-card-style` / `widget-card-bg` partials (also fixed drift: weather had it
  twice, embed omitted it).
- **Ring-chart markup** in the 5 ring widgets → one `xeneon-ring-body` partial
  (value element standardized to `chart-value`).
- **Five ring polling blocks** in the kiosk JS → one `pollRingWidget` helper;
  removed the redundant `updateRing` + `setThermalValue` double render.
- **`Area*` lookup + layout-build prologue** across four methods → one `placement()`
  helper.
- **Spotify token response struct** + **validity/skew check** duplicated in
  `Exchange` and `bearer` → `tokenResponse` type, `tokenRefreshSkew` const,
  `tokenValidLocked` helper.
- **`widget-area-select` / `widget-span-select`** change handlers → `postWidgetChange`
  helper (and dropped a dead try/catch that guarded nothing).
- Hand-rolled `htmlEscape` → stdlib `html.EscapeString`.

### Correctness / robustness (fixed)
- **Spotify token refresh no longer performs network I/O while holding the write
  lock** — a stalled token endpoint previously blocked all Spotify operations;
  refreshes now run unlocked and are serialized by a dedicated `refreshMu`.
- **`configureWidget` modal DOM leak** — added `modal.remove()` on hide so
  `#systemModal` and its duplicate child IDs no longer accumulate on each reopen.
- **Storage temp handler** now returns an error status for an out-of-range index
  (was `Status 1` with `Data -1`).
- **CPU temp/load rings** now update on a `0` value (previous truthy check skipped
  `0%` load).
- **Weather config** now range-validates latitude `[-90,90]` / longitude
  `[-180,180]` (inputs declared bounds; JS only `isNaN`-checked).
- **nvtop** GPU name and process type are now HTML-escaped before innerHTML
  concatenation (consistency with the `.text()`-set user/command fields).
- **Media upload sniff** uses `io.ReadFull` so the 512-byte content-type buffer is
  filled even on a chunked reader.
- **Weather city is clearable** (empty allowed, 64-char cap kept) — consistent with
  Country/HeaderText/Unit.
- `getSpotifyNowPlaying` reports errors in `Message`, not `Data`.

### Consistency
- Spotify handler status strings routed through `language.GetValue` (+ 4 `txt` keys);
  Spotify config status labels routed through `i18n.t`.
- Renamed `systeminfo` locals that shadowed package globals (`gpuIndex`→`idx`,
  `info`→`byPid`/`si`).

## Consciously not changed (with reasons)
- **`application/octet-stream` in the video MIME allowlist** — kept by design;
  `http.DetectContentType` cannot classify mp4/mov/avi/mpeg containers, so the
  extension/name regex is the intended guard on this local desktop app.
- **`GetStorageTemperatureIndex` per-call hwmon rescan** — negligible in practice
  (a couple of drives polled every 2 s); a batch endpoint wasn't worth the API churn.
- **`xeneon-embed` vs `xeneon-weburl` templates** — near-identical iframes but serve
  distinct catalog roles (fixed presets vs a single user-configured URL); the shared
  card-style partial now covers the actionable overlap.
- **Media-list fetch** in `overview.js` — the two call sites diverge after fetching,
  so a shared helper would not simplify meaningfully.

## Verification
- `go build`, `go vet` (all touched packages), and `go build -race` (spotify) pass.
- Both kiosk/config JS files pass `node --check`; catalog and `en_US` JSON validate.
- All 17 widget templates were executed through `html/template` (escaping runs at
  execute time) — every one renders non-empty with styles intact and no `ZgotmplZ`
  sanitization.
