package handlers

import (
	"net/http"
	"path"

	"github.com/Bengo-Hub/httpware/objectstore"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	"go.uber.org/zap"
)

// RegisterMediaTypes sets logistics' KYC rules on the shared registry (5 MB; JPEG, PNG, WebP or
// PDF; private). Call once at startup.
func RegisterMediaTypes() {
	objectstore.Register(objectstore.MediaType{
		Name: objectstore.TypeKYC, MaxBytes: 5 << 20, Image: true,
		Allowed: []string{"image/jpeg", "image/png", "image/webp", "application/pdf"},
	})
}

// MediaHandler stores rider KYC documents and proof-of-delivery photos privately.
type MediaHandler struct {
	log *zap.Logger
}

// NewMediaHandler creates the upload handler (storage: SetMediaStore).
func NewMediaHandler(log *zap.Logger) *MediaHandler {
	return &MediaHandler{log: log.Named("media")}
}

// Upload stores one KYC or proof-of-delivery file for the caller's tenant: JPEG, PNG, WebP or PDF
// up to 5 MB, checked from the bytes, images re-encoded. It answers a signed link to preview it;
// sending that link back on a profile or proof of delivery stores the key. Requires a signed-in
// user of a tenant.
//
// @Summary Upload a KYC or proof-of-delivery file
// @Tags Media
// @Accept multipart/form-data
// @Produce json
// @Param file formData file true "JPEG, PNG, WebP or PDF, up to 5 MB"
// @Success 201 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Router /api/v1/media/upload [post]
func (h *MediaHandler) Upload(w http.ResponseWriter, r *http.Request) {
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok || claims.TenantID == "" {
		http.Error(w, "Sign in to an organisation to upload", http.StatusUnauthorized)
		return
	}
	if !kycMedia.Enabled() {
		http.Error(w, "File storage is not configured", http.StatusServiceUnavailable)
		return
	}
	up, err := objectstore.ReadUpload(w, r, "file", objectstore.TypeKYC)
	if err != nil {
		if objectstore.IsUploadError(err) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, "Failed to read the upload", http.StatusInternalServerError)
		return
	}
	key, err := kycMedia.NewKey(objectstore.Owner{TenantID: claims.TenantID}, objectstore.TypeKYC, up.Ext)
	if err != nil {
		http.Error(w, "Invalid organisation", http.StatusBadRequest)
		return
	}
	if err := kycMedia.Put(r.Context(), key, up.Data, up.ContentType); err != nil {
		h.log.Error("kyc upload not stored", zap.Error(err))
		http.Error(w, "File storage is unavailable", http.StatusBadGateway)
		return
	}
	respondJSON(w, http.StatusCreated, map[string]string{"url": mediaLink(key), "filename": path.Base(key)})
}
