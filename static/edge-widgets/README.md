# Edge embed widgets (third-party)

These are standalone HTML/CSS/JS widgets imported from the
[stealthsrc/icue-edge-widgets](https://github.com/stealthsrc/icue-edge-widgets)
project (Apache License 2.0 — see `LICENSE`).

They were built as `.icuewidget` packages for Corsair's native iCUE software, but
each widget degrades gracefully without the iCUE runtime (config falls back to
sensible defaults; they call only public web APIs or use browser localStorage).
OpenLinkHub serves each folder as static content and embeds it via the
`xeneon-embed` widget (an `<iframe>`), registered in `database/xeneon/xeneon.json`
(ids 17–22).

| Folder | Widget | Network |
| --- | --- | --- |
| `world-clock/` | World Clock | none |
| `focus-timer/` | Focus Timer | none |
| `countdowns/` | Countdowns | none (localStorage) |
| `habit-rings/` | Habit Rings | none (localStorage) |
| `daily-brief/` | Daily Brief | Open-Meteo |
| `github-repo-monitor/` | GitHub Repo Monitor | api.github.com |

`xeneon-widget.css` is the shared stylesheet each widget references via `../`.

Per-widget config (city, repo, timezones) is normally set through iCUE's settings
UI, which is absent here — to customize, edit the defaults inside a widget's
`app.js`. Not imported: ISS Horizon (pulls Leaflet/Google Fonts from external
CDNs) and the two pump-LCD widgets (require iCUE's native sensor/media providers).
