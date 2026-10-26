package xeneonedge

// Package: CORSAIR XENEON EDGE
// Author: Nikola Jurkovic
// License: GPL-3.0 or later

import (
	"OpenLinkHub/src/config"
	"OpenLinkHub/src/language"
	"OpenLinkHub/src/logger"
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	// maxWidgetUploadSize caps the compressed upload body (zip or single html).
	maxWidgetUploadSize = 8 << 20 // 8MB
	// maxWidgetDecompressed caps the total decompressed content of an archive,
	// so a few-kilobyte zip cannot expand into a disk-filling bomb.
	maxWidgetDecompressed = 32 << 20 // 32MB
	// maxWidgetEntries caps the file count of an archive; many tiny entries are
	// still a bomb on the filesystem (inode) level even under the size cap.
	// maxWidgetCount caps distinct stored widgets (disk-fill guard).
	maxWidgetCount   = 32
	maxWidgetEntries = 512
	// widgetServePrefix is the URL prefix custom widgets are served under.
	widgetServePrefix = "/api/xeneon/widgets/"
)

var (
	widgetSlugRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

	// widgetExtensions lists the file extensions allowed inside a widget
	// archive or as a single-file upload. Anything else is rejected at upload
	// time; files are later served with the extension-derived content type.
	widgetExtensions = map[string]bool{
		".html":  true,
		".css":   true,
		".js":    true,
		".json":  true,
		".png":   true,
		".jpg":   true,
		".jpeg":  true,
		".gif":   true,
		".svg":   true,
		".webp":  true,
		".woff2": true,
	}

	errWidgetInvalid        = errors.New("invalid widget archive")
	errWidgetForbidden      = errors.New("widget archive contains a forbidden entry")
	errWidgetMissingIndex   = errors.New("widget archive has no root index.html")
	errWidgetTooLarge       = errors.New("widget content exceeds the decompressed size limit")
	errWidgetTooManyEntries = errors.New("widget archive contains too many files")
)

// WidgetInUseHook reports whether any profile widget references the given slug.
// The server layer wires this at startup (the device registry lives outside this
// package); DeleteWidget refuses deletion while the hook returns true.
var WidgetInUseHook func(slug string) bool

// CustomWidget describes one uploaded custom widget.
type CustomWidget struct {
	Slug string `json:"slug"`
}

// widgetsRoot returns the custom widget storage location
func widgetsRoot() string {
	return config.GetConfig().ConfigPath + "/database/xeneon/widgets/"
}

// widgetDirectory returns the storage folder of one widget slug
func widgetDirectory(slug string) string {
	return widgetsRoot() + slug
}

// widgetUrlSlug extracts the widget slug from an internal widget URL
// ("/api/xeneon/widgets/<slug>[/]"); returns "" when url is not one.
func widgetUrlSlug(url string) string {
	url = strings.TrimSpace(url)
	url = strings.TrimSuffix(url, "/")
	if !strings.HasPrefix(url, widgetServePrefix) {
		return ""
	}
	slug := strings.TrimPrefix(url, widgetServePrefix)
	if !widgetSlugRegex.MatchString(slug) {
		return ""
	}
	return slug
}

// IsValidWidgetUrl reports whether url is a valid internal custom-widget URL.
func IsValidWidgetUrl(url string) bool {
	return widgetUrlSlug(url) != ""
}

// ProfileReferencesWidget reports whether any profile of this device contains a
// widget entry whose URL points at the given custom widget slug.
func (d *Device) ProfileReferencesWidget(slug string) bool {
	if d == nil {
		return false
	}
	for _, profile := range d.UserProfiles {
		if profile == nil {
			continue
		}
		for i := range profile.Widgets {
			if widgetUrlSlug(profile.Widgets[i].Url) == slug {
				return true
			}
		}
	}
	return false
}

