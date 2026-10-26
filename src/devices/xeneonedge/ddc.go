package xeneonedge

// Package: CORSAIR XENEON EDGE
// Author: Nikola Jurkovic
// License: GPL-3.0 or later

// DDC/CI display control for the Edge panel (VESA Monitor Control Command Set
// over the panel's DP-aux i2c bus).
//
// The bus is located once and the /dev/i2c-N handle is cached; a scan failure
// triggers a re-scan on the next call. Every exported call is synchronous and
// bounded by ddcCallBudget (no work is ever left running after the HTTP
// request that triggered it returns), guarded by a single mutex.

import (
	"OpenLinkHub/src/logger"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	ddcSlaveAddress   = 0x37 // DDC/CI device address on the i2c bus
	ddcSourceAddress  = 0x51 // host (source) address
	ddcDestAddress    = 0x6E // display (destination) address
	ddcOpGetVCP       = 0x01 // Get VCP Feature
	ddcOpSetVCP       = 0x03 // Set VCP Feature
	ddcOpGetVCPReply  = 0x02 // Get VCP Feature reply
	ddcGetReplyDataCt = 8    // reply payload bytes (op, result, vcp, type, max, cur)
	ddcReplyBytes     = 11   // source + length + 8 payload bytes + checksum
	ddcVcpUnsupported = 0x01 // reply result code: VCP code not supported

	i2cRdwr    = 0x0707 // I2C_RDWR ioctl request
	i2cMsgRead = 0x0001 // I2C_M_RD message flag

	ddcEdidAddress = 0x50 // EDID address on the i2c bus

	ddcEdgeMode          = "2560x720"             // DRM mode unique to the Edge panel
	ddcReplyChecksumSeed = 0x50                   // host write-address byte, checksum only
	ddcCallBudget        = 2 * time.Second        // worst case per exported call
	ddcTxnBudget         = 350 * time.Millisecond // budget per i2c transaction
	ddcMaxTxnAttempts    = 3                      // retries per transaction
	ddcTxnBackoff        = 25 * time.Millisecond  // backoff between retries
	ddcReplyDelay        = 40 * time.Millisecond  // settle time between write and read
	ddcSetAttempts       = 3                      // write + read-back verify rounds
	ddcVerifyDelay       = 60 * time.Millisecond  // settle time before read-back
)

// VCP codes exposed by the Edge panel.
const (
	vcpBrightness uint8 = 0x10
	vcpContrast   uint8 = 0x12
	vcpRedGain    uint8 = 0x16
	vcpGreenGain  uint8 = 0x18
	vcpBlueGain   uint8 = 0x1A
)

// DisplaySetting is the user-facing state of one display VCP code.
type DisplaySetting struct {
	Current   int  `json:"current"`
	Max       int  `json:"max"`
	Available bool `json:"available"`
}

// ddcVcpCodes maps setting names to VCP feature codes.
var ddcVcpCodes = map[string]uint8{
	"brightness": vcpBrightness,
	"contrast":   vcpContrast,
	"red":        vcpRedGain,
	"green":      vcpGreenGain,
	"blue":       vcpBlueGain,
}

// ddcCodeState caches per-code availability and the last seen max value.
type ddcCodeState struct {
	available bool
	max       int
}

var (
	ddcMutex     sync.Mutex
	ddcFile      *os.File // cached bus handle
	ddcBusDevice string   // /dev/i2c-N currently opened
	ddcStates    = map[uint8]*ddcCodeState{}
)

