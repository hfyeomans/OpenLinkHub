#!/bin/sh
# Start the XENEON EDGE kiosk under cage (Wayland kiosk compositor).
# See docs/xeneon-edge-cage.md. Exits when the session ends.

PORT="${EDGE_PORT:-27003}"
URL="http://127.0.0.1:${PORT}/xeneon"

# Wait for the daemon (max ~30 s) so the first paint is not an error page.
i=0
while [ "$i" -lt 30 ] && command -v curl >/dev/null 2>&1; do
    if curl -fsS -o /dev/null "http://127.0.0.1:${PORT}/api/xeneon/revision" 2>/dev/null; then
        break
    fi
    i=$((i + 1))
    sleep 1
done

if [ -n "$EDGE_BROWSER" ]; then
    BROWSER="$EDGE_BROWSER"
else
    for candidate in chromium chromium-browser google-chrome-stable firefox; do
        if command -v "$candidate" >/dev/null 2>&1; then
            BROWSER="$candidate"
            break
        fi
    done
fi

if [ -z "$BROWSER" ] || ! command -v "$BROWSER" >/dev/null 2>&1; then
    echo "start-edge-kiosk: no Wayland-native browser found (set EDGE_BROWSER)" >&2
    exit 1
fi

exec cage -d -- "$BROWSER" --kiosk "$URL"
