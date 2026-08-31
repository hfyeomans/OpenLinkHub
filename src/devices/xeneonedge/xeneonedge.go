package xeneonedge

// Package: CORSAIR XENEON EDGE
// Author: Nikola Jurkovic
// License: GPL-3.0 or later

import (
	"OpenLinkHub/src/common"
	"OpenLinkHub/src/config"
	"OpenLinkHub/src/logger"
	"encoding/json"
	"fmt"
	"github.com/sstallion/go-hid"
	"os"
	"regexp"
	"slices"
	"strings"
)

// DeviceProfile struct contains all device profile
type DeviceProfile struct {
	Active      bool
	Path        string
	Product     string
	Serial      string
	WidgetAreas map[int]WidgetArea
	RgbOff      bool
	Widgets     []Widget `json:"widgets"`
}

type WidgetArea struct {
	WidgetId int `json:"widgetId"`
	Span     int `json:"span,omitempty"`
}

// RenderSlot is a resolved widget ready to render into a kiosk column, together
// with the number of areas it spans.
type RenderSlot struct {
	Widget *Widget
	Span   int
}
type Device struct {
	dev             *hid.Device
	Debug           bool
	Manufacturer    string                    `json:"manufacturer"`
	Product         string                    `json:"product"`
	Serial          string                    `json:"serial"`
	Firmware        string                    `json:"firmware"`
	UserProfiles    map[string]*DeviceProfile `json:"userProfiles"`
	Devices         map[int]string            `json:"devices"`
	Widgets         []Widget                  `json:"widgets"`
	DeviceProfile   *DeviceProfile
	OriginalProfile *DeviceProfile
	Template        string
	VendorId        uint16
	ProductId       uint16
	instance        *common.Device
}

type Widget struct {
	Id              int     `json:"id"`
	Name            string  `json:"name"`
	Template        string  `json:"template"`
	Columns         []int   `json:"columns"`
	GpuIndex        int     `json:"gpuIndex"`
	City            string  `json:"city"`
	Country         string  `json:"country"`
	Latitude        float64 `json:"latitude"`
	Longitude       float64 `json:"longitude"`
	Source          string  `json:"source"`
	AutoWeather     bool    `json:"autoWeather"`
	DataColor       string  `json:"dataColor"`
	Max             int     `json:"max"`
	HeaderText      string  `json:"headerText"`
	Unit            string  `json:"unit"`
	TextColor       string  `json:"textColor"`
	Url             string  `json:"url"`
	MediaFile       string  `json:"mediaFile"`
	Fit             string  `json:"fit"`
	Interval        int     `json:"interval"`
	BackgroundColor string  `json:"backgroundColor"`
}

var (
	pwd           = ""
	hexColorRegex = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

func Init(vendorId, productId uint16, _, path string) *common.Device {
	// Set global working directory
	pwd = config.GetConfig().ConfigPath

	dev, err := hid.OpenPath(path)
	if err != nil {
		logger.Log(logger.Fields{"error": err, "vendorId": vendorId, "productId": productId, "path": path}).Error("Unable to open HID device")
		return nil
	}

	// Init new struct with HID device
	d := &Device{
		dev:       dev,
		Template:  "xeneonedge.html",
		VendorId:  vendorId,
		ProductId: productId,
		Firmware:  "n/a",
		Product:   "XENEON EDGE",
	}

	d.getDebugMode()       // Debug mode
	d.getManufacturer()    // Manufacturer
	d.getSerial()          // Serial
	d.loadWidgets()        // Widgets
	d.loadDeviceProfiles() // Load all device profiles
	d.saveDeviceProfile()  // Save profile
	d.createDevice()       // Device register
	logger.Log(logger.Fields{"serial": d.Serial, "product": d.Product}).Info("Device successfully initialized")
	return d.instance
}

// createDevice will create new device register object
func (d *Device) createDevice() {
	d.instance = &common.Device{
		ProductType: common.ProductTypeXeneonEdge,
		Product:     d.Product,
		Serial:      d.Serial,
		Firmware:    d.Firmware,
		Image:       "icon-lcd.svg",
		Instance:    d,
	}
}

// Stop will stop all device operations and switch a device back to hardware mode
func (d *Device) Stop() {
	if d.dev != nil {
		err := d.dev.Close()
		if err != nil {
			return
		}
	}
	logger.Log(logger.Fields{"serial": d.Serial, "product": d.Product}).Info("Device stopped")
}

// StopDirty will stop device in a dirty way
func (d *Device) StopDirty() uint8 {
	logger.Log(logger.Fields{"serial": d.Serial, "product": d.Product}).Info("Device stopped")
	return 1
}

// getManufacturer will return device manufacturer
func (d *Device) getManufacturer() {
	manufacturer, err := d.dev.GetMfrStr()
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Fatal("Unable to get manufacturer")
	}
	d.Manufacturer = manufacturer
}

