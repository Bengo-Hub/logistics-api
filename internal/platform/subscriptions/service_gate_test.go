package subscriptions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConsumerUsesService(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/none/"):
			w.WriteHeader(http.StatusNotFound)
		case strings.Contains(r.URL.Path, "/exempt/"):
			_, _ = w.Write([]byte(`{"billing_mode":"exempt"}`))
		case strings.Contains(r.URL.Path, "/shop/"):
			_, _ = w.Write([]byte(`{"billing_mode":"recurring","active_service_tags":["pos","logistics"]}`))
		case strings.Contains(r.URL.Path, "/library/"):
			_, _ = w.Write([]byte(`{"billing_mode":"one_time","active_service_tags":["library"]}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	c := NewClient(Config{ServiceURL: srv.URL, RequestTimeout: time.Second})
	ctx := context.Background()
	cases := map[string]bool{"none": false, "exempt": true, "shop": true, "library": false, "down": true}
	for tenant, want := range cases {
		if got := c.ConsumerUsesService(ctx, tenant, "logistics"); got != want {
			t.Errorf("%s: got %v, want %v", tenant, got, want)
		}
	}
}
