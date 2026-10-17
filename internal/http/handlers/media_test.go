package handlers

import (
	"bytes"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Bengo-Hub/httpware"
	"github.com/Bengo-Hub/httpware/objectstore"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	"go.uber.org/zap"
)

func kycUpload(t *testing.T, claims *authclient.Claims) *http.Request {
	t.Helper()
	var pic bytes.Buffer
	_ = png.Encode(&pic, image.NewNRGBA(image.Rect(0, 0, 5, 5)))
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "id.png")
	_, _ = fw.Write(pic.Bytes())
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/media/upload", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	if claims != nil {
		r = r.WithContext(authclient.ContextWithClaims(r.Context(), claims))
	}
	return r
}

// KYC uploads need a tenant user, are stored privately under the tenant and come back as a signed
// link that normalises to the key; reads sign the key again.
func TestKYCUploadPrivate(t *testing.T) {
	RegisterMediaTypes()
	store, err := objectstore.New("logistics", objectstore.Config{LocalRoot: t.TempDir(), LocalURLBase: "http://localhost:4005"},
		objectstore.WithLocalSigner(httpware.NewMediaSigner("secret", time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	SetMediaStore(store)
	t.Cleanup(func() { SetMediaStore(nil) })
	h := NewMediaHandler(zap.NewNop())

	rec := httptest.NewRecorder()
	h.Upload(rec, kycUpload(t, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous upload: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.Upload(rec, kycUpload(t, &authclient.Claims{TenantID: "t1"}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/media/logistics/t1/kyc/") || !strings.Contains(body, "sig=") {
		t.Fatalf("expected a signed private link: %s", body)
	}
	start := strings.Index(body, "http")
	link := body[start : start+strings.Index(body[start:], `"`)]
	link = strings.ReplaceAll(link, `&`, "&")
	key := mediaRef(link)
	if !strings.HasPrefix(key, "logistics/t1/kyc/") {
		t.Fatalf("link must store as the key: %q", key)
	}
	if !strings.Contains(mediaLink(key), "sig=") {
		t.Error("reads must sign the key")
	}
}