// getSerial will return device serial number
func (d *Device) getSerial() {
	serial, err := d.dev.GetSerialNbr()
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Fatal("Unable to get device serial number")
	}
	d.Serial = serial
}

// loadWidgets will load xeneon widgets
func (d *Device) loadWidgets() {
	location := pwd + "/database/xeneon/xeneon.json"

	file, fe := os.Open(location)
	if fe != nil {
		logger.Log(logger.Fields{"error": fe, "location": location}).Warn("Unable to open widgets file")
		return
	}

	defer func(file *os.File) {
		err := file.Close()
		if err != nil {
			//
		}
	}(file)

	reader := json.NewDecoder(file)
	if err := reader.Decode(&d); err != nil {
		fmt.Println(err)
		logger.Log(logger.Fields{"error": err, "location": location}).Warn("Unable to decode widgets file")
		return
	}
}

// GetDeviceTemplate will return device template name
func (d *Device) GetDeviceTemplate() string {
	return d.Template
}

// ChangeDeviceProfile will change device profile
func (d *Device) ChangeDeviceProfile(profileName string) uint8 {
	if profile, ok := d.UserProfiles[profileName]; ok {
		currentProfile := d.DeviceProfile
		currentProfile.Active = false
		d.DeviceProfile = currentProfile
		d.saveDeviceProfile()

		newProfile := profile
		newProfile.Active = true
		d.DeviceProfile = newProfile
		d.saveDeviceProfile()
		return 1
	}
	return 0
}

// DeleteDeviceProfile will delete device profile
func (d *Device) DeleteDeviceProfile(profileName string) uint8 {
	profile, ok := d.UserProfiles[profileName]
	if !ok {
		return 0
	}

	if !common.IsValidExtension(profile.Path, ".json") {
		return 0
	}

	if profile.Active {
		return 2
	}

	if err := os.Remove(profile.Path); err != nil {
		return 3
	}

	delete(d.UserProfiles, profileName)

	return 1
}

// areaColumn will return the kiosk column for a widget area
func areaColumn(areaId int) int {
	switch {
	case areaId >= 1 && areaId <= 2:
		return 1
	case areaId >= 3 && areaId <= 8:
		return 2
	case areaId >= 9 && areaId <= 11:
		return 3
	}
	return 0
}

// columnAreas lists the kiosk areas per side column in vertical order. The middle
// column (rings) is intentionally excluded — it does not support spanning.
var columnAreas = map[int][]int{
	1: {1, 2},
	3: {9, 10, 11},
}

// areasRemainingInColumn returns how many areas remain from areaId to the bottom
// of its column (1 when the area's column does not support spanning).
func areasRemainingInColumn(areaId int) int {
	areas, ok := columnAreas[areaColumn(areaId)]
	if !ok {
		return 1
	}
	for i, id := range areas {
		if id == areaId {
			return len(areas) - i
		}
	}
	return 1
}

