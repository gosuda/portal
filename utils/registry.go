package utils

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gosuda/portal/v2/types"
)

func ResolvePortalRelayURLs(ctx context.Context, explicit []string, includeDefaults bool) ([]string, error) {
	explicit, err := NormalizeRelayURLs(explicit...)
	if err != nil {
		return nil, err
	}
	if !includeDefaults {
		return explicit, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, types.PortalRelayRegistryURL, nil)
	if err != nil {
		return explicit, nil
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return explicit, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return explicit, nil
	}

	var registry struct {
		Relays []string `json:"relays"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&registry); err != nil {
		return explicit, nil
	}

	defaults, err := NormalizeRelayURLs(registry.Relays...)
	if err != nil {
		return explicit, nil
	}
	if len(defaults) == 0 {
		return explicit, nil
	}
	return MergeRelayURLs(defaults, nil, explicit)
}
