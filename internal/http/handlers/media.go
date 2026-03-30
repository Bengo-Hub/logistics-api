package handlers

import (
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/config"
)

type MediaHandler struct {
	log *zap.Logger
	cfg *config.Config
}

func NewMediaHandler(log *zap.Logger, cfg *config.Config) *MediaHandler {
	return &MediaHandler{
		log: log,
		cfg: cfg,
	}
}

// Upload handles file uploads for KYC documents.
// It validates file size, extension, and saves the file to the media root.
func (h *MediaHandler) Upload(w http.ResponseWriter, r *http.Request) {
	// 1. Limit upload size (5MB as per ERD)
	r.Body = http.MaxBytesReader(w, r.Body, 5*1024*1024)
	if err := r.ParseMultipartForm(5 * 1024 * 1024); err != nil {
		h.log.Error("failed to parse multipart form", zap.Error(err))
		http.Error(w, "File too large (max 5MB)", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		h.log.Error("failed to get file from form", zap.Error(err))
		http.Error(w, "Invalid file upload", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// 2. Detect Content Type
	buffer := make([]byte, 512)
	n, _ := file.Read(buffer)
	contentType := http.DetectContentType(buffer[:n])
	file.Seek(0, 0) // Reset after detection

	allowedTypes := map[string]bool{
		"image/jpeg":      true,
		"image/jpg":       true,
		"image/png":       true,
		"image/webp":      true,
		"application/pdf": true,
	}

	if !allowedTypes[contentType] {
		http.Error(w, "Invalid file type. Supported: JPEG, JPG, PNG, WEBP, PDF", http.StatusBadRequest)
		return
	}

	// 3. Generate unique filename
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext == "" {
		switch contentType {
		case "image/jpeg":
			ext = ".jpg"
		case "image/png":
			ext = ".png"
		case "image/webp":
			ext = ".webp"
		case "application/pdf":
			ext = ".pdf"
		}
	}
	filename := fmt.Sprintf("%s_%d%s", uuid.New().String(), time.Now().Unix(), ext)

	// Create subdirectory for kyc
	dir := filepath.Join(h.cfg.Media.Root, "uploads", "kyc")
	if err := os.MkdirAll(dir, 0755); err != nil {
		h.log.Error("failed to create upload directory", zap.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	dstPath := filepath.Join(dir, filename)
	dst, err := os.Create(dstPath)
	if err != nil {
		h.log.Error("failed to create destination file", zap.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	// 4. Sanitize and Compress (Re-encode supported images)
	if contentType == "image/jpeg" || contentType == "image/png" {
		img, _, err := image.Decode(file)
		if err == nil {
			if contentType == "image/jpeg" {
				err = jpeg.Encode(dst, img, &jpeg.Options{Quality: 80}) // Slightly more compression for KYC docs
			} else {
				err = png.Encode(dst, img)
			}
			if err == nil {
				goto response
			}
			h.log.Warn("re-encoding failed, falling back to direct copy", zap.Error(err))
			file.Seek(0, 0)
			dst.Seek(0, 0)
			dst.Truncate(0)
		}
	}

	if _, err := io.Copy(dst, file); err != nil {
		h.log.Error("failed to copy file", zap.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

response:

	// 4. Return URL
	// Note: In production, URLBase is defined in values.yaml
	url := fmt.Sprintf("%s/media/uploads/kyc/%s", h.cfg.Media.URLBase, filename)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, `{"url": "%s", "filename": "%s"}`, url, filename)
}
