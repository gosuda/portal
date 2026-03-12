package sdk

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/gosuda/portal/v2/utils"
)

const PortalRelayRegistryURL = "https://raw.githubusercontent.com/gosuda/portal/main/registry.json"

// WithDefaultRelayURLs fetches the default Portal relay registry and appends
// any explicit relay inputs before normalization.
func WithDefaultRelayURLs(ctx context.Context, explicit ...string) []string {
	if ctx == nil {
		ctx = context.Background()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, PortalRelayRegistryURL, nil)
	if err != nil {
		return explicit
	}

	client := &http.Client{Timeout: defaultRequestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return explicit
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return explicit
	}

	var registry struct {
		Relays []string `json:"relays"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&registry); err != nil {
		return explicit
	}

	relayURLs := append(registry.Relays, explicit...)
	if len(relayURLs) == 0 {
		return nil
	}
	relayURLs, err = utils.NormalizeRelayURLs(relayURLs)
	if err != nil {
		return nil
	}

	return relayURLs
}