var (
	ddcPciAddressRegex = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-9a-f]$`)
	ddcI2cNameRegex    = regexp.MustCompile(`^i2c-[0-9]+$`)
)

// ddcI2cMsg mirrors struct i2c_msg from <linux/i2c.h>.
type ddcI2cMsg struct {
	addr  uint16
	flags uint16
	len   uint16
	buf   unsafe.Pointer
}

// ddcI2cRdwrData mirrors struct i2c_rdwr_ioctl_data from <linux/i2c.h>.
type ddcI2cRdwrData struct {
	msgs  unsafe.Pointer
	count uint32
}

// GetDisplaySettings returns the current value, max value and availability of
// every supported display VCP code. Codes that hard-fail are reported as
// unavailable and stay cached as such until the bus is re-scanned.
func GetDisplaySettings() map[string]DisplaySetting {
	ddcMutex.Lock()
	defer ddcMutex.Unlock()

	settings := make(map[string]DisplaySetting, len(ddcVcpCodes))
	deadline := time.Now().Add(ddcCallBudget)
	f, err := ddcEnsureBus()
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Warn("Unable to open DDC/CI bus")
		for _, name := range ddcCodeNames() {
			settings[name] = DisplaySetting{}
		}
		return settings
	}

	for _, name := range ddcCodeNames() {
		code := ddcVcpCodes[name]
		if state, ok := ddcStates[code]; ok && !state.available {
			settings[name] = DisplaySetting{}
			continue
		}
		if !time.Now().Before(deadline) {
			// Overall call budget spent; report remaining codes as unknown.
			settings[name] = DisplaySetting{}
			continue
		}
		current, max, err := ddcGetVCP(f, code)
		if err != nil && ddcBusGone(err) {
			if nf, rescanErr := ddcRescanLocked(); rescanErr == nil {
				f = nf
				current, max, err = ddcGetVCP(nf, code)
			}
		}
		if err != nil {
			if ddcPermanentError(err) {
				ddcMarkUnavailable(code, err)
			}
			settings[name] = DisplaySetting{}
			continue
		}
		ddcStates[code] = &ddcCodeState{available: true, max: int(max)}
		settings[name] = DisplaySetting{Current: int(current), Max: int(max), Available: true}
	}
	return settings
}

// SetDisplaySetting writes one display VCP code and verifies the write by
// reading the value back. Valid codes: brightness, contrast, red, green, blue.
// Values outside the code's hardware range (0..max) are rejected with an
// error; callers clamp at the API boundary.
func SetDisplaySetting(code string, value int) error {
	name := strings.ToLower(strings.TrimSpace(code))
	vcp, ok := ddcVcpCodes[name]
	if !ok {
		return fmt.Errorf("unknown display code %q (valid: brightness, contrast, red, green, blue)", code)
	}

	ddcMutex.Lock()
	defer ddcMutex.Unlock()

	if state, ok := ddcStates[vcp]; ok && !state.available {
		// Stale latch: rescan clears the availability cache; if the bus is
		// really gone the rescan fails and the error stands.
		if _, rescanErr := ddcRescanLocked(); rescanErr != nil {
			return fmt.Errorf("display code %q is not available on this panel", name)
		}
	}

	deadline := time.Now().Add(ddcCallBudget)
	f, err := ddcEnsureBus()
	if err != nil {
		return fmt.Errorf("display bus not found: %w", err)
	}

	// Clamp to the code's hardware range; read the max value on first use.
	maxValue := -1
	if state, ok := ddcStates[vcp]; ok && state.max > 0 {
		maxValue = state.max
	}
	if maxValue < 0 {
		_, max, err := ddcGetVCP(f, vcp)
		if err != nil {
			if ddcBusGone(err) {
				if nf, rescanErr := ddcRescanLocked(); rescanErr == nil {
					f = nf
					_, max, err = ddcGetVCP(nf, vcp)
				}
			}
		}
		if err != nil {
			if ddcPermanentError(err) {
				ddcMarkUnavailable(vcp, err)
			}
			return fmt.Errorf("display code %q read failed: %w", name, err)
		}
		maxValue = int(max)
	}
	if value < 0 {
		return fmt.Errorf("display code %q value %d out of range", name, value)
	}
	if value > maxValue {
		return fmt.Errorf("display code %q value %d out of range (max %d)", name, value, maxValue)
	}

	var lastErr error
	for attempt := range ddcSetAttempts {
		if attempt > 0 {
			time.Sleep(ddcVerifyDelay)
		}
		if !time.Now().Before(deadline) {
			break
		}
		if err := ddcWriteVCP(f, vcp, uint16(value)); err != nil {
			lastErr = err
			if ddcBusGone(err) {
				if nf, rescanErr := ddcRescanLocked(); rescanErr == nil {
					f = nf
				}
			}
			continue
		}
		time.Sleep(ddcVerifyDelay)
		current, _, err := ddcGetVCP(f, vcp)
		if err != nil {
			lastErr = err
			continue
		}
		if int(current) == value {
			ddcStates[vcp] = &ddcCodeState{available: true, max: maxValue}
			return nil
		}
		lastErr = fmt.Errorf("write not confirmed: wrote %d, panel reports %d", value, current)
	}

	if lastErr != nil && ddcPermanentError(lastErr) {
		ddcMarkUnavailable(vcp, lastErr)
	}
	if lastErr == nil {
		lastErr = errors.New("display write timed out")
	}
	return fmt.Errorf("display code %q set failed: %w", name, lastErr)
}

// AvailableCodes returns the names of display codes that are currently usable.
// Codes never probed yet are reported as available; a code is dropped after
// its first hard error and stays cached as unavailable until the bus is
// re-scanned (which resets the cache).
func AvailableCodes() []string {
	ddcMutex.Lock()
	defer ddcMutex.Unlock()

	codes := make([]string, 0, len(ddcVcpCodes))
	for _, name := range ddcCodeNames() {
		if state, ok := ddcStates[ddcVcpCodes[name]]; ok && !state.available {
			continue
		}
		codes = append(codes, name)
	}
	return codes
}

// ddcCodeNames returns the supported setting names in sorted order.
func ddcCodeNames() []string {
	names := make([]string, 0, len(ddcVcpCodes))
	for name := range ddcVcpCodes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ddcMarkUnavailable caches a VCP code as unavailable after a hard error.
func ddcMarkUnavailable(code uint8, err error) {
	ddcStates[code] = &ddcCodeState{available: false}
	logger.Log(logger.Fields{
		"error": err,
		"code":  fmt.Sprintf("0x%02x", code),
	}).Warn("Display VCP code marked unavailable")
}

// ddcEnsureBus returns the cached bus handle, locating and opening the bus on
// first use. The caller must hold ddcMutex.
func ddcEnsureBus() (*os.File, error) {
	if ddcFile != nil {
		return ddcFile, nil
	}
	device, err := ddcScanBus()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(device, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	logger.Log(logger.Fields{"device": device}).Info("DDC/CI bus discovered")
	ddcFile = f
	ddcBusDevice = device
	return f, nil
}

// ddcRescanLocked drops the cached bus handle and locates the bus again. The
// caller must hold ddcMutex.
func ddcRescanLocked() (*os.File, error) {
	if ddcFile != nil {
		if err := ddcFile.Close(); err != nil {
			logger.Log(logger.Fields{"error": err}).Warn("Failed to close DDC/CI bus handle")
		}
		ddcFile = nil
	}
	// A fresh bus invalidates the availability cache; codes are re-probed on
	// the next call.
	ddcStates = make(map[uint8]*ddcCodeState)
	return ddcEnsureBus()
}

// ddcScanBus locates the /dev/i2c-N node carrying the Edge panel's DDC/CI
// channel. Preferred: the DRM connector "ddc" symlink (amdgpu/i915 style).
// Fallback: the NVIDIA proprietary driver exposes DP-aux channels as plain
// i2c adapters parented to the GPU with no ddc symlink, so the adapters of the
// card that owns a 2560x720 connector are probed for the panel.
func ddcScanBus() (string, error) {
	links, _ := filepath.Glob("/sys/class/drm/card*-*/ddc")
	for _, link := range links {
		connector := filepath.Dir(link)
		if !ddcConnectorHasMode(connector) {
			continue
		}
		target, err := os.Readlink(link)
		if err != nil {
			continue
		}
		name := filepath.Base(target)
		if ddcI2cNameRegex.MatchString(name) {
			return "/dev/" + name, nil
		}
	}

	modes, _ := filepath.Glob("/sys/class/drm/card*-*/modes")
	for _, modesFile := range modes {
		connector := filepath.Dir(modesFile)
		if !ddcConnectorHasMode(connector) {
			continue
		}
		pci := ddcConnectorPciAddress(connector)
		if pci == "" {
			continue
		}
		if device := ddcProbeCardAdapters(pci); device != "" {
			return device, nil
		}
	}

	return "", errors.New("no DDC/CI bus found for the 2560x720 panel")
}

// ddcConnectorHasMode reports whether a DRM connector exposes ddcEdgeMode.
func ddcConnectorHasMode(connector string) bool {
	data, err := os.ReadFile(filepath.Join(connector, "modes"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == ddcEdgeMode || strings.HasPrefix(line, ddcEdgeMode+" ") {
			return true
		}
	}
	return false
}

// ddcConnectorPciAddress resolves the PCI address of the GPU owning a DRM
// connector (e.g. 0000:c1:00.0).
func ddcConnectorPciAddress(connector string) string {
	real, err := filepath.EvalSymlinks(filepath.Join(connector, "device"))
	if err != nil {
		return ""
	}
	for dir := real; dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		if ddcPciAddressRegex.MatchString(filepath.Base(dir)) {
			return filepath.Base(dir)
		}
	}
	return ""
}

// ddcPciAdapters lists /dev/i2c-N nodes of i2c adapters parented to pciAddr,
// in ascending adapter order.
func ddcPciAdapters(pciAddr string) []string {
	short := pciAddr
	if idx := strings.Index(short, ":"); idx > 0 {
		// NVIDIA names adapters "at bb:dd.f" without the PCI domain prefix.
		short = short[idx+1:]
	}
	matches, _ := filepath.Glob("/sys/bus/i2c/devices/i2c-*")
	devices := make([]string, 0, len(matches))
	numbers := make(map[string]int, len(matches))
	for _, match := range matches {
		name := filepath.Base(match)
		if !ddcI2cNameRegex.MatchString(name) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(match, "name"))
		if err != nil {
			continue
		}
		adapter := strings.TrimSpace(string(data))
		if !strings.Contains(adapter, " at ") {
			continue
		}
		at := adapter[strings.LastIndex(adapter, " at ")+4:]
		if at != pciAddr && at != short {
			continue
		}
		number := 0
		fmt.Sscanf(name, "i2c-%d", &number)
		devices = append(devices, "/dev/"+name)
		numbers["/dev/"+name] = number
	}
	sort.Slice(devices, func(i, j int) bool {
		return numbers[devices[i]] < numbers[devices[j]]
	})
	return devices
}

// ddcProbeCardAdapters probes the GPU's i2c adapters for the Edge panel.
// Preference: an adapter whose EDID identifies a CORSAIR/XENEON panel, then
// one answering DDC/CI, then one with any readable EDID.
func ddcProbeCardAdapters(pciAddr string) string {
	var withDdc, withEdid string
	for _, device := range ddcPciAdapters(pciAddr) {
		f, err := os.OpenFile(device, os.O_RDWR, 0)
		if err != nil {
			continue
		}
		edid, edidErr := ddcReadEdid(f)
		if err := f.Close(); err != nil {
			logger.Log(logger.Fields{"error": err, "device": device}).Warn("Failed to close i2c device")
		}
		if edidErr == nil && ddcEdidIsCorsair(edid) {
			return device
		}
		if edidErr == nil && withEdid == "" {
			withEdid = device
		}
		if edidErr == nil && withDdc == "" && ddcProbeDDC(device) {
			withDdc = device
		}
	}
	if withDdc != "" {
		return withDdc
	}
	if withEdid != "" {
		return withEdid
	}
	return ""
}

// ddcEdidIsCorsair reports whether an EDID block names a CORSAIR/XENEON panel.
func ddcEdidIsCorsair(edid []byte) bool {
	hay := strings.ToUpper(string(edid))
	return strings.Contains(hay, "XENEON") || strings.Contains(hay, "CORSAIR")
}

// ddcProbeDDC reports whether a device answers a Get VCP request at 0x37.
func ddcProbeDDC(device string) bool {
	f, err := os.OpenFile(device, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer func() {
		if err := f.Close(); err != nil {
			logger.Log(logger.Fields{"error": err, "device": device}).Warn("Failed to close i2c device")
		}
	}()
	_, _, err = ddcGetVCP(f, vcpBrightness)
	return err == nil
}

// ddcGetVCP reads one VCP code and returns its current and max values.
func ddcGetVCP(f *os.File, code uint8) (uint16, uint16, error) {
	frame := ddcGetFrame(code)
	var reply [ddcReplyBytes]byte
	var current, max uint16
	err := ddcWithRetries(func() error {
		if err := ddcWrite(f, frame); err != nil {
			return err
		}
		time.Sleep(ddcReplyDelay)
		if err := ddcRead(f, reply[:]); err != nil {
			return err
		}
		cur, mx, err := ddcParseGetReply(reply[:], code)
		if err != nil {
			return err
		}
		current, max = cur, mx
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return current, max, nil
}

// ddcWriteVCP sends a Set VCP Feature request. Confirmation happens through a
// separate read-back in SetDisplaySetting.
func ddcWriteVCP(f *os.File, code uint8, value uint16) error {
	frame := ddcSetFrame(code, value)
	return ddcWithRetries(func() error {
		return ddcWrite(f, frame)
	})
}

// ddcParseGetReply decodes a Get VCP Feature reply:
// [source 0x6E, length|0x80, op 0x02, result code, VCP code, type,
// max (2 bytes), current (2 bytes), checksum], all multi-byte fields big
// endian. The length byte counts the 8 payload bytes, not the checksum, and
// carries the 0x80 DDC/CI flag. The checksum covers the host's own write
// address byte (0x50), which is likewise implied by the bus transaction.
func ddcParseGetReply(reply []byte, code uint8) (uint16, uint16, error) {
	if len(reply) < ddcReplyBytes {
		return 0, 0, fmt.Errorf("short reply: %d bytes", len(reply))
	}
	sum := byte(ddcReplyChecksumSeed)
	for _, b := range reply[:ddcReplyBytes-1] {
		sum ^= b
	}
	if sum != reply[ddcReplyBytes-1] {
		return 0, 0, errors.New("checksum mismatch")
	}
	if reply[0] != ddcDestAddress {
		return 0, 0, fmt.Errorf("unexpected reply source 0x%02x", reply[0])
	}
	if reply[1]&0x7f != ddcGetReplyDataCt {
		return 0, 0, fmt.Errorf("unexpected reply length 0x%02x", reply[1])
	}
	if reply[2] != ddcOpGetVCPReply {
		return 0, 0, fmt.Errorf("unexpected reply format 0x%02x", reply[2])
	}
	if reply[3] == ddcVcpUnsupported {
		return 0, 0, errVcpUnsupported
	}
	if reply[3] != 0x00 {
		return 0, 0, fmt.Errorf("panel reply result code 0x%02x", reply[3])
	}
	if reply[4] != code {
		return 0, 0, fmt.Errorf("reply for wrong VCP code 0x%02x", reply[4])
	}
	max := uint16(reply[6])<<8 | uint16(reply[7])
	current := uint16(reply[8])<<8 | uint16(reply[9])
	return current, max, nil
}

// ddcGetFrame builds a Get VCP Feature request frame.
func ddcGetFrame(code uint8) []byte {
	frame := []byte{ddcSourceAddress, 0x80 | 0x02, ddcOpGetVCP, code, 0}
	frame[len(frame)-1] = ddcChecksum(frame[:len(frame)-1])
	return frame
}

// ddcSetFrame builds a Set VCP Feature request frame.
func ddcSetFrame(code uint8, value uint16) []byte {
	frame := []byte{ddcSourceAddress, 0x80 | 0x04, ddcOpSetVCP, code, byte(value >> 8), byte(value), 0}
	frame[len(frame)-1] = ddcChecksum(frame[:len(frame)-1])
	return frame
}

// ddcChecksum returns the DDC/CI frame checksum. The destination address byte
// (0x6E = 0x37<<1) is the i2c address byte emitted by the controller, so it is
// not part of the transmitted payload, but it is part of the checksummed
// logical frame per the DDC/CI spec.
func ddcChecksum(frame []byte) byte {
	sum := byte(ddcDestAddress)
	for _, b := range frame {
		sum ^= b
	}
	return sum
}

// ddcWithRetries runs op with bounded retries and backoff. i2c-dev ignores
// O_NONBLOCK for I2C_RDWR transactions, so the per-call budget is enforced
// with a deadline and a bounded attempt count instead.
func ddcWithRetries(op func() error) error {
	deadline := time.Now().Add(ddcTxnBudget)
	var err error
	for attempt := range ddcMaxTxnAttempts {
		if attempt > 0 {
			time.Sleep(ddcTxnBackoff * time.Duration(attempt))
		}
		if !time.Now().Before(deadline) {
			break
		}
		if err = op(); err == nil {
			return nil
		}
		if ddcBusGone(err) {
			break // stale handle; caller re-scans
		}
	}
	return err
}

var (
	errVcpUnsupported = errors.New("VCP code not supported by panel")
)

// ddcPermanentError reports whether a failure means a VCP code is definitively
// unusable (panel rejects it) or the bus is gone. Transient i2c hiccups and
// busy-panel read-back mismatches must not latch a code as unavailable.
func ddcPermanentError(err error) bool {
	return errors.Is(err, errVcpUnsupported) || ddcBusGone(err)
}

// ddcBusGone reports whether an error means the cached bus handle is stale
// (device unbound or node removed) and a bus re-scan is required.
func ddcBusGone(err error) bool {
	return errors.Is(err, unix.ENODEV) ||
		errors.Is(err, unix.EBADF) ||
		errors.Is(err, unix.ENXIO) ||
		errors.Is(err, unix.ENOTTY)
}

// ddcWrite performs a write transaction to the DDC/CI slave address.
func ddcWrite(f *os.File, buf []byte) error {
	msgs := []ddcI2cMsg{{
		addr: ddcSlaveAddress,
		len:  uint16(len(buf)),
		buf:  unsafe.Pointer(&buf[0]),
	}}
	return ddcIoctlRdwr(f, msgs)
}

// ddcRead performs a read transaction from the DDC/CI slave address.
func ddcRead(f *os.File, buf []byte) error {
	msgs := []ddcI2cMsg{{
		addr:  ddcSlaveAddress,
		flags: i2cMsgRead,
		len:   uint16(len(buf)),
		buf:   unsafe.Pointer(&buf[0]),
	}}
	return ddcIoctlRdwr(f, msgs)
}

// ddcReadEdid reads the first EDID block from address 0x50 as a single
// combined write-read transaction (repeated start).
func ddcReadEdid(f *os.File) ([]byte, error) {
	offset := []byte{0x00}
	edid := make([]byte, 128)
	msgs := []ddcI2cMsg{
		{addr: ddcEdidAddress, len: uint16(len(offset)), buf: unsafe.Pointer(&offset[0])},
		{addr: ddcEdidAddress, flags: i2cMsgRead, len: uint16(len(edid)), buf: unsafe.Pointer(&edid[0])},
	}
	if err := ddcIoctlRdwr(f, msgs); err != nil {
		return nil, err
	}
	return edid, nil
}

// ddcIoctlRdwr performs an I2C_RDWR ioctl: one or more i2c messages in a
// single bus transaction (raw user-space i2c, same pattern as src/smbus).
func ddcIoctlRdwr(f *os.File, msgs []ddcI2cMsg) error {
	data := ddcI2cRdwrData{
		msgs:  unsafe.Pointer(&msgs[0]),
		count: uint32(len(msgs)),
	}
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), i2cRdwr, uintptr(unsafe.Pointer(&data)))
	if errno != 0 {
		return errno
	}
	return nil
}