// AreaSpanChoices returns the valid span values for an area (nil when the area
// cannot span more than one slot), for building the config UI.
func (d *Device) AreaSpanChoices(areaId int) []int {
	n := areasRemainingInColumn(areaId)
	if n <= 1 {
		return nil
	}
	choices := make([]int, n)
	for i := range choices {
		choices[i] = i + 1
	}
	return choices
}

// AreaSpan returns the configured span for an area (1 when unset).
func (d *Device) AreaSpan(areaId int) int {
	if d.DeviceProfile != nil {
		if area, ok := d.DeviceProfile.WidgetAreas[areaId]; ok && area.Span > 1 {
			return area.Span
		}
	}
	return 1
}

// AreaWidget resolves the widget assigned to an area, or nil when unassigned.
// Called from templates, so it must stay exported.
func (d *Device) AreaWidget(areaId int) *Widget {
	if d.DeviceProfile == nil {
		return nil
	}
	if area, ok := d.DeviceProfile.WidgetAreas[areaId]; ok {
		return d.getProfileWidget(area.WidgetId)
	}
	return nil
}

// SegmentSlots resolves the widgets for a column's area ids in order, skipping
// areas covered by a preceding widget that spans multiple areas. Called from
// templates to render the side columns.
func (d *Device) SegmentSlots(areaIds ...int) []RenderSlot {
	var slots []RenderSlot
	if d.DeviceProfile == nil {
		return slots
	}
	skip := 0
	for idx, id := range areaIds {
		if skip > 0 {
			skip--
			continue
		}
		widget := d.AreaWidget(id)
		if widget == nil {
			continue
		}
		span := d.DeviceProfile.WidgetAreas[id].Span
		if span < 1 {
			span = 1
		}
		if remaining := len(areaIds) - idx; span > remaining {
			span = remaining
		}
		slots = append(slots, RenderSlot{Widget: widget, Span: span})
		skip = span - 1
	}
	return slots
}

// getProfileWidget will return a widget from the active device profile
func (d *Device) getProfileWidget(widgetId int) *Widget {
	if d.DeviceProfile == nil {
		return nil
	}
	for i := range d.DeviceProfile.Widgets {
		if d.DeviceProfile.Widgets[i].Id == widgetId {
			return &d.DeviceProfile.Widgets[i]
		}
	}
	return nil
}

// UpdateWidgetArea will assign a widget to a widget area. widgetId 0 clears the area.
func (d *Device) UpdateWidgetArea(areaId int, widgetId int) uint8 {
	if d.DeviceProfile == nil {
		return 0
	}

	if _, ok := d.DeviceProfile.WidgetAreas[areaId]; !ok {
		return 0
	}

	if widgetId == 0 {
		d.DeviceProfile.WidgetAreas[areaId] = WidgetArea{}
		d.saveDeviceProfile()
		return 1
	}

	widget := d.getProfileWidget(widgetId)
	if widget == nil {
		return 0
	}

	if !slices.Contains(widget.Columns, areaColumn(areaId)) {
		return 0
	}

	// Widget can be placed only once, clear its previous area
	for key, area := range d.DeviceProfile.WidgetAreas {
		if area.WidgetId == widgetId && key != areaId {
			d.DeviceProfile.WidgetAreas[key] = WidgetArea{}
		}
	}

	d.DeviceProfile.WidgetAreas[areaId] = WidgetArea{WidgetId: widgetId}
	d.saveDeviceProfile()
	return 1
}

// UpdateWidgetSpan will set how many consecutive areas a widget occupies. Span
// is clamped to the area's column so a widget cannot bleed past its column.
func (d *Device) UpdateWidgetSpan(areaId int, span int) uint8 {
	if d.DeviceProfile == nil {
		return 0
	}

	area, ok := d.DeviceProfile.WidgetAreas[areaId]
	if !ok || area.WidgetId == 0 {
		return 0
	}

	if span < 1 || span > areasRemainingInColumn(areaId) {
		return 0
	}

	area.Span = span
	d.DeviceProfile.WidgetAreas[areaId] = area
	d.saveDeviceProfile()
	return 1
}

