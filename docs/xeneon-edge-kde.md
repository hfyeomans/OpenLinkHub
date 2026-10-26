## XENEON EDGE KDE

- If you don't want to use terminal, open KDE Settings, search for Touchscreen and change Target display from Automatic to XENEON EDGE. Click Apply.

- From terminal:

Run `kscreen-doctor -o` to get output UUID of the display.
```
Output: 3 HDMI-A-4 ed36747c-ceb1-402c-af08-f963d94bec99
        enabled
        connected
        priority 3
        HDMI
        replication source:0
        Modes:  57:2560x720@60.27*!  58:1920x1080@59.94  59:1920x1080@50.00  60:1280x1024@75.03  61:1280x720@60.00  62:1280x720@59.94  63:1280x720@50.00  64:1024x768@60.00  65:720x576@50.00  66:720x480@59.94  67:640x480@59.94  68:640x480@59.93 
        Custom modes: None
        Geometry: 5120,0 2560x720
        Scale: 1
        Rotation: 1
        Overscan: 0
        Vrr: incapable
        RgbRange: unknown
        HDR: incapable
        Wide Color Gamut: incapable
        ICC profile: none
        Color profile source: sRGB
        Color power preference: prefer efficiency and performance
        Brightness control: supported, set to 95% and dimming to 100%
        DDC/CI: allowed
        Color resolution: unknown
        Allow EDR: unsupported
        Sharpness control: unsupported
        Automatic brightness: unsupported
```

Configure touch input to the actual display
```
kwriteconfig6 \
  --file kcminputrc \
  --group Libinput \
  --group 10176 \
  --group 2137 \
  --group "wch.cn TouchScreen" \
  --key Enabled true

kwriteconfig6 \
  --file kcminputrc \
  --group Libinput \
  --group 10176 \
  --group 2137 \
  --group "wch.cn TouchScreen" \
  --key OutputUuid ed36747c-ceb1-402c-af08-f963d94bec99
```

## Sensor discovery (roadmap G9/R1, 2026-10-25)

Goal: find out whether the Edge exposes an accelerometer to this host (IIO, HID
sensor, or raw input), to decide if G9/R2 auto-rotation is implementable.

**Outcome: C — nothing.** No accelerometer of any kind is exposed to the host:
no IIO bus, no HID Sensor usage page on any connected HID device, no sensor input
node. R2 (auto-rotation) is **not implemented**; manual rotation via
`kscreen-doctor` + the touch-mapping recipe above stays.

### Evidence

1. No IIO subsystem at all:

```
$ ls /sys/class/iio
ls: cannot access '/sys/class/iio/': No such file or directory
$ ls /dev/iio*
ls: cannot access '/dev/iio*': No such file or directory
```

   Per-device `name` / `in_accel_*` listing: n/a — there are zero IIO devices.

2. No HID sensor kernel modules loaded, but the stack exists on disk
   (kernel 7.2.6-070206-generic):

```
$ lsmod | grep hid_sensor
(no output)
$ modinfo hid-sensor-hub | head -1
filename: /lib/modules/7.2.6-070206-generic/kernel/drivers/hid/hid-sensor-hub.ko.zst
$ modinfo hid_sensor_accel_3d | head -1
filename: /lib/modules/7.2.6-070206-generic/kernel/drivers/iio/accel/hid-sensor-accel-3d.ko.zst
```

3. modprobe attempt and re-check (unprivileged user; sudo needs interactive
   auth on this host, so the attempt could not be completed as root):

```
$ modprobe hid-sensor-hub
modprobe: ERROR: could not insert 'hid_sensor_hub': Operation not permitted
$ sudo -n modprobe hid-sensor-hub hid-sensor-iio-common hid-sensor-accel-3d
sudo: interactive authentication is required
$ lsmod | grep hid_sensor   # re-check
(still no output)
$ ls /sys/class/iio         # re-check
ls: cannot access '/sys/class/iio/': No such file or directory
```

   Note: even a successful `modprobe` cannot produce an IIO accelerometer here —
   `hid-sensor-hub` binds only to HID devices whose report descriptor contains a
   HID Sensor usage page (0x20) collection, and no such device exists (bullet 5).

