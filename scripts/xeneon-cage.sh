#!/bin/sh
# Minimal Xeneon Edge kiosk without a desktop.
# cage (single-app Wayland compositor) drives the NVIDIA output the Edge is on
# (a 2560x720 panel). Chrome renders in SOFTWARE (--disable-gpu) so the dashboard
# page uses no NVIDIA VRAM — only cage's scanout buffers do (~tens of MB), leaving
# the GPUs to vLLM. Run this from a logged-in TTY (Ctrl+Alt+F3), not from a desktop.

# Point wlroots at the DRM node that actually drives the Edge.
#
# DRM card numbers are NOT stable. A kernel update reshuffled them so card2 became
# the ASPEED BMC (driver "ast", no GL driver); with the old hardcoded card2, cage
# died with "Software rendering detected ... Failed to initialize EGL". Resolve the
# node by EDID instead — the Edge's EDID contains "XENEON" — which survives both
# renumbering and re-cabling to another port or GPU.
find_edge_node() {
  for edid in /sys/class/drm/card*-*/edid; do
    [ -r "$edid" ] || continue
    con=${edid%/edid}
    [ "$(cat "$con/status" 2>/dev/null)" = "connected" ] || continue
    grep -qa XENEON "$edid" 2>/dev/null || continue
    con=${con##*/}                      # e.g. card1-DP-2
    printf '/dev/dri/%s\n' "${con%%-*}" # -> /dev/dri/card1
    return 0
  done
  return 1
}

# Fallback only: first NVIDIA card. Never the ASPEED BMC, which cannot render.
first_nvidia_node() {
  for d in /sys/class/drm/card[0-9]*; do
    case "${d##*/}" in *-*) continue ;; esac
    [ "$(basename "$(readlink -f "$d/device/driver" 2>/dev/null)" 2>/dev/null)" = "nvidia" ] || continue
    printf '/dev/dri/%s\n' "${d##*/}"
    return 0
  done
  return 1
}

# The Edge can take a few seconds to appear after a cold boot.
i=0
while ! DRM_NODE=$(find_edge_node); do
  i=$((i + 1))
  if [ "$i" -ge 30 ]; then
    DRM_NODE=$(first_nvidia_node) || { echo "xeneon-cage: no NVIDIA DRM node found" >&2; exit 1; }
    echo "xeneon-cage: XENEON EDGE not detected; falling back to $DRM_NODE" >&2
    break
  fi
  sleep 1
done
export WLR_DRM_DEVICES="$DRM_NODE"
echo "xeneon-cage: driving the Edge on $WLR_DRM_DEVICES"

# NVIDIA + wlroots needs GBM; harmless if already default on recent drivers.
export WLR_NO_HARDWARE_CURSORS=1
export GBM_BACKEND=nvidia-drm
export __GLX_VENDOR_LIBRARY_NAME=nvidia

# Wait for the OpenLinkHub daemon to be serving before launching the browser, so
# on boot Chrome never loads a dead page (it won't auto-recover from that).
command -v curl >/dev/null 2>&1 || { echo "xeneon-cage: curl is required to check dashboard readiness" >&2; exit 1; }
i=0
while ! curl --connect-timeout 2 --max-time 3 -fsS -o /dev/null http://127.0.0.1:27003/xeneon 2>/dev/null; do
  i=$((i + 1))
  if [ "$i" -ge 60 ]; then
    echo "xeneon-cage: dashboard at http://127.0.0.1:27003/xeneon is unavailable; inspect OpenLinkHub.service and USB device permissions" >&2
    exit 1
  fi
  sleep 1
done

# Self-healing loop: if cage/Chrome exits (crash, closed window), relaunch after a
# short delay so the unattended Edge kiosk always comes back. Ctrl+C / SIGTERM
# exits cleanly (for manual runs and clean shutdown).
trap 'exit 0' INT TERM

# -s: allow VT switching (Ctrl+Alt+F-key out)  -d: no window decorations
while true; do
  cage -s -d -m last -- google-chrome \
    --ozone-platform=wayland \
    --disable-gpu \
    --disable-features=Vulkan \
    --disable-background-networking \
    --password-store=basic \
    --app=http://127.0.0.1:27003/xeneon \
    --user-data-dir="$HOME/.config/xeneon-kiosk"
  sleep 3
done
