# XENEON EDGE — feature roadmap (vs iCUE)

Phased plan to close the gaps between this repo's Edge implementation and Corsair
iCUE's widget/platform feature set. Produced 2026-10-25 from three read-only scoping
passes (Interaction, Data, Platform); every current-code claim carries `file:line`
evidence. Hardening already landed on this branch: template render-error logging,
"Not placed" markers, en_US language fallback + translations, Go 1.26 PipeWire
build fix (`57ba6ecc`, `fe6d1819`).

Sources for the iCUE feature set: [XENEON EDGE Widgets Explained](https://www.corsair.com/us/en/explorer/diy-builder/accessories/xeneon-edge-widgets-explained)
(updated Apr 2026), [official quickstart](https://www.corsair.com/us/en/explorer/gamer/monitors/corsair-xeneon-edge),
[TechPowerUp review](https://www.techpowerup.com/review/corsair-xeneon-edge).

## Grounded pipeline facts (apply to every slice)

- Widget pipeline: catalog `database/xeneon/xeneon.json` → per-profile widgets
  (`src/devices/xeneonedge/xeneonedge.go:73-95`) → server-side template render into
  the kiosk page (`web/xeneon.html:26-58` via `SegmentSlots`/`AreaWidget`) → kiosk
  JS pollers gated on `$('#xeneon-<t>').length` (`static/js/xeneon.js`).
- New `web/xeneon-*.html` templates auto-register — no registration code
  (`src/templates/templates.go:100-104`).
- New catalog widgets auto-append to existing profiles via `mergeCatalogWidgets`
  (`xeneonedge.go:867-897`); no profile migration needed.
- Mutating config changes must `bumpConfigRevision()` (`xeneonedge.go:13-21`) so the
  headless kiosk self-reloads (`static/js/xeneon.js:37-57`).
- API has no auth middleware; daemon binds `127.0.0.1` by default
  (`src/config/config.go:139-141`).

## Catalog ID registry (existing 1–25)

| ID | Widget | Slice |
|----|--------|-------|
| 26 | Volume | G1 |
| 27 | Actions (Launch App) | G2 |
| 28 | Sensor Chart | G4 |
| 29 | Notifications | G5 |
| 30 | Twitch Chat | G7 |
| 31 | Twitch Live | G7 |
| 32 | Stopwatch | G12 |
| 33 | Custom widget | G10 |

## Phase summary

| Phase | Theme | Gaps | Sizes |
|-------|-------|------|-------|
| 1 | Quick wins: keyless embeds, touch parity, AQI | G6, G7 (TW1), G3 (T1+T2), G12 | S/M |
| 2 | Interactive & data widgets | G1, G4, G5, G2 | S/M |
| 3 | Platform & hardware | G8, G9, G10, G3 (T3), G11 | S/M/L |
| 4 | Optional / deferred | G7 (TW2), rejected: Decision Coin | M |

Shared plumbing first: G4 and G7 both extend the `Widget` struct +
`UpdateWidgetSettings` (`xeneonedge.go:531-663`) and the config modal
(`web/xeneonedge.html:73-88`, `static/js/overview.js`) — batch those struct/UI
extensions in one pass to avoid merge conflicts.

---

## Phase 1 — Quick wins

### G6 — Air Quality (iCUE: Air Quality) — Slice Q1 (S/M)

Weather is fetched client-side by the kiosk JS from Open-Meteo
(`static/js/xeneon.js:176-186`, 10 min refresh). AQI rides the same pattern.

- Add a parallel fetch to `https://air-quality-api.open-meteo.com/v1/air-quality`
  (`current=european_aqi,us_aqi,pm2_5`, keyless) in the weather block
  (`xeneon.js:173-330`); render AQI (European bands) + PM2.5 into two new spans in
  `web/xeneon-weather.html:20-30`.
- AC: AQI appears within one refresh cycle; failure hides the row, never breaks the
  weather card; null (coverage gap) hides, no "NaN".
- Non-goals: pollen, AQI forecast, server-side proxy.

### G7 — Twitch Chat/Live embeds (iCUE: Twitch Chat, Twitch Live) — Slice TW1 (M)

Twitch's official chat/player embeds need **no auth**, but `parent` must match the
embedding hostname — so a dedicated template beats reusing `xeneon-embed` (fixed
catalog URL).

- New `web/xeneon-twitch.html` (empty iframe + mode attr); catalog ids 30/31
  (template `xeneon-twitch`, columns `[1,3]`); add `Channel string` to `Widget`
  (`xeneonedge.go:73-95`), validate like `Url` (`xeneonedge.go:615-622`).
- Kiosk JS sets iframe `src` at runtime with `parent=window.location.hostname` so
  both `127.0.0.1:port` and LAN-IP origins work.
- AC: chat renders/updates with zero credentials; player shows a live channel;
  works on both origin forms.
- Risks: channels that disable embeds (rare); parent mismatch fails silently
  (mitigated by runtime hostname).

### G3 — Touch on the display: swipe between profiles — Slices T1 (S) + T2 (S)

The Edge is a 5-point touchscreen (USB HID `wch.cn TouchScreen`); kiosk JS currently
has click handlers only. Profiles already work as pages; only the API surface and
gestures are missing. `UserProfiles` is not exposed today (`common.Device.Instance`
is `json:"-"`, `src/common/common.go:54`).

- **T1** — `GET /api/xeneon/profiles` → `{profiles:[names], active:name}`:
  `ProfileNames()` (sorted copy — map order is nondeterministic) +
  `ActiveProfileName()` in `xeneonedge.go`; handler beside `getXeneonRevision`
  (`server.go:1318-1328`).
- **T2** — `static/js/xeneon.js` + `static/css/xeneon.css`: fetch profiles on load;
  bottom-center dot indicator (also clickable fallback, hidden when <2 profiles);
  document-level `touchstart`/`touchend` horizontal swipe (`dx > 96px`,
  `|dx| > 1.5·|dy|`, `< 600ms`, target not `button/a/input/select`/inside iframe);
  swipe → `POST /api/userProfile/change` → existing revision watcher reloads the new
  layout (`xeneon.js:37-57`). Add `touch-action: pan-y` on `.edge-shell`.
- AC: swipe on the Edge switches profile; layout reloads via revision; indicator
  reflects active profile.
- Non-goals: pinch/palm gestures, on-screen keyboard, soft transitions.

### G12 — Stopwatch (iCUE: Stopwatch) — Slice (S)

Embed-style preset, zero Go changes (pattern: `static/edge-widgets/`, catalog
`xeneon.json:17-22`).

- New `static/edge-widgets/stopwatch/` (index.html/app.js/style.css; start/stop/lap/
  reset via touch; reset-on-reload acceptable) + catalog id 32 embed entry.
- AC: renders from catalog; touch works; no outbound requests; `node --check` clean.
- **Rejected:** Decision Coin as a standalone catalog entry (novelty; doesn't clear
  the recurring-utility bar of the six existing embeds). If ever wanted, fold it
  into the stopwatch widget as a second screen.

---

## Phase 2 — Interactive & data widgets

### G1 — Volume widget (iCUE: Volume) — Slices V1 (M) → V2 (S) → V3 (S)

**Correction to earlier assessment:** `src/audio` is a virtual sink + DSP/EQ engine,
*not* a system volume controller — no sink volume/mute API exists
(`src/audio/audio.h:52-63`; `GetSinks` returns empty with the engine off,
`audio.go:158-183`). A new PipeWire binding is required. (Interim fallback:
uinput media-key routes `/api/media/volumeUp|Down|mute`, `server.go:916-951` —
no state read, no absolute level.)

- **V1** — `src/audio/volume.c` + `audio.h` additions + Go wrappers: independent
  `pw_context`+`pw_thread_loop` (must not touch the running DSP engine); registry
  listener tracks `media.class = Audio/Sink` nodes (pattern `audio.c:665-666`);
  default sink via Metadata `default.audio.sink` (fallback: first sink); read/write
  node `Props` (volume/channelVolumes/mute). Export `GetSystemVolume()`,
  `SetSystemVolume(0-1)`, `SetSystemMute(bool)`, mutex-guarded like `GetSinks`.
  PipeWire volume is linear 0..1 — map UI linearly and document the divergence from
  wpctl's cubic scale.
- **V2** — `GET /api/audio/volume`, `POST /api/audio/volume {volume:0-100}`,
  `POST /api/audio/mute {muted:bool}`; handlers following `setAudioSettings`
  (`server.go:1659-1666`), processors following `ProcessAudioOutputDeviceChange`
  (`requests.go:3477-3513`); `txtVolume*` language keys.
- **V3** — catalog 26 (template `xeneon-volume`, columns `[1,3]`, span 1);
  `web/xeneon-volume.html` (slider + mute, `widget-card-style` partial); JS: 1 s
  poll, ~300 ms debounced slider POST, mute toggle + 400 ms re-poll (mirror
  `xeneon.js:313-321`).
- AC: slider mirrors host volume (cross-check `wpctl get-volume`); works with
  virtual audio disabled; engine unaffected when enabled.
- Risks: SPA pod construction (test with `pw-dump`); slider drags may look like
  swipes — exclude `input`/`button` from gesture targets (G3 T2 covers this).
- Non-goals: per-sink chooser, per-app volumes, output switching, OSD.

### G4 — Sensor Chart (iCUE: Sensor Chart) — Slices S1 (M) → S2 (M+S)

All stats today are point-in-time (`src/stats/stats.go:32-40`); the only history
is client-side nvtop accumulation that dies on every kiosk reload
(`xeneon.js:713-750`, revision watcher `xeneon.js:31-58`).

- **S1** — new `src/history/history.go`: in-memory ring per sensor key
  `{t,v}`, capacity 900 × 2 s tick = 30 min, single tier. Sampler goroutine records
  `cpu-temp`, `cpu-load`, `gpu-<i>-temp/load`, `storage-<i>-temp`. `history.Init()`
  in `src/controller/controller.go:39-57`. Route `GET /api/xeneon/history/<sensor>`.
  Never hold the mutex across hwmon/CPU-load I/O (unlocked-I/O discipline,
  `src/spotify/spotify.go:255-300`).
- **S2** — catalog 28 (template `xeneon-chart`, columns `[1,3]`, span 1-3 — nvtop
  precedent `xeneon.json:130-136`); `Widget.Sensor`/`Widget.Window` fields
  (validate `Sensor` against sampler keys, `Window` ∈ {5,15,30} min); config modal
  sensor dropdown; JS renders polyline via the nvtop `areaPath` + DOMParser pattern
  (`xeneon.js:808-840`), 2 s poll; empty history renders placeholder.
- AC: chart survives kiosk reloads; tracks live data; window rescales server-side.
- Non-goals: persistence, chart libraries, AIO channel sensors, alerting.

### G5 — Notifications mirror (iCUE: Windows Notifications) — Slices N1 (M) → N2 (S)

`github.com/godbus/dbus/v5 v5.1.0` already in `go.mod:6`; session-bus client +
signal-subscription patterns exist (`src/media/media.go:61-72`,
`src/monitor/monitor.go:77-84`).

- **N1** — new `src/notifications/notifications.go`: session bus,
  `org.freedesktop.DBus.Monitoring.BecomeMonitor` with match
  `type='method_call',interface='org.freedesktop.Notifications',member='Notify'`
  (Notify is a method call, not a signal; `eavesdrop` was removed in dbus 1.9);
  decode the Notify tuple; ring capped at 8 items / 1 h TTL; dedupe
  (app, summary, body) within 2 s.
  **Privacy:** no persistence; body never logged; loopback-only; capture runs only
  while a Notifications widget is placed (gate on `UpdateWidgetArea` placement,
  `xeneonedge.go:468-503`) — placement is the opt-in.
- **N2** — catalog 29 (template `xeneon-notifications`, span 1-2); `web/xeneon-notifications.html`
  rolling list; 3 s poll of `GET /api/xeneon/notifications`.
- Deployment constraint (same as the MPRIS media widget): session bus exists only
  under the user-space install (`install-user-space.sh:26-42`); the system unit
  (`OpenLinkHub.service:8-9,18`) has no session — degrade with a warn log
  (`media.go:67-70` pattern).
- AC: `notify-send` appears within 1 s when widget placed; nothing captured when not;
  daemon survives bus absence.
- Non-goals: actions/inline reply, persistence, sending.

### G2 — Launch App / action buttons (iCUE: Launch App) — Slices A1 (M) → A2 (S) → A3 (S), A4 (M, optional)

Transport is solved (kiosk already POSTs to the daemon, `xeneon.js:313-321`); the
work is a conservative exec surface on an unauthenticated API.

- **A1** — `src/devices/xeneonedge/actions.go`: allowlist file
  `database/xeneon/actions.json` (precedent: `database/spotify.json`,
  `spotify.go:87-89`), schema `[{id,name,icon,command,args[]}]`. Validation before
  any exec: id matches `AlphanumericDashRegex`; `command` absolute, exists,
  executable; **no shell** — `exec.CommandContext(cmd, args...)` with argv slice;
  ≤8 args, each ≤256 chars; 10 s timeout, output discarded + logged; single-flight
  mutex; per-id 1 s debounce; save validates whole file then `bumpConfigRevision()`.
- **A2** — `POST /api/xeneon/action/<id>` (tap target), `GET/PUT /api/xeneon/actions`
  (config page); `<id>` parsed with the existing `getVarLast` regex discipline
  (`server.go:2731-2739`).
- **A3** — catalog 27 (template `xeneon-actions`); `web/xeneon-actions.html`
  renders buttons from an exported `Actions()` on `Device` (pattern:
  `SegmentSlots`, `xeneonedge.go:444-456`); tap → POST, 1 s button-disable feedback;
  empty allowlist renders nothing.
- **A4** (optional) — "Actions" editor section in `web/xeneonedge.html`; v1
  alternative is hand-editing the JSON (live via revision bump).
- AC: only allowlisted ids execute; unknown/malformed id → `Status:0`; tapping runs
  the command once.
- Non-goals: shell, pipes/redirects, env customization, client-supplied commands,
  Windows/macOS.

---

## Phase 3 — Platform & hardware

### G8 — Display control via DDC/CI (iCUE: brightness/backlight/contrast/RGB) — Slices D1 (M) → D2 (S) → D3 (S) → D4 (S)

kscreen-doctor on this Edge: `Brightness control: supported`, `DDC/CI: allowed`
(`docs/xeneon-edge-kde.md:11-37`). No DDC/i2c code exists today.

- **D1** — `src/devices/xeneonedge/ddc.go`: find the Edge bus via
  `/sys/class/drm/card*-*/ddc` symlink, matching the connector whose `modes`
  contains `2560x720` (DRM glob pattern `src/display/display.go:104-108`); DDC/CI
  framing via `I2C_RDWR` ioctl (helper beside `src/smbus/smbus.go:103-135` — same
  boring raw-i2c pattern, zero new deps; **not** shelling out to ddcutil); VCP get/
  set for 0x10 brightness, 0x12 contrast, 0x16/0x18/0x1A R/G/B gain; retries with
  backoff + write-then-read-back; per-code `available:false` degradation; ≤2 s
  timeouts; never hold a call across the HTTP request; single mutex.
- **D2** — udev: `KERNEL=="i2c-[0-9]*", SUBSYSTEM=="i2c-dev", MODE="0660",
  GROUP="openlinkhub"` in `99-openlinkhub.rules` (works in both install modes;
  `install-user-space.sh:9-10` rewrites `OWNER=` only, so write `GROUP=`).
- **D3** — persistence: `DisplayBrightness/Contrast/Red/Green/Blue` on
  `DeviceProfile` (`xeneonedge.go:31-37`), restored on `Init()`.
- **D4** — `GET/POST /api/xeneon/display` (partial update, 0-100 clamp; pattern
  `processWidgetCall`, `requests.go:2544-2596`) + "Display settings" card in
  `web/xeneonedge.html` (sidebar `settings-list` pattern, lines 29-152) with
  debounced (300 ms) sliders; disabled + explanation per unavailable code.
- AC: `curl -d '{"brightness":50}'` changes panel brightness (confirm
  `ddcutil getvcp 10` before/after); values survive restart; invalid payload writes
  nothing.
- Risks: [INFERENCE] panel may expose a subset of VCP codes (brightness confirmed;
  contrast/RGB unconfirmed) — per-code availability handles it.
- Non-goals: `/sys/class/backlight` (external panel), auto-dim, DPMS, input
  switching, non-Edge monitors.

### G9 — Auto-rotation (iCUE: accelerometer auto-rotate) — R1 (S) → conditional R2 (M)

- **R1 (do first)** — sensor discovery report appended to
  `docs/xeneon-edge-kde.md`: `ls /sys/class/iio` + per-device `name`;
  `lsmod | grep hid_sensor` + `modprobe hid-sensor-hub hid-sensor-iio-common
  hid-sensor-accel-3d` re-check; `lsusb`; `/proc/bus/input/devices`;
  `libinput list-devices`. Outcome A (IIO accel) / B (raw HID, no driver) / C
  (nothing) recorded with evidence.
- **R2 (only on outcome A)** — `src/devices/xeneonedge/orientation.go`: poll
  `in_accel_{x,y,z}_{raw,scale}` every 1-2 s, ±15° dead-zone, stable 2 s before
  switching; `GET /api/xeneon/orientation` → `{rotation: 0|90|180|270}`. Kiosk JS
  polls every 5 s; on 90/270 applies CSS `transform: rotate(90deg) translateY(-100%)`
  on `.edge-shell` (already `transform-origin: top left`, `xeneon.css:29-38`) and
  swaps grid columns/rows.
- Risks: unknown sensor mount matrix (calibrate during implementation); touch
  coords under CSS transform must be verified against the KDE touch mapping
  (`xeneon-edge-kde.md:39-52`); if wrong, rotation stays manual (documented).
- Non-goals: daemon never mutates DRM/KScreen rotation.

### G10 — User-defined custom widgets (iCUE: `.icuewidget` import) — C1 (L) → C2 (M) → C3/C4 (S/M) → C5 (S)

Minimal boring version of iCUE's import: uploaded HTML/zip + sandboxed iframe +
documented read-only data API.

- **C1** — `src/devices/xeneonedge/widgets.go` (mirrors `media.go`):
  `POST /api/xeneon/widgets/upload` — multipart `widgetArchive`, `MaxBytesReader`
  8 MB; `.zip` (stdlib `archive/zip`) or single `.html`; slug
  `^[a-zA-Z0-9_-]+$` (`media.go:27`); zip-slip guard (reject `..`, absolute paths,
  symlinks; `filepath.Clean` containment) + 32 MB decompressed cap; ext allowlist
  `.html .css .js .json .png .jpg .jpeg .gif .svg .webp .woff2` (map pattern
  `media.go:29-49`); store `ConfigPath/database/xeneon/widgets/<slug>/`, require
  root `index.html`; `bumpConfigRevision()` on change.
- **C2** — `GET /api/xeneon/widgets` (list), `GET /api/xeneon/widgets/<slug>/`
  (path-contained subpath serving, extends `PerformMediaServe` `media.go:94-102`),
  `DELETE /api/xeneon/widgets` (refuses when a profile widget references the slug);
  `Cache-Control: no-cache` like static (`server.go:2788-2794`).
- **C3** — catalog 33 "Custom widget" (template `xeneon-custom` — a **new** template
  name on purpose: `mergeCatalogWidgets` force-overwrites URLs only for
  `xeneon-embed`, `xeneonedge.go:883-887`, so the user's pick persists);
  `web/xeneon-custom.html` = `xeneon-embed.html` + `sandbox="allow-scripts"`.
- **C4** — settings: accept URL prefix `/api/xeneon/widgets/` only when template is
  `xeneon-custom` (`UpdateWidgetSettings`, `xeneonedge.go:531-619`); config modal
  swaps the URL field for a dropdown fed by `GET /api/xeneon/widgets`
  (`overview.js:1823-2147`).
- **C5** — CORS middleware (`Access-Control-Allow-Origin: *` + preflight) applied
  **only** to whitelisted read-only GETs: cpu/gpu temp+load, gpuStats, storageTemp,
  batteryStats, media/playback, spotify/nowPlaying, xeneon/media, xeneon/revision,
  `/api/language` (`server.go:2798-2856`). All mutating endpoints stay CORS-free.
  Accepted risk: widens local read access — data is non-sensitive system stats,
  daemon binds loopback; `/api/spotify/nowPlaying` returns playback info only,
  never tokens (`server.go:1038-1042` — re-verify before shipping).
- AC: valid zip serves; zip-slip/oversize/missing-index → 400; traversal → 404;
  widget renders in sandboxed iframe after revision reload; user widget JS errors
  cannot break the kiosk.
- Non-goals: manifest parsing, marketplace, server-side exec of widget code, auth.

### G3 (part) — cage kiosk runtime — T3 (S, docs+script only)

- `docs/xeneon-edge-cage.md` + `scripts/start-edge-kiosk.sh`: systemd **user** unit
  running `cage -d -- <browser> --kiosk http://127.0.0.1:<port>/xeneon` +
  XDG-autostart alternative; DDC brightness under cage via the G8 module or a
  `ddcutil` helper; cage maps touch to its output automatically (no
  `kcminputrc OutputUuid` dance, `xeneon-edge-kde.md:27-45`).
- **Tradeoff, stated:** *KDE* (current) = Edge is one output of the real desktop;
  KDE owns brightness/rotation; heavier. *cage* = minimal single-app kiosk;
  automatic touch mapping; but brightness/rotation then belong to this daemon
  (G8) and the desktop session is gone on that output. Recommendation: stay on KDE
  until G8 lands; then cage becomes a clean deployment option for dedicated-box
  setups.

### G11 — Widget data API docs — DOC1 (S)

- `docs/xeneon-edge-data-api.md`: response envelope (`Response.Send`,
  `server.go:67`), CORS-whitelisted endpoint table (from C5) with examples,
  sandbox rules, custom-widget upload flow + minimal example widget; keep the
  table greppable against `server.go` route lines to resist drift.
- Lands after C5.

---

## Phase 4 — Optional / deferred

- **G7 TW2** — Twitch Helix live-status badge (M): client-credentials app-access
  token, Spotify pattern minus user auth (`src/spotify/spotify.go:47-53,255-300`);
  `/api/twitch/*` + config block mirroring `web/xeneonedge.html:113-141`; "LIVE /
  viewers" overlay. Optional: requires Twitch app registration (user friction).
- **Rejected:** Decision Coin standalone (see G12).

## Sequencing

1. Shared `Widget`-struct/config-modal pass (G4 S2 fields + G7 Channel) → then
   Phase 1 slices are fully independent.
2. Phase 2 slices are mutually independent; V1 is the long pole (C binding).
3. Phase 3: D1→D2→D3→D4; R1 gate before R2; C1→C2→C3/C4→C5→DOC1; T3 anytime after
   D1 exists (else cage users have no brightness path).
4. Every mutating task bumps `configRevision` so the headless kiosk self-reloads.
