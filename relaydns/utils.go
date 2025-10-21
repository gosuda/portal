package relaydns

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/rs/zerolog/log"
)

func AddrToTarget(listen string) string {
	if len(listen) > 0 && listen[0] == ':' {
		return "127.0.0.1" + listen
	}
	return listen
}

func BuildAddrs(h host.Host) []string {
	out := make([]string, 0)
	for _, a := range h.Addrs() {
		out = append(out, fmt.Sprintf("%s/p2p/%s", a.String(), h.ID().String()))
	}
	return out
}

func sortMultiaddrs(addrs []string, preferQUIC, preferLocal bool) {
	score := func(a string) int {
		sc := 0
		if preferQUIC && strings.Contains(a, "/quic-v1") {
			sc += 2
		}
		if preferLocal && (strings.Contains(a, "/ip4/127.0.0.1/") || strings.Contains(a, "/ip6/::1/")) {
			sc += 1
		}
		return sc
	}
	sort.SliceStable(addrs, func(i, j int) bool { return score(addrs[i]) > score(addrs[j]) })
}

func RemoveDuplicate(ss []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func fetchMultiaddrsFromHosts(base string, timeout time.Duration) ([]string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("parse server-url: %w", err)
	}
	// ensure path ends with /hosts
	if !strings.HasSuffix(u.Path, "/hosts") {
		if u.Path == "" || u.Path == "/" {
			u.Path = "/hosts"
		} else {
			u.Path = strings.TrimSuffix(u.Path, "/") + "/hosts"
		}
	}
	client := &http.Client{Timeout: timeout}
	req, _ := http.NewRequest(http.MethodGet, u.String(), nil)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Debug().Err(err).Msg("response body close")
		}
	}()

	var payload Hosts
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	addrs := make([]string, 0, len(payload.ServerAddrs))
	for _, s := range payload.ServerAddrs {
		// sanity check
		if strings.Contains(s, "/p2p/") && (strings.Contains(s, "/ip4/") || strings.Contains(s, "/ip6/")) {
			addrs = append(addrs, s)
		}
	}
	return addrs, nil
}
