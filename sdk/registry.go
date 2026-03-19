package sdk

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

// WithDefaultRelayURLs fetches the default Portal relay registry and appends
// any explicit relay inputs before normalization.
func WithDefaultRelayURLs(ctx context.Context, registryURL string, explicit ...string) []string {
	if registryURL == "" {
		registryURL = types.PortalRelayRegistryURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, registryURL, nil)
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
