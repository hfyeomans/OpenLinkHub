#!/bin/sh
# Start the XENEON EDGE kiosk under cage (Wayland kiosk compositor).
# See docs/xeneon-edge-cage.md. Exits when the session ends.

PORT="${EDGE_PORT:-27003}"
URL="http://127.0.0.1:${PORT}/xeneon"

# Wait for the actual kiosk page; the revision API succeeds without an Edge.
if ! command -v curl >/dev/null 2>&1; then
    echo "start-edge-kiosk: curl is required to check $URL" >&2
    exit 1
fi

i=0
while ! curl --connect-timeout 2 --max-time 3 -fsS -o /dev/null "$URL" 2>/dev/null; do
    i=$((i + 1))
    if [ "$i" -ge 30 ]; then
        echo "start-edge-kiosk: $URL unavailable after 30 attempts; browser not started" >&2
        exit 1
    fi
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
