#!/usr/bin/env python3
"""Exercise kiosk startup with mocked curl, browser, cage and sleep commands."""

from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("start-edge-kiosk.sh").resolve()


class KioskStartupTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.env = {
            "PATH": str(self.root),
            "HOME": str(self.root),
            "EDGE_BROWSER": "chromium",
            "CURL_COUNT_FILE": str(self.root / "attempts"),
            "CURL_ARGS_FILE": str(self.root / "curl-args"),
            "CAGE_ARGS_FILE": str(self.root / "cage-args"),
        }
        self.command("chromium", "exit 0")
        self.command("sleep", "exit 0")
        self.command("cage", 'printf "%s\\n" "$@" > "$CAGE_ARGS_FILE"')

    def command(self, name, body):
        path = self.root / name
        path.write_text("#!/bin/sh\n" + body + "\n")
        path.chmod(0o755)

    def curl(self, successful_attempt):
        self.env["CURL_SUCCESSFUL_ATTEMPT"] = str(successful_attempt)
        self.command("curl", '''count=0
if [ -f "$CURL_COUNT_FILE" ]; then read -r count < "$CURL_COUNT_FILE"; fi
count=$((count + 1))
printf '%s\\n' "$count" > "$CURL_COUNT_FILE"
printf '%s\\n' "$@" > "$CURL_ARGS_FILE"
[ "$count" -ge "$CURL_SUCCESSFUL_ATTEMPT" ] || exit 22
''')

    def launch(self, script=SCRIPT):
        return subprocess.run(
            ["/bin/sh", str(script)], env=self.env,
            capture_output=True, text=True, timeout=5,
        )

    def test_failed_page_then_ready_launches(self):
        self.curl(successful_attempt=2)
        result = self.launch()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.root / "attempts").read_text(), "2\n")
        self.assertEqual((self.root / "curl-args").read_text().splitlines(), [
            "--connect-timeout", "2", "--max-time", "3", "-fsS", "-o",
            "/dev/null", "http://127.0.0.1:27003/xeneon",
        ])
        self.assertEqual((self.root / "cage-args").read_text().splitlines(), [
            "-d", "--", "chromium", "--kiosk", "http://127.0.0.1:27003/xeneon",
        ])

    def test_failed_page_exhausts_attempts_without_launching(self):
        self.curl(successful_attempt=31)
        result = self.launch()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((self.root / "attempts").read_text(), "30\n")
        self.assertIn("unavailable after 30 attempts", result.stderr)
        self.assertFalse((self.root / "cage-args").exists())

    def test_missing_curl_does_not_launch(self):
        result = self.launch()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("curl is required", result.stderr)
        self.assertFalse((self.root / "cage-args").exists())


    def dedicated_script(self):
        # Substitute only the fixture sysfs root in an isolated script copy.
        drm = self.root / "drm"
        for connector, model in (("card2-DP-1", "LG TV"),
                                 ("card7-DP-3", "XENEON EDGE")):
            path = drm / connector
            path.mkdir(parents=True)
            (path / "status").write_text("connected\n")
            (path / "edid").write_bytes(b"\x00" + model.encode() + b"\x00")
        for command in ("cat", "grep"):
            (self.root / command).symlink_to(Path("/bin") / command)
        source = SCRIPT.with_name("xeneon-cage.sh").read_text()
        script = self.root / "xeneon-cage.sh"
        script.write_text(source.replace("/sys/class/drm", str(drm)))
        return script

    def test_dedicated_failed_page_never_launches(self):
        script = self.dedicated_script()
        self.curl(successful_attempt=61)
        result = self.launch(script)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((self.root / "attempts").read_text(), "60\n")
        self.assertIn("dashboard at http://127.0.0.1:27003/xeneon is unavailable",
                      result.stderr)
        self.assertFalse((self.root / "cage-args").exists())

    def test_dedicated_edid_follows_card_renumbering(self):
        script = self.dedicated_script()
        self.curl(successful_attempt=2)
        self.env["CAGE_DRM_FILE"] = str(self.root / "cage-drm")
        self.command("cage", '''printf '%s\\n' "$@" > "$CAGE_ARGS_FILE"
printf '%s\\n' "$WLR_DRM_DEVICES" > "$CAGE_DRM_FILE"
kill -TERM "$PPID"
''')
        result = self.launch(script)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.root / "attempts").read_text(), "2\n")
        self.assertEqual((self.root / "cage-drm").read_text(), "/dev/dri/card7\n")
        self.assertEqual((self.root / "cage-args").read_text().splitlines(), [
            "-s", "-d", "-m", "last", "--", "google-chrome",
            "--ozone-platform=wayland", "--disable-gpu",
            "--disable-features=Vulkan", "--disable-background-networking",
            "--password-store=basic", "--app=http://127.0.0.1:27003/xeneon",
            f"--user-data-dir={self.root}/.config/xeneon-kiosk",
        ])

if __name__ == "__main__":
    unittest.main()
