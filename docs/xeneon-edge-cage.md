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

`scripts/start-edge-kiosk.sh` waits for HTTP success from `/xeneon` and execs
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

## Dedicated NVIDIA launcher and boot permissions

For the TTY-based dual-GPU setup, install the tracked launcher:

```sh
install -m 0755 scripts/xeneon-cage.sh "$HOME/xeneon-cage.sh"
```

It selects the DRM card by the connected XENEON EDID and retains software Chrome
rendering. DRM card numbers can change after a reboot. Check the selected
connector with `wlr-randr`; `card2` alone is not an error.

Both launchers now require a successful `/xeneon` response before opening the
browser. The revision API can return 200 while the Edge is unavailable. Requests
have a two-second connect timeout and a three-second total timeout; exhausting
the readiness attempts exits with an error instead of opening a failed page.

A white page containing “Xeneon Edge is unavailable” means the daemon could not
register the device. Check `stdout.log` for HID permission failures and inspect
the device's udev rules. On this host, early boot could not resolve the
`openlinkhub` group, and the root-filesystem rules named a nonexistent
`openlinkhub` owner. The Edge and HX1500i nodes became `root:root 0600`.

For a user service, grant the existing `openlinkhub` group read/write access to
the required HID devices. The local repair uses two product-specific rules
(Corsair `1b1c:1d0d` and `1b1c:1c1f`), owner 0, the host's numeric group ID,
and mode 0660. Numeric IDs avoid name resolution during initramfs startup.
Determine the group ID with `getent group openlinkhub`; this host uses 973.
Install the override after the package rules, reload udev, trigger only the
affected HID devices, and update the current kernel's initramfs so the override
is present at the next boot. Restart the user service and reload the kiosk only
after the device nodes are accessible.

Launcher regression checks:

```sh
python3 scripts/test-start-edge-kiosk.py
shellcheck scripts/start-edge-kiosk.sh scripts/xeneon-cage.sh
```
