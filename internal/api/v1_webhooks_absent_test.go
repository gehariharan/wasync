package api

import (
	"strings"
	"testing"
)

// Webhook management is human-only: no versioned route, whatever its scope,
// may expose or modify webhook endpoints, secrets or dead letters.
func TestNoV1RouteManagesWebhooks(t *testing.T) {
	for _, route := range v1Routes {
		if strings.Contains(strings.ToLower(route.Pattern), "webhook") {
			t.Errorf("route %s exposes webhooks over the API", route.Pattern)
		}
	}
}