// ListWidgets returns all uploaded custom widgets
func ListWidgets() []CustomWidget {
	widgets := make([]CustomWidget, 0)
	entries, err := os.ReadDir(widgetsRoot())
	if err != nil {
		return widgets
	}
	for _, entry := range entries {
		if !entry.IsDir() || !widgetSlugRegex.MatchString(entry.Name()) {
			continue
		}
		// Only list widgets that are actually usable (root index.html present).
		if _, err = os.Stat(filepath.Join(widgetDirectory(entry.Name()), "index.html")); err != nil {
			continue
		}
		widgets = append(widgets, CustomWidget{Slug: entry.Name()})
	}
	sort.Slice(widgets, func(i, j int) bool { return widgets[i].Slug < widgets[j].Slug })
	return widgets
}

// ServeWidget serves files of an uploaded custom widget. The request path must
// stay inside the widget folder; anything else is a 404.
func ServeWidget(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")

	rest := strings.TrimPrefix(r.URL.Path, widgetServePrefix)
	rest = strings.Trim(rest, "/")
	if strings.Contains(rest, "..") {
		// Consistent 404 for traversal instead of http.ServeFile's 400.
		http.NotFound(w, r)
		return
	}
	slug, sub, _ := strings.Cut(rest, "/")
	if !widgetSlugRegex.MatchString(slug) {
		http.NotFound(w, r)
		return
	}

	root := widgetDirectory(slug)
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		http.NotFound(w, r)
		return
	}

	target := filepath.Join(root, "index.html")
	if sub != "" {
		candidate := filepath.Join(root, sub)
		// filepath.Join already cleaned the path; the prefix check rejects any
		// traversal that escaped the widget folder.
		if candidate != root && !strings.HasPrefix(candidate, root+string(os.PathSeparator)) {
			http.NotFound(w, r)
			return
		}
		target = candidate
	}
	if info, err = os.Stat(target); err != nil {
		http.NotFound(w, r)
		return
	}
	if info.IsDir() {
		// Directories are never listed; serve their index.html or 404.
		target = filepath.Join(target, "index.html")
		if _, err = os.Stat(target); err != nil {
			http.NotFound(w, r)
			return
		}
	}

	// Resolve symlinks so a link planted next to the archive files cannot point
	// outside the widget folder.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if realTarget != realRoot && !strings.HasPrefix(realTarget, realRoot+string(os.PathSeparator)) {
		http.NotFound(w, r)
		return
	}

	http.ServeFile(w, r, realTarget)
}

// DeleteWidget removes an uploaded custom widget. Returns 1 when deleted, 2 when
// the widget is still referenced by a profile (deletion refused), 0 on error.
func DeleteWidget(slug string) uint8 {
	if !widgetSlugRegex.MatchString(slug) {
		return 0
	}
	if _, err := os.Stat(widgetDirectory(slug)); err != nil {
		return 0
	}
	if WidgetInUseHook != nil && WidgetInUseHook(slug) {
		logger.Log(logger.Fields{"slug": slug}).Warn("Refusing to delete custom widget: still referenced by a profile")
		return 2
	}
	if err := os.RemoveAll(widgetDirectory(slug)); err != nil {
		logger.Log(logger.Fields{"error": err, "slug": slug}).Error("Unable to delete custom widget")
		return 0
	}
	bumpConfigRevision()
	return 1
}

