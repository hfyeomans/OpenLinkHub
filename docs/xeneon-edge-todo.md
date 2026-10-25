# XENEON EDGE — task backlog

Living checklist for Edge dashboard work (this session and beyond). One thing at
a time; newest context at the bottom of each item.

## In progress
- (none)

## Backlog (not started)
- [ ] (optional, deferred) PSU software temp fan curve for HX1500i — add an
      "Auto (temp)" fan mode in `src/devices/psuhid` that polls a temp sensor and
      pushes manual fan speed via `dataSetFanSpeed` (mirrors iCUE's Temp Curve).
      Today the PSU only supports Default (firmware curve) or fixed 40-100 %; the
      `/temperature` profiles do NOT drive the PSU. User chose to keep Default for now.

## Done (this session)
- [x] Fix side-column span rendering (Weather grow-up, GPU Monitor data-span)
- [x] `RenderTemplate` logs template execution errors (previously silently rendered empty widget slots)
- [x] Language values fall back to en_US for untranslated keys (`GetValue` + `/api/language` via `GetLanguageForRender`); all missing keys translated for de/fr/hr/pt-BR/ru/sv
- [x] Phase 1 roadmap slices implemented (roadmap `docs/xeneon-edge-roadmap.md`): profiles endpoint + touchscreen swipe between profiles with page dots, Twitch chat/live embed widgets (catalog 30/31), air-quality badge in the weather card, Stopwatch embed widget (catalog 32). Reviewed by DeepSeek V4.1-Flash; XSS/dropped-fail/stale-touch/NaN findings fixed.
- [x] Local Go builds: `CGO_CFLAGS_ALLOW=-fno-strict-overflow` needed with Go >= 1.26 + system PipeWire (applied via `go env -w`; Dockerfile and CI already carried it)
- [x] Config page shows a "Not placed" marker for catalog widgets assigned to no area (new catalog widgets used to appear invisibly)
- [x] Add 6 embeddable third-party edge widgets (World Clock, Focus Timer,
      Countdowns, Habit Rings, Daily Brief, GitHub Repo Monitor)
- [x] Spotify Web API now-playing widget (connected & verified live)
- [x] Multiple display profiles verified (Main / Media switch cleanly)
- [x] Per-drive storage temperature ring gauges (Storage 1/2 Temp, catalog ids 24/25;
      `/api/storageTemp/clean/<i>`; verified live 35 °C / 34 °C)
- [x] Pushed all commits to fork (origin/xeneon-edge)
- [x] Embed defaults: Daily Brief city -> Boston, GitHub Repo Monitor -> hfyeomans/OpenLinkHub
- [x] Deleted stale profile `xeneon` (`default` is protected/required by the daemon)
- [x] Recorded Spotify + kiosk-restart + profile facts in deployment memory