// UpdateWidgetSettings will update widget configuration from a JSON payload
func (d *Device) UpdateWidgetSettings(widgetId int, data string) uint8 {
	widget := d.getProfileWidget(widgetId)
	if widget == nil {
		return 0
	}

	settings := &struct {
		City            *string  `json:"city"`
		Country         *string  `json:"country"`
		Latitude        *float64 `json:"latitude"`
		Longitude       *float64 `json:"longitude"`
		AutoWeather     *bool    `json:"autoWeather"`
		DataColor       *string  `json:"dataColor"`
		Max             *int     `json:"max"`
		HeaderText      *string  `json:"headerText"`
		Unit            *string  `json:"unit"`
		TextColor       *string  `json:"textColor"`
		Url             *string  `json:"url"`
		MediaFile       *string  `json:"mediaFile"`
		Fit             *string  `json:"fit"`
		Interval        *int     `json:"interval"`
		BackgroundColor *string  `json:"backgroundColor"`
	}{}

	if err := json.Unmarshal([]byte(data), settings); err != nil {
		logger.Log(logger.Fields{"error": err, "serial": d.Serial}).Warn("Unable to decode widget settings")
		return 0
	}

	if settings.City != nil {
		city := strings.TrimSpace(*settings.City)
		if len(city) < 1 || len(city) > 64 {
			return 0
		}
		widget.City = city
	}
	if settings.Country != nil {
		country := strings.TrimSpace(*settings.Country)
		if len(country) > 64 {
			return 0
		}
		widget.Country = country
	}
	if settings.Latitude != nil {
		if *settings.Latitude < -90 || *settings.Latitude > 90 {
			return 0
		}
		widget.Latitude = *settings.Latitude
	}
	if settings.Longitude != nil {
		if *settings.Longitude < -180 || *settings.Longitude > 180 {
			return 0
		}
		widget.Longitude = *settings.Longitude
	}
	if settings.AutoWeather != nil {
		widget.AutoWeather = *settings.AutoWeather
	}
	if settings.City != nil || settings.Latitude != nil || settings.Longitude != nil {
		widget.Source = "Configured location"
	}
	if settings.DataColor != nil {
		if !hexColorRegex.MatchString(*settings.DataColor) {
			return 0
		}
		widget.DataColor = *settings.DataColor
	}
	if settings.TextColor != nil {
		if len(*settings.TextColor) > 0 && !hexColorRegex.MatchString(*settings.TextColor) {
			return 0
		}
		widget.TextColor = *settings.TextColor
	}
	if settings.BackgroundColor != nil {
		if len(*settings.BackgroundColor) > 0 && !hexColorRegex.MatchString(*settings.BackgroundColor) {
			return 0
		}
		widget.BackgroundColor = *settings.BackgroundColor
	}
	if settings.Url != nil {
		url := strings.TrimSpace(*settings.Url)
		if len(url) > 512 {
			return 0
		}
		if len(url) > 0 && !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			return 0
		}
		widget.Url = url
	}
	if settings.MediaFile != nil {
		if len(*settings.MediaFile) > 0 && !IsValidMediaFile(*settings.MediaFile) {
			return 0
		}
		widget.MediaFile = *settings.MediaFile
	}
	if settings.Fit != nil {
		switch *settings.Fit {
		case "", "cover", "contain", "fill":
			widget.Fit = *settings.Fit
		default:
			return 0
		}
	}
	if settings.Interval != nil {
		if *settings.Interval < 2 || *settings.Interval > 3600 {
			return 0
		}
		widget.Interval = *settings.Interval
	}
	if settings.Max != nil {
		if *settings.Max < 1 || *settings.Max > 1000 {
			return 0
		}
		widget.Max = *settings.Max
	}
	if settings.HeaderText != nil {
		headerText := strings.TrimSpace(*settings.HeaderText)
		if len(headerText) > 32 {
			return 0
		}
		widget.HeaderText = headerText
	}
	if settings.Unit != nil {
		unit := strings.TrimSpace(*settings.Unit)
		if len(unit) > 8 {
			return 0
		}
		widget.Unit = unit
	}

	d.saveDeviceProfile()
	return 1
}