// PerformWidgetUpload saves an uploaded widget archive (.zip) or a single .html
// file into the custom widget library.
func PerformWidgetUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxWidgetUploadSize)
	if err := r.ParseMultipartForm(maxWidgetUploadSize); err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Widget upload too large or invalid")
		http.Error(w, "File too large or invalid upload", http.StatusBadRequest)
		return
	}

	file, handler, err := r.FormFile("widgetArchive")
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Failed to read uploaded widget")
		http.Error(w, "Failed to read uploaded file", http.StatusBadRequest)
		return
	}
	defer func(file multipart.File) {
		if cerr := file.Close(); cerr != nil {
			logger.Log(logger.Fields{"error": cerr}).Error("Failed to close file")
		}
	}(file)

	slug := widgetSlugFromFilename(handler.Filename)
	if slug == "" {
		http.Error(w, "Invalid widget name. Letters, numbers, dash and underscore only", http.StatusBadRequest)
		return
	}

	ext := strings.ToLower(filepath.Ext(handler.Filename))
	// Aggregate cap: unlimited distinct slugs would allow disk-fill.
	if entries, derr := os.ReadDir(widgetsRoot()); derr == nil {
		count := 0
		for _, entry := range entries {
			if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".upload-") {
				count++
			}
		}
		if count >= maxWidgetCount {
			if _, serr := os.Stat(widgetDirectory(slug)); serr != nil {
				logger.Log(logger.Fields{"count": count}).Warn("Widget library full")
				http.Error(w, "Widget library is full", http.StatusBadRequest)
				return
			}
		}
	}
	if err = os.MkdirAll(widgetsRoot(), 0755); err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Failed to create widget directory")
		http.Error(w, "Failed to save widget", http.StatusInternalServerError)
		return
	}

	// Extract into a staging folder first and swap it into place only when the
	// whole archive validated, so a failed upload never leaves a broken widget.
	staging, err := os.MkdirTemp(widgetsRoot(), ".upload-")
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Failed to create staging directory")
		http.Error(w, "Failed to save widget", http.StatusInternalServerError)
		return
	}
	defer func() {
		if cerr := os.RemoveAll(staging); cerr != nil {
			logger.Log(logger.Fields{"error": cerr, "dir": staging}).Error("Failed to clean up staging directory")
		}
	}()

	switch ext {
	case ".zip":
		err = extractWidgetZip(file, handler.Size, staging)
	case ".html":
		err = storeWidgetHtml(file, staging)
	default:
		err = errWidgetInvalid
	}
	if err != nil {
		logger.Log(logger.Fields{"error": err, "slug": slug}).Error("Widget upload rejected")
		switch {
		case errors.Is(err, errWidgetTooLarge):
			http.Error(w, "Decompressed widget content exceeds the 32MB limit", http.StatusBadRequest)
		case errors.Is(err, errWidgetTooManyEntries):
			http.Error(w, "Widget archive contains too many files", http.StatusBadRequest)
		case errors.Is(err, errWidgetMissingIndex):
			http.Error(w, "Widget archive must contain a root index.html", http.StatusBadRequest)
		case errors.Is(err, errWidgetForbidden):
			http.Error(w, "Widget archive contains a forbidden entry (path traversal, absolute path, symlink or unsupported file type)", http.StatusBadRequest)
		case errors.Is(err, errWidgetInvalid):
			http.Error(w, "Invalid widget archive", http.StatusBadRequest)
		default:
			http.Error(w, "Failed to save widget", http.StatusInternalServerError)
		}
		return
	}

	target := widgetDirectory(slug)
	// Backup-swap: the old widget survives a failed rename.
	backup := target + ".bak"
	_ = os.RemoveAll(backup)
	if _, serr := os.Lstat(target); serr == nil {
		if err = os.Rename(target, backup); err != nil {
			logger.Log(logger.Fields{"error": err, "slug": slug}).Error("Failed to replace widget directory")
			http.Error(w, "Failed to save widget", http.StatusInternalServerError)
			return
		}
	}
	if err = os.Rename(staging, target); err != nil {
		if rbErr := os.Rename(backup, target); rbErr != nil {
			logger.Log(logger.Fields{"error": rbErr, "slug": slug}).Error("Failed to restore previous widget")
		}
		logger.Log(logger.Fields{"error": err, "slug": slug}).Error("Failed to save widget")
		http.Error(w, "Failed to save widget", http.StatusInternalServerError)
		return
	}
	_ = os.RemoveAll(backup)

	bumpConfigRevision()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":    http.StatusOK,
		"status":  1,
		"message": language.GetValue("txtWidgetUploaded"),
	})
}

// widgetSlugFromFilename derives the widget slug from the uploaded filename
// (base name without extension), or "" when it is not a valid slug.
func widgetSlugFromFilename(filename string) string {
	base := filepath.Base(filename)
	ext := strings.ToLower(filepath.Ext(base))
	if ext != ".zip" && ext != ".html" {
		return ""
	}
	slug := strings.TrimSuffix(base, filepath.Ext(base))
	if !widgetSlugRegex.MatchString(slug) {
		return ""
	}
	return slug
}

