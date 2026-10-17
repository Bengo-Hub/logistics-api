package handlers

import (
	"github.com/Bengo-Hub/httpware/objectstore"
	"github.com/bengobox/logistics-service/internal/ent"
)

// kycMedia is the shared object storage (httpware/objectstore) holding rider KYC documents (ID
// scan, rider photo, vehicle photos) and proof-of-delivery photos and signatures, all private:
// rows keep the object key and every API response carries a short-lived signed link, so a leaked
// or guessed link stops working. Set once at startup (SetMediaStore); nil leaves stored values as
// they are (tests).
var kycMedia *objectstore.Store

// SetMediaStore wires the storage used for every KYC and proof-of-delivery file.
func SetMediaStore(s *objectstore.Store) { kycMedia = s }

// mediaLink is what a response carries for a stored value: a signed link.
func mediaLink(ref string) string { return kycMedia.URL(ref) }

// mediaRef is what a row keeps for a value a client sends back (a link this API handed out).
func mediaRef(ref string) string { return kycMedia.Normalize(ref) }

func signFleetMemberMedia(m *ent.FleetMember) *ent.FleetMember {
	if m == nil {
		return m
	}
	m.IDPassportAttachment = mediaLink(m.IDPassportAttachment)
	m.RiderPhoto = mediaLink(m.RiderPhoto)
	signVehicleMedia(m.Edges.Vehicle)
	return m
}

func signVehicleMedia(v *ent.Vehicle) *ent.Vehicle {
	if v == nil {
		return v
	}
	v.ImageLicensePlate = mediaLink(v.ImageLicensePlate)
	v.ImageSideView = mediaLink(v.ImageSideView)
	return v
}

// signPoDMedia signs the proof-of-delivery photo and signatures, which are private uploads too.
func signPoDMedia(p *ent.ProofOfDelivery) *ent.ProofOfDelivery {
	if p == nil {
		return p
	}
	p.PhotoURL = mediaLink(p.PhotoURL)
	p.SignatureURL = mediaLink(p.SignatureURL)
	p.ReceivingStaffSignatureURL = mediaLink(p.ReceivingStaffSignatureURL)
	return p
}
