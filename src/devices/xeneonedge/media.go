package xeneonedge

// Package: CORSAIR XENEON EDGE
// Author: Nikola Jurkovic
// License: GPL-3.0 or later

import (
	"OpenLinkHub/src/config"
	"OpenLinkHub/src/language"
	"OpenLinkHub/src/logger"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const maxMediaUploadSize = 256 * 1024 * 1024 // 256MB

var (
	mediaNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

	// mediaExtensions maps allowed file extensions to accepted sniffed MIME types.
	// Container formats the standard library cannot reliably sniff also accept
	// application/octet-stream.
	mediaExtensions = map[string][]string{
		".jpg":  {"image/jpeg"},
		".jpeg": {"image/jpeg"},
		".png":  {"image/png"},
		".gif":  {"image/gif"},
		".bmp":  {"image/bmp"},
		".webp": {"image/webp"},
		".ico":  {"image/x-icon", "image/vnd.microsoft.icon"},
		".mp4":  {"video/mp4", "application/octet-stream"},
		".webm": {"video/webm"},
		".mov":  {"video/quicktime", "application/octet-stream"},
		".avi":  {"video/avi", "video/x-msvideo", "application/octet-stream"},
		".mpeg": {"video/mpeg", "application/octet-stream"},
	}
)

// mediaDirectory will return the media library location
func mediaDirectory() string {
	return config.GetConfig().ConfigPath + "/database/xeneon/media/"
}

// IsValidMediaFile will validate a media library filename
func IsValidMediaFile(filename string) bool {
	if filename != filepath.Base(filename) {
		return false
	}
	ext := strings.ToLower(filepath.Ext(filename))
	name := strings.TrimSuffix(filename, filepath.Ext(filename))
	if _, ok := mediaExtensions[ext]; !ok {
		return false
	}
	return mediaNameRegex.MatchString(name)
}

// GetMediaFiles will return all media library filenames
func GetMediaFiles() []string {
	files := make([]string, 0)
	entries, err := os.ReadDir(mediaDirectory())
	if err != nil {
		return files
	}
	for _, entry := range entries {
		if entry.IsDir() || !IsValidMediaFile(entry.Name()) {
			continue
		}
		files = append(files, entry.Name())
	}
	sort.Strings(files)
	return files
}

// DeleteMediaFile will remove a file from the media library
func DeleteMediaFile(filename string) uint8 {
	if !IsValidMediaFile(filename) {
		return 0
	}
	if err := os.Remove(filepath.Join(mediaDirectory(), filename)); err != nil {
		logger.Log(logger.Fields{"error": err, "file": filename}).Error("Unable to delete media file")
		return 0
	}
	bumpConfigRevision()
	return 1
}

// PerformMediaServe will serve a media library file
func PerformMediaServe(w http.ResponseWriter, r *http.Request) {
	filename := strings.TrimPrefix(r.URL.Path, "/api/xeneon/media/")
	if !IsValidMediaFile(filename) {
		http.Error(w, "Invalid media file", http.StatusBadRequest)
		return
	}
	http.ServeFile(w, r, filepath.Join(mediaDirectory(), filename))
}

// PerformMediaUpload will save an uploaded image or video into the media library
func PerformMediaUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxMediaUploadSize)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		logger.Log(logger.Fields{"error": err}).Error("File too large or invalid upload")
		http.Error(w, "File too large or invalid upload", http.StatusBadRequest)
		return
	}

	file, handler, err := r.FormFile("mediaFile")
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Failed to read uploaded file")
		http.Error(w, "Failed to read uploaded file", http.StatusBadRequest)
		return
	}
	defer func(file multipart.File) {
		if cerr := file.Close(); cerr != nil {
			logger.Log(logger.Fields{"error": cerr}).Error("Failed to close file")
		}
	}(file)

	filename := filepath.Base(handler.Filename)
	if !IsValidMediaFile(filename) {
		http.Error(w, "Invalid filename. Letters, numbers, dash and underscore only", http.StatusBadRequest)
		return
	}

	header := make([]byte, 512)
	// io.ReadFull fills the sniff buffer even when the reader returns it in chunks;
	// a short file (ErrUnexpectedEOF) or empty file (EOF) is fine — sniff what we got.
	n, err := io.ReadFull(file, header)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		logger.Log(logger.Fields{"error": err}).Error("Unable to inspect file")
		http.Error(w, "Unable to inspect file", http.StatusBadRequest)
		return
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Seek failed")
		http.Error(w, "Unable to inspect file", http.StatusBadRequest)
		return
	}

	ext := strings.ToLower(filepath.Ext(filename))
	detected := http.DetectContentType(header[:n])
	detected = strings.Split(detected, ";")[0]
	valid := false
	for _, mime := range mediaExtensions[ext] {
		if detected == mime {
			valid = true
			break
		}
	}
	if !valid {
		logger.Log(logger.Fields{"detected": detected, "ext": ext}).Error("MIME mismatch")
		http.Error(w, "Invalid file content for extension", http.StatusBadRequest)
		return
	}

	if err = os.MkdirAll(mediaDirectory(), 0755); err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Failed to create media directory")
		http.Error(w, "Failed to save file", http.StatusInternalServerError)
		return
	}

	out, err := os.Create(filepath.Join(mediaDirectory(), filename))
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Failed to save file")
		http.Error(w, "Failed to save file", http.StatusInternalServerError)
		return
	}
	defer func(out *os.File) {
		if cerr := out.Close(); cerr != nil {
			logger.Log(logger.Fields{"error": cerr}).Error("Failed to close file")
		}
	}(out)

	if _, err = io.Copy(out, file); err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Failed to write file")
		http.Error(w, "Failed to write file", http.StatusInternalServerError)
		return
	}

	bumpConfigRevision()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":    http.StatusOK,
		"status":  1,
		"message": language.GetValue("txtMediaUploaded"),
	})
}