// extractWidgetZip validates and extracts a widget archive into destDir.
func extractWidgetZip(file io.ReaderAt, size int64, destDir string) error {
	reader, err := zip.NewReader(file, size)
	if err != nil {
		return errWidgetInvalid
	}
	if len(reader.File) == 0 {
		return errWidgetMissingIndex
	}
	if len(reader.File) > maxWidgetEntries {
		return errWidgetTooManyEntries
	}

	hasIndex := false
	var total int64
	for _, entry := range reader.File {
		name := entry.Name
		if name == "" {
			return errWidgetInvalid
		}
		info := entry.FileInfo()
		if info.IsDir() || strings.HasSuffix(name, "/") {
			continue // directories are created on demand below
		}
		// Reject everything that is not a plain regular file: symlinks,
		// hardlinks, devices and sockets cannot be stored safely here.
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: non-regular entry %q", errWidgetForbidden, name)
		}
		// Zip-slip: no absolute paths and no dot-dot path segments.
		if filepath.IsAbs(name) {
			return fmt.Errorf("%w: absolute path %q", errWidgetForbidden, name)
		}
		for _, part := range strings.Split(name, "/") {
			if part == ".." {
				return fmt.Errorf("%w: path traversal in %q", errWidgetForbidden, name)
			}
		}
		cleanName := filepath.Clean(name)
		// Defense in depth: after cleaning, the target must stay inside destDir.
		destPath := filepath.Join(destDir, cleanName)
		if destPath != destDir && !strings.HasPrefix(destPath, destDir+string(os.PathSeparator)) {
			return fmt.Errorf("%w: entry %q escapes the destination", errWidgetForbidden, name)
		}
		if !widgetExtensions[strings.ToLower(filepath.Ext(cleanName))] {
			return fmt.Errorf("%w: extension not allowed in %q", errWidgetForbidden, name)
		}
		if cleanName == "index.html" {
			hasIndex = true
		}
		if err = writeWidgetEntry(entry, destPath, &total); err != nil {
			return err
		}
	}
	if !hasIndex {
		return errWidgetMissingIndex
	}
	return nil
}

// writeWidgetEntry decompresses one archive entry into destPath, enforcing the
// total decompressed size cap.
func writeWidgetEntry(entry *zip.File, destPath string, total *int64) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return err
	}
	source, err := entry.Open()
	if err != nil {
		return err
	}
	defer func(source io.ReadCloser) {
		if cerr := source.Close(); cerr != nil {
			logger.Log(logger.Fields{"error": cerr}).Error("Failed to close zip entry")
		}
	}(source)

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer func(out *os.File) {
		if cerr := out.Close(); cerr != nil {
			logger.Log(logger.Fields{"error": cerr}).Error("Failed to close widget file")
		}
	}(out)

	// The archive header size is attacker-controlled and can lie; count the
	// real decompressed bytes instead.
	sink := &widgetLimitWriter{w: out, total: total}
	if _, err = io.Copy(sink, source); err != nil {
		return err
	}
	return nil
}

// storeWidgetHtml writes a single-file .html upload as the widget's index.html.
func storeWidgetHtml(file io.Reader, destDir string) error {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}
	out, err := os.Create(filepath.Join(destDir, "index.html"))
	if err != nil {
		return err
	}
	defer func(out *os.File) {
		if cerr := out.Close(); cerr != nil {
			logger.Log(logger.Fields{"error": cerr}).Error("Failed to close widget file")
		}
	}(out)

	var total int64
	sink := &widgetLimitWriter{w: out, total: &total}
	if _, err = io.Copy(sink, file); err != nil {
		return err
	}
	return nil
}

// widgetLimitWriter counts decompressed bytes and fails once the total passes
// maxWidgetDecompressed, so an undersized archive header cannot hide a bomb.
type widgetLimitWriter struct {
	w     io.Writer
	total *int64
}

func (l *widgetLimitWriter) Write(p []byte) (int, error) {
	n, err := l.w.Write(p)
	*l.total += int64(n)
	if err == nil && *l.total > maxWidgetDecompressed {
		return n, errWidgetTooLarge
	}
	return n, err
}
