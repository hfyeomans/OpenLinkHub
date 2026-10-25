# XENEON EDGE — widget data API

Read-only HTTP API for user-defined custom widgets. Endpoints are served by the
OpenLinkHub daemon (`src/server/server.go`); keep the endpoint table below
greppable against the `handleFunc` lines there.

## Response envelope

Every endpoint returns the same JSON shape (`Response.Send`, server.go:70):

```json
{ "code": 200, "status": 1, "message": "", "data": { ... } }
```

`status: 1` = success, `0` = failure (`message` holds the reason).

## CORS

`Access-Control-Allow-Origin: *` (+ preflight) is applied **only** to the
whitelisted read-only GETs below. Mutating endpoints stay CORS-free. Widgets
run in sandboxed iframes (`sandbox="allow-scripts"`, no `allow-same-origin`),
so they fetch these endpoints as cross-origin requests.

## Whitelisted endpoints

| Endpoint | Data |
|---|---|
| `GET /api/cpuTemp` | CPU temperature |
| `GET /api/cpuTemp/clean` | CPU temperature (no unit suffix) |
| `GET /api/cpuLoad` | CPU utilization % |
| `GET /api/gpuTemp`, `GET /api/gpuTemp/clean/<i>` | GPU temperature |
| `GET /api/gpuLoad`, `GET /api/gpuLoad/<i>` | GPU utilization % |
| `GET /api/gpuStats` | Per-GPU stats |
| `GET /api/storageTemp`, `GET /api/storageTemp/clean/<i>` | Storage temperatures |
| `GET /api/batteryStats` | Battery stats |
| `GET /api/media/playback` | Media player state |
| `GET /api/spotify/nowPlaying` | Spotify playback (no tokens) |
| `GET /api/xeneon/media` | Media library listing |
| `GET /api/xeneon/revision` | Config revision (kiosk reload signal) |
| `GET /api/language` | Language strings |

`GET /api/xeneon/history/{sensor}?window=N` (sensor chart samples) and
`GET /api/xeneon/notifications` are served but **not** CORS-whitelisted; add
them to the whitelist in `server.go` only if a custom widget genuinely needs
them.

## Custom widgets

Upload flow (`POST /api/xeneon/widgets/upload`, multipart field
`widgetArchive`):

- `.zip` (max 8 MB upload / 32 MB decompressed) or a single `.html` file
- slug: `^[a-zA-Z0-9_-]+$`; requires root `index.html`
- allowed extensions: `.html .css .js .json .png .jpg .jpeg .gif .svg .webp .woff2`
- served at `/api/xeneon/widgets/<slug>/<path>` with `Cache-Control: no-cache`
- deletion (`DELETE /api/xeneon/widgets`) is refused while a placed widget
  references the slug

Then add the "Custom widget" (catalog 33) to a display area and pick the slug
in its config. The widget renders in a sandboxed iframe — its JS errors cannot
affect the kiosk page.

## Minimal example widget

`index.html`:

```html
<!doctype html>
<html>
<head>
<style>
  body { background: transparent; color: #fff; font: 12px sans-serif;
         display: flex; align-items: center; justify-content: center; }
</style>
</head>
<body>
<div id="v">-- °C</div>
<script>
  const load = () =>
    fetch('http://127.0.0.1:27003/api/cpuTemp')
      .then(r => r.json())
      .then(d => { document.getElementById('v').textContent =
        (d.data ?? '--') + ' °C'; });
  load();
  setInterval(load, 2000);
</script>
</body>
</html>
```

Zip it (`zip cpu-widget.zip index.html`) and upload on the Edge config page.

## Sandbox rules

- No `allow-same-origin`: the iframe is opaque-origin and cannot read kiosk
  cookies/storage.
- Mutating (non-GET/HEAD) requests require a same-origin `Origin` header
  (`originHandler` in `server.go`); a sandboxed widget's cross-origin simple
  requests are rejected with 403. Non-browser clients send no `Origin` and
  pass.
- Scripts allowed; network access only via `fetch` to the whitelisted GETs.
- The daemon listens on all interfaces (`listenPort` in `config.json`); LAN
  exposure widens read access to the endpoints above — non-sensitive system
