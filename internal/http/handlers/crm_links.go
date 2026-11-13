package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/tenant"
	"github.com/bengobox/logistics-service/internal/modules/tasks"
)

// errTenantNotFound is what the tenant lookup answers for a tenant logistics does not know.
var errTenantNotFound = errors.New("tenant not found")

// CRMLinksHandler tells marketflow whether logistics still links a CRM contact, so marketflow can
// decide between deleting an erased person's contact and stripping it to a minimal record.
// Logistics keeps a CRM contact id in one place only: delivery task metadata (crm_contact_id,
// next to the customer_name, customer_phone and customer_email copies the source order supplied).
type CRMLinksHandler struct {
	tenantID   func(ctx context.Context, raw string) (uuid.UUID, error)
	referenced func(ctx context.Context, contactID uuid.UUID) (bool, error)
	log        *zap.Logger
}

// NewCRMLinksHandler builds the handler over the tenants and tasks tables. The route that mounts
// it carries the service key check (requireServiceKey), as the other S2S routes do.
func NewCRMLinksHandler(db *ent.Client, log *zap.Logger) *CRMLinksHandler {
	if log == nil {
		log = zap.NewNop()
	}
	return &CRMLinksHandler{
		tenantID: func(ctx context.Context, raw string) (uuid.UUID, error) {
			pred := tenant.Slug(raw)
			if id, err := uuid.Parse(raw); err == nil {
				pred = tenant.ID(id)
			}
			t, err := db.Tenant.Query().Where(pred).Only(ctx)
			if ent.IsNotFound(err) {
				return uuid.Nil, errTenantNotFound
			}
			if err != nil {
				return uuid.Nil, err
			}
			return t.ID, nil
		},
		referenced: func(ctx context.Context, contactID uuid.UUID) (bool, error) {
			return TaskReferencesContact(ctx, db, contactID)
		},
		log: log.Named("crm_links"),
	}
}

// TaskReferencesContact reports whether any task, in any tenant, keeps the CRM contact id in its
// metadata. CRM contact ids are random UUIDs minted by marketflow, so a match anywhere is this
// person, and a task held under a drifted tenant id can only answer "referenced", which keeps the
// contact. The id is matched case-insensitively because the metadata keeps the text as the source
// order sent it.
func TaskReferencesContact(ctx context.Context, db *ent.Client, contactID uuid.UUID) (bool, error) {
	return db.Task.Query().Where(tasks.MetadataContact(contactID)).Exist(ctx)
}

// Referenced answers whether any task, in any tenant, carries the contact id. {tenant} is the
// tenant UUID or slug and must be known here: an unknown one answers 404, never "not referenced",
// so marketflow keeps the contact. The answer is the flag only: no task ids, names or phones.
// GET /api/v1/s2s/{tenant}/crm-links/{contactID} (X-API-Key) -> {"referenced": bool}
func (h *CRMLinksHandler) Referenced(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	contactID, err := uuid.Parse(chi.URLParam(r, "contactID"))
	if err != nil || contactID == uuid.Nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "contact id must be a UUID", "code": "invalid_contact_id"})
		return
	}
	tenantID, err := h.tenantID(ctx, chi.URLParam(r, "tenant"))
	if errors.Is(err, errTenantNotFound) {
		respondJSON(w, http.StatusNotFound, map[string]string{"error": "tenant not found", "code": "tenant_not_found"})
		return
	}
	if err != nil {
		h.log.Error("crm links: tenant lookup failed", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to check links"})
		return
	}
	found, err := h.referenced(ctx, contactID)
	if err != nil {
		// Ids only in the log, never the person's details.
		h.log.Error("crm links check failed", zap.String("tenant_id", tenantID.String()),
			zap.String("contact_id", contactID.String()), zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to check links"})
		return
	}
	respondJSON(w, http.StatusOK, map[string]bool{"referenced": found})
}
