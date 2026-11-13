package marketflow

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

// GetContact reads one contact by id from marketflow's S2S route, in the given tenant.
func TestGetContact(t *testing.T) {
	tenant, known := uuid.New(), uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "key" || r.URL.Query().Get("tenant_id") != tenant.String() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/internal/contacts/" + known.String():
			_, _ = w.Write([]byte(`{"id":"` + known.String() + `","first_name":"Amina","last_name":"Otieno","phone":"+254712000111"}`))
		case "/api/v1/internal/contacts/" + uuid.Nil.String():
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "key")
	ctx := context.Background()

	got, err := c.GetContact(ctx, tenant, known)
	if err != nil || got.ID != known || got.Name() != "Amina Otieno" || got.Phone != "+254712000111" {
		t.Fatalf("known contact: %+v %v", got, err)
	}
	if _, err := c.GetContact(ctx, tenant, uuid.New()); !errors.Is(err, ErrContactNotFound) {
		t.Fatalf("unknown contact: %v, want ErrContactNotFound", err)
	}
	if _, err := c.GetContact(ctx, tenant, uuid.Nil); err == nil || errors.Is(err, ErrContactNotFound) {
		t.Fatalf("a server error must be a plain error: %v", err)
	}
	if _, err := NewClient("", "").GetContact(ctx, tenant, known); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("disabled client: %v", err)
	}
}

// An erased contact is the minimal record marketflow keeps after an erasure: first name "Erased"
// and no last name, email or phone. A live contact always has an email or phone.
func TestContactErased(t *testing.T) {
	cases := []struct {
		c    Contact
		want bool
	}{
		{Contact{FirstName: "Erased"}, true},
		{Contact{FirstName: " Erased "}, true},
		{Contact{FirstName: "Erased", Email: "a@example.com"}, false},
		{Contact{FirstName: "Erased", Phone: "+254700000000"}, false},
		{Contact{FirstName: "Erased", LastName: "Smith"}, false},
		{Contact{FirstName: "Jane", Email: "jane@example.com"}, false},
		{Contact{}, false},
	}
	for _, c := range cases {
		if got := c.c.Erased(); got != c.want {
			t.Errorf("%+v.Erased() = %v, want %v", c.c, got, c.want)
		}
	}
}