// SaveUserProfile will generate a new user profile configuration and save it to a file
func (d *Device) SaveUserProfile(profileName string) uint8 {
	if d.DeviceProfile != nil {
		profilePath := pwd + "/database/profiles/" + d.Serial + "-" + profileName + ".json"

		newProfile := d.DeviceProfile
		newProfile.Path = profilePath
		newProfile.Active = false

		buffer, err := json.Marshal(newProfile)
		if err != nil {
			logger.Log(logger.Fields{"error": err}).Error("Unable to convert to json format")
			return 0
		}

		// Create profile filename
		file, err := os.Create(profilePath)
		if err != nil {
			logger.Log(logger.Fields{"error": err, "location": newProfile.Path}).Error("Unable to create new device profile")
			return 0
		}

		_, err = file.Write(buffer)
		if err != nil {
			logger.Log(logger.Fields{"error": err, "location": newProfile.Path}).Error("Unable to write data")
			return 0
		}

		err = file.Close()
		if err != nil {
			logger.Log(logger.Fields{"error": err, "location": newProfile.Path}).Error("Unable to close file handle")
			return 0
		}
		d.loadDeviceProfiles()
		return 1
	}
	return 0
}

// getManufacturer will return device manufacturer
func (d *Device) getDebugMode() {
	d.Debug = config.GetConfig().Debug
}

func (d *Device) getWidget(widgetId int) *Widget {
	for _, widget := range d.Widgets {
		if widget.Id == widgetId {
			return &widget
		}
	}
	return nil
}

// saveDeviceProfile will save device profile for persistent configuration
func (d *Device) saveDeviceProfile() {
	profilePath := pwd + "/database/profiles/" + d.Serial + ".json"

	deviceProfile := &DeviceProfile{
		Product: d.Product,
		Serial:  d.Serial,
		Path:    profilePath,
	}

	// First save, assign saved profile to a device
	if d.DeviceProfile == nil {
		deviceProfile.Active = true
		deviceProfile.WidgetAreas = map[int]WidgetArea{
			1:  {WidgetId: 1},
			2:  {WidgetId: 2},
			3:  {WidgetId: 6},
			4:  {WidgetId: 7},
			5:  {WidgetId: 8},
			6:  {WidgetId: 9},
			7:  {WidgetId: 0},
			8:  {WidgetId: 0},
			9:  {WidgetId: 3},
			10: {WidgetId: 5},
			11: {WidgetId: 4},
		}
		deviceProfile.Widgets = d.Widgets
	} else {
		deviceProfile.Active = d.DeviceProfile.Active
		if len(d.DeviceProfile.Path) < 1 {
			deviceProfile.Path = profilePath
			d.DeviceProfile.Path = profilePath
		} else {
			deviceProfile.Path = d.DeviceProfile.Path
		}
		deviceProfile.WidgetAreas = d.DeviceProfile.WidgetAreas
		deviceProfile.Widgets = d.DeviceProfile.Widgets
	}

	// Convert to JSON
	buffer, err := json.MarshalIndent(deviceProfile, "", "    ")
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Unable to convert to json format")
		return
	}

	// Create profile filename
	file, fileErr := os.Create(deviceProfile.Path)
	if fileErr != nil {
		logger.Log(logger.Fields{"error": fileErr, "location": deviceProfile.Path}).Error("Unable to create new device profile")
		return
	}

	// Write JSON buffer to file
	_, err = file.Write(buffer)
	if err != nil {
		logger.Log(logger.Fields{"error": err, "location": deviceProfile.Path}).Error("Unable to write data")
		return
	}

	// Close file
	err = file.Close()
	if err != nil {
		logger.Log(logger.Fields{"error": err, "location": deviceProfile.Path}).Error("Unable to close file handle")
	}

	d.loadDeviceProfiles() // Reload
}