4. USB IDs (`lsusb`):

```
Bus 005 Device 008: ID 1b1c:1d0d Corsair XENEON EDGE
Bus 005 Device 013: ID 27c0:0859 Cadwell Laboratories, Inc. TouchScreen
```

   The Edge enumerates as one device with a single HID interface (5-1.3:1.0,
   `hid-generic`); the touchscreen is a separate USB device (5-1.4) with three
   interfaces (`hid-multitouch`, vendor `0xFF0A`, mouse).

5. HID report descriptors — decisive evidence. The Edge's only HID interface is
   a 32-byte vendor-page control collection (this is the interface OpenLinkHub
   writes to); the touchscreen interfaces carry multitouch/vendor/mouse
   collections. No HID Sensor usage page anywhere:

```
Edge 1B1C:1D0D (5-1.3:1), full 32-byte descriptor:
06 1b ff 09 91 a1 01 85 01 19 00 2a ff 00 15 00
26 ff 00 75 08 95 3f 81 02 19 00 29 ff 91 02 c0
           ^^^ Usage Page 0xFF1B (vendor), Usage 0x91; 63-byte in/out reports, report ID 1

$ for h in /sys/bus/hid/devices/*/report_descriptor; do 
    grep -qP '\x05\x20' "$h" && echo "SENSOR PAGE FOUND: $h"; 
  done
(no output — no 05 20 Usage Page (Sensor) in ANY HID descriptor on this system)
```

6. `/proc/bus/input/devices` — no Corsair/XENEON entry at all (the Edge control
   interface is hidraw-only, it creates no input node), and no accelerometer or
   sensor input device of any name:

```
$ grep -iE 'xeneon|corsair|1b1c' /proc/bus/input/devices
(no output)
$ grep -B1 -A5 -iE 'wch|accel|sensor' /proc/bus/input/devices
I: Bus=0003 Vendor=27c0 Product=0859 Version=0110
N: Name="wch.cn TouchScreen"
P: Phys=usb-0000:a7:00.0-1.4/input0
S: Sysfs=/devices/.../5-1.4:1.0/0003:27C0:0859.0022/input/input46
H: Handlers=mouse3 event23 
B: PROP=2
--
I: Bus=0003 Vendor=27c0 Product=0859 Version=0110
N: Name="wch.cn TouchScreen"
P: Phys=usb-0000:a7:00.0-1.4/input2
S: Sysfs=/devices/.../5-1.4:1.2/0003:27C0:0859.0024/input/input47
H: Handlers=mouse4 event24 
B: PROP=0
```

   (Only the two touchscreen nodes exist; no accel/sensor input device.)

7. `libinput list-devices`: the `libinput` CLI tools are not installed on this
   host (`command -v libinput` → not found) — tooling gap, not hardware evidence;
   the udev/input-node evidence above covers the same ground.

8. Cross-checks: `/sys/bus/i2c/devices` contains only spd5118 RAM temperature
   sensors plus two undriven ACPI nodes (`i2c-ITE8853:00`, `i2c-MSFT8000:00` on
   i2c-3 — board-internal, unrelated to the external Edge); NVIDIA i2c adapters
   are GPU connector DDC buses.

### Interpretation

- Classification C per the roadmap taxonomy: no IIO accelerometer (A), and no
  raw HID sensor interface that merely lacks a kernel driver (B) — the device
  simply exposes nothing host-readable.
- G9/R2 (orientation polling, `CurrentRotation()`, `/api/xeneon/orientation`,
  kiosk CSS rotation) is therefore **not implementable on this host** and was
  not built. There are no accel readings to calibrate, so the mount-matrix
  question (gravity vs physical orientation) is moot for now.
- Manual rotation stays the supported path: rotate via KDE display settings,
  then fix touch mapping per the recipe at the top of this file.
- [INFERENCE] The only remaining host-visible path to sensor data would be the
  Edge's vendor control interface (page 0xFF1B, 63-byte reports on hidraw) —
  an undocumented Corsair protocol; sniffing it is out of scope for R1/R2 and
  should only be attempted with the vendor protocol reversed separately.