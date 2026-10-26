# XENEON EDGE — cage kiosk runtime

Dedicated-box kiosk runtime for the Edge using [cage](https://github.com/cage-kiosk/cage)
(Wayland kiosk compositor). This is the deployment option for a machine whose
only purpose is the Edge display; it replaces the "Edge as one output of the
KDE desktop" setup described in `xeneon-edge-kde.md`.

## Tradeoff, stated

| | KDE (current) | cage |
|---|---|---|
| Edge as | one output of the real desktop | the whole session |
| Touch mapping | needs `kcminputrc OutputUuid` (`xeneon-edge-kde.md:27-45`) | automatic (cage maps touch to its single output) |
| Brightness/rotation | KDE owns both | this daemon owns brightness (G8 DDC/CI module); rotation stays manual |
| Footprint | full desktop session | one compositor + one browser |

Recommendation: stay on KDE until the G8 DDC/CI module is deployed (it provides
`POST /api/xeneon/display` brightness/contrast control); then cage becomes a
clean option for dedicated-box setups. Under KDE, keep using KDE's brightness
applet — do not fight it with the API.

## Prerequisites

- cage installed (`pacman -S cage` / `apt install cage`)
- A Wayland-native browser: `chromium`, `google-chrome-stable`, or `firefox`
- OpenLinkHub daemon running (default listen port 27003, `config.json:listenPort`)
- The Edge kiosk page reachable at `http://127.0.0.1:<port>/xeneon`

## Script

`scripts/start-edge-kiosk.sh` waits for the daemon port and execs
`cage -d -- <browser> --kiosk <url>`. Browser selection order: chromium →
google-chrome-stable → firefox. Override with `EDGE_BROWSER=/path/to/browser`.

```sh
./scripts/start-edge-kiosk.sh
```

## systemd user unit

Save as `~/.config/systemd/user/openlinkhub-edge.service`:

```ini
[Unit]
Description=XENEON EDGE kiosk (cage)
After=graphical-session.target openlinkhub.service
Wants=openlinkhub.service

[Service]
ExecStart=%h/dev/src/OpenLinkHub/scripts/start-edge-kiosk.sh
Restart=on-failure
RestartSec=5

[Install]
WantedBy=graphical-session.target
```

```sh
systemctl --user daemon-reload
systemctl --user enable --now openlinkhub-edge.service
journalctl --user -u openlinkhub-edge.service -f
```

Run the unit inside an existing graphical session (`graphical-session.target`)
or from a TTY via `cage` itself — cage is the compositor, so it does not need
a running desktop, but it does need DRM/master access (logind seat, standard
for user sessions).

## XDG autostart alternative

`~/.config/autostart/openlinkhub-edge.desktop`:

```ini
[Desktop Entry]
Type=Application
Name=XENEON EDGE kiosk
Exec=/home/hank/dev/src/OpenLinkHub/scripts/start-edge-kiosk.sh
X-GNOME-Autostart-enabled=true
```

This starts cage after the desktop session appears — useful when you still
want a desktop on other outputs and the Edge kiosk alongside it.

## Brightness under cage

- Preferred: the daemon's DDC/CI module (`POST /api/xeneon/display`,
  G8/D4) — the config page "Display settings" card drives it.
- Manual fallback: `ddcutil setvcp 10 70` (uses the same i2c bus the G8
  module discovered; see `docs/xeneon-edge-kde.md` for bus identification).

## Touch input

cage assigns all touch devices to its single output automatically — the
`OutputUuid` mapping dance from the KDE setup is not needed. If touch feels
offset after rotation, verify the browser receives the transformed pointer
coordinates (the kiosk applies CSS rotation itself; see the orientation notes
in `xeneon-edge-kde.md`).