// loadDeviceProfiles will load custom user profiles
func (d *Device) loadDeviceProfiles() {
	profileList := make(map[string]*DeviceProfile)
	userProfileDirectory := pwd + "/database/profiles/"

	files, err := os.ReadDir(userProfileDirectory)
	if err != nil {
		logger.Log(logger.Fields{"error": err, "location": userProfileDirectory, "serial": d.Serial}).Error("Unable to read content of a folder")
		return
	}

	for _, fi := range files {
		pf := &DeviceProfile{}
		if fi.IsDir() {
			continue // Exclude folders if any
		}

		// Define a full path of filename
		profileLocation := userProfileDirectory + fi.Name()

		// Check if filename has .json extension
		if !common.IsValidExtension(profileLocation, ".json") {
			continue
		}

		fileName := strings.Split(fi.Name(), ".")[0]
		if !common.AlphanumericDashRegex.MatchString(fileName) {
			continue
		}

		fileSerial := ""
		if strings.Contains(fileName, "-") {
			fileSerial = strings.Split(fileName, "-")[0]
		} else {
			fileSerial = fileName
		}

		if fileSerial != d.Serial {
			continue
		}

		file, err := os.Open(profileLocation)
		if err != nil {
			logger.Log(logger.Fields{"error": err, "serial": d.Serial, "location": profileLocation}).Warn("Unable to load profile")
			continue
		}
		if err = json.NewDecoder(file).Decode(pf); err != nil {
			logger.Log(logger.Fields{"error": err, "serial": d.Serial, "location": profileLocation}).Warn("Unable to decode profile")
			continue
		}
		err = file.Close()
		if err != nil {
			logger.Log(logger.Fields{"location": profileLocation, "serial": d.Serial}).Warn("Failed to close file handle")
		}

		if pf.Serial == d.Serial {
			d.mergeCatalogWidgets(pf)
			if fileName == d.Serial {
				profileList["default"] = pf
			} else {
				name := strings.Split(fileName, "-")[1]
				profileList[name] = pf
			}
			logger.Log(logger.Fields{"location": profileLocation, "serial": d.Serial}).Info("Loaded custom user profile")
		}
	}
	d.UserProfiles = profileList
	d.getDeviceProfile()
}

// mergeCatalogWidgets reconciles a stored profile against the widget catalog:
// widgets missing from the profile are appended, and for widgets already present
// the catalog-owned structural fields (name, template, columns, gpu index) are
// refreshed so catalog fixes reach existing profiles. User-customizable fields
// (colors, header text, media, location, etc.) are left untouched.
func (d *Device) mergeCatalogWidgets(pf *DeviceProfile) {
	for _, catalog := range d.Widgets {
		existing := false
		for i := range pf.Widgets {
			if pf.Widgets[i].Id == catalog.Id {
				pf.Widgets[i].Name = catalog.Name
				pf.Widgets[i].Template = catalog.Template
				pf.Widgets[i].Columns = catalog.Columns
				pf.Widgets[i].GpuIndex = catalog.GpuIndex
				existing = true
				break
			}
		}
		if !existing {
			pf.Widgets = append(pf.Widgets, catalog)
		}
	}
}

// getDeviceProfile will load persistent device configuration
func (d *Device) getDeviceProfile() {
	if len(d.UserProfiles) == 0 {
		logger.Log(logger.Fields{"serial": d.Serial}).Warn("No profile found for device. Probably initial start")
	} else {
		for _, pf := range d.UserProfiles {
			if pf.Active {
				d.DeviceProfile = pf
			}
		}
	}
}
