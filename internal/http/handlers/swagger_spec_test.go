package handlers

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// The served OpenAPI spec is hand-written YAML; a stray indent once made it unparseable
// and nobody noticed. Parse it and check the delivery routes are documented.
func TestSwaggerSpecParsesAndDocumentsDeliveryRoutes(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(swaggerSpecYAML, &spec); err != nil {
		t.Fatalf("swagger.yaml does not parse: %v", err)
	}
	for _, p := range []string{
		"/api/v1/{tenant}/zones",
		"/api/v1/{tenant}/zones/coverage",
		"/api/v1/{tenant}/zones/quote",
		"/api/v1/{tenant}/zones/outlets/refresh",
		"/api/v1/{tenant}/delivery-policy",
		"/api/v1/{tenant}/delivery-policy/suggest-fee",
		"/api/v1/s2s/zones/{tenant}/quote",
	} {
		if _, ok := spec.Paths[p]; !ok {
			t.Errorf("%s is not documented", p)
		}
	}
}
