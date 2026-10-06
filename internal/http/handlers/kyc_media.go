package handlers

import (
	"github.com/Bengo-Hub/httpware"
	"github.com/bengobox/logistics-service/internal/ent"
)

// kycSigner signs the rider KYC document URLs (ID scan, rider photo, vehicle photos) that the
// API returns. The files are private media: /media serves them only with a valid, unexpired
// signature, so a leaked or guessed URL stops working. Set once at startup (SetMediaSigner);
// nil leaves URLs unsigned (tests).
var kycSigner *httpware.MediaSigner

// SetMediaSigner wires the signer used for every KYC URL in API responses.
func SetMediaSigner(s *httpware.MediaSigner) { kycSigner = s }

func signFleetMemberMedia(m *ent.FleetMember) *ent.FleetMember {
	if m == nil {
		return m
	}
	m.IDPassportAttachment = kycSigner.Sign(m.IDPassportAttachment)
	m.RiderPhoto = kycSigner.Sign(m.RiderPhoto)
	signVehicleMedia(m.Edges.Vehicle)
	return m
}

func signVehicleMedia(v *ent.Vehicle) *ent.Vehicle {
	if v == nil {
		return v
	}
	v.ImageLicensePlate = kycSigner.Sign(v.ImageLicensePlate)
	v.ImageSideView = kycSigner.Sign(v.ImageSideView)
	return v
}

// signPoDMedia signs the proof-of-delivery photo and signature, which are private uploads too.
func signPoDMedia(p *ent.ProofOfDelivery) *ent.ProofOfDelivery {
	if p == nil {
		return p
	}
	p.PhotoURL = kycSigner.Sign(p.PhotoURL)
	p.SignatureURL = kycSigner.Sign(p.SignatureURL)
	return p
}