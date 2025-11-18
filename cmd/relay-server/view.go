package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/oschwald/geoip2-golang"
	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/utils/wsstream"
	"gosuda.org/portal/sdk"
)

//go:embed static
var assetsFS embed.FS

//go:embed static/GeoLite2-Country.mmdb
var geoipFS embed.FS

// GeoIP database reader (global instance with lazy initialization)
var (
	geoipReader     *geoip2.Reader
	geoipReaderOnce sync.Once
	geoipReaderErr  error
)

func serveAsset(mux *http.ServeMux, route, assetPath, contentType string) {
	mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
		b, err := assetsFS.ReadFile(assetPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
	})
}

// serveHTTP builds the HTTP mux and returns the server.
func serveHTTP(_ context.Context, addr string, serv *portal.RelayServer, nodeID string, bootstraps []string, cancel context.CancelFunc) *http.Server {
	if addr == "" {
		addr = ":0"
	}

	// Create app UI mux
	appMux := http.NewServeMux()

	// Serve embedded favicons (ico/png/svg) for portal UI
	serveAsset(appMux, "/favicon.ico", "static/favicon/favicon.ico", "image/x-icon")
	serveAsset(appMux, "/favicon.png", "static/favicon/favicon.png", "image/png")
	serveAsset(appMux, "/favicon.svg", "static/favicon/favicon.svg", "image/svg+xml")

	// Portal app assets (JS, CSS, etc.) - served from /app/
	appMux.HandleFunc("/app/", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/app/")
		serveAppStatic(w, r, path, serv)
	})

		// Portal frontend files (for unified caching)
	appMux.HandleFunc("/frontend/", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/frontend/")

		// Special handling for manifest.json - generate dynamically
		if path == "manifest.json" {
			serveDynamicManifest(w)
			return
		}

		servePortalStaticFile(w, r, path)
	})

	appMux.HandleFunc("/relay", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		wsConn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Error().Err(err).Msg("[server] websocket upgrade failed")
			return
		}

		stream := &wsstream.WsStream{Conn: wsConn}
		if err := serv.HandleConnection(stream, r.RemoteAddr); err != nil {
			log.Error().Err(err).Msg("[server] websocket relay connection error")
			wsConn.Close()
			return
		}
	})

	// App UI index page - serve React frontend with SSR (delegates to serveAppStatic)
	appMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// serveAppStatic handles both "/" and 404 fallback with SSR
		path := strings.TrimPrefix(r.URL.Path, "/")
		serveAppStatic(w, r, path, serv)
	})

	appMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{\"status\":\"ok\"}"))
	})

	// Create portal frontend mux
	portalMux := createPortalMux()

	// Top-level handler that routes based on host and path
	topHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPortalSubdomain(r.Host) {
			portalMux.ServeHTTP(w, r)
		} else {
			appMux.ServeHTTP(w, r)
		}
	})

	srv := &http.Server{
		Addr:    addr,
		Handler: topHandler,
	}

	go func() {
		log.Info().Msgf("[server] http: %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("[server] http error")
			cancel()
		}
	}()

	return srv
}

type leaseRow struct {
	Peer        string
	Name        string
	Kind        string
	Connected   bool
	DNS         string
	LastSeen    string
	LastSeenISO string
	TTL         string
	Link        string
	StaleRed    bool
	Hide        bool
	Metadata    string
	Region      string
	CountryCode string
}

// initGeoIP initializes the GeoIP database reader (lazy loaded)
func initGeoIP() error {
	geoipReaderOnce.Do(func() {
		// Try to load from embedded FS first
		data, err := geoipFS.ReadFile("static/GeoLite2-Country.mmdb")
		if err == nil {
			reader, err := geoip2.FromBytes(data)
			if err == nil {
				geoipReader = reader
				log.Info().
					Str("source", "embedded").
					Int("size", len(data)).
					Msg("GeoIP database loaded successfully from embedded FS")
				return
			}
			log.Warn().Err(err).Msg("Failed to parse embedded GeoIP database")
		} else {
			log.Debug().Err(err).Msg("Embedded GeoIP database not found, trying file paths")
		}
	})

	return geoipReaderErr
}

// getRegionFromIP extracts region information from an IP address
func getRegionFromIP(ipStr string) (region, countryCode string) {
	// Initialize GeoIP if needed
	if err := initGeoIP(); err != nil {
		return "unknown", ""
	}

	// Parse IP address
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return "unknown", ""
	}

	// Skip private/local IPs
	if ip.IsLoopback() || ip.IsPrivate() {
		log.Debug().Str("ip", ipStr).Msg("[GeoIP] Skipping local/private IP")
		return "local", ""
	}

	// Lookup country record
	record, err := geoipReader.Country(ip)
	if err != nil {
		log.Debug().Err(err).Str("ip", ipStr).Msg("GeoIP lookup failed")
		return "unknown", ""
	}

	// Extract country code
	countryCode = record.Country.IsoCode

	// Map country code to region
	// Based on common regional groupings used in gaming/CDN services
	switch countryCode {
	// North America
	case "US", "CA", "MX", "GT", "HN", "SV", "NI", "CR", "PA", "BZ":
		region = "us-east"

	// South America
	case "BR", "AR", "CL", "CO", "PE", "VE", "EC", "BO", "PY", "UY", "GY", "SR", "GF":
		region = "south-america"

	// Europe - West
	case "GB", "IE", "PT", "ES", "FR", "BE", "NL", "LU":
		region = "eu-west"

	// Europe - Central/East
	case "DE", "AT", "CH", "IT", "PL", "CZ", "SK", "HU", "RO", "BG", "SI", "HR", "BA", "RS", "ME", "AL", "MK", "GR", "CY":
		region = "eu-central"

	// Europe - North
	case "SE", "NO", "DK", "FI", "IS", "EE", "LV", "LT":
		region = "eu-west"

	// Asia - East
	case "JP", "KR", "CN", "TW", "HK", "MO":
		region = "asia-pacific"

	// Asia - Southeast
	case "SG", "MY", "TH", "ID", "PH", "VN", "LA", "KH", "MM", "BN":
		region = "asia-pacific"

	// Asia - South
	case "IN", "PK", "BD", "LK", "NP", "BT", "MV":
		region = "asia-pacific"

	// Oceania
	case "AU", "NZ", "FJ", "PG", "NC", "PF", "SB", "VU", "WS", "TO":
		region = "asia-pacific"

	// Middle East
	case "AE", "SA", "IL", "TR", "EG", "IQ", "IR", "JO", "LB", "SY", "YE", "OM", "KW", "BH", "QA":
		region = "eu-central"

	// Africa
	case "ZA", "NG", "KE", "GH", "TZ", "UG", "ET", "MA", "DZ", "TN", "LY", "SD", "AO", "MZ", "ZW", "BW", "NA", "ZM", "MW", "MG":
		region = "eu-west"

	default:
		region = "unknown"
	}

	log.Debug().
		Str("ip", ipStr).
		Str("region", region).
		Str("country", countryCode).
		Msg("[GeoIP] Region mapped successfully")

	return region, countryCode
}

// convertLeaseEntriesToRows converts LeaseEntry data from LeaseManager to leaseRow format for the app page
func convertLeaseEntriesToRows(serv *portal.RelayServer) []leaseRow {
	// Get all lease entries directly from the lease manager
	leaseEntries := serv.GetAllLeaseEntries()

	// Initialize with empty slice instead of nil to avoid "null" in JSON
	rows := []leaseRow{}
	now := time.Now()

	for _, leaseEntry := range leaseEntries {
		// Check if lease is still valid
		if now.After(leaseEntry.Expires) {
			continue
		}

		lease := leaseEntry.Lease
		identityID := string(lease.Identity.Id)

		// Metadata parsing
		var meta sdk.Metadata
		_ = json.Unmarshal([]byte(lease.Metadata), &meta)
		if meta.Hide {
			continue
		}

		// Calculate TTL
		ttl := time.Until(leaseEntry.Expires)
		ttlStr := ""
		if ttl > 0 {
			if ttl > time.Hour {
				ttlStr = fmt.Sprintf("%.0fh", ttl.Hours())
			} else if ttl > time.Minute {
				ttlStr = fmt.Sprintf("%.0fm", ttl.Minutes())
			} else {
				ttlStr = fmt.Sprintf("%.0fs", ttl.Seconds())
			}
		}

		// Format last active as relative time (e.g., "1h 4m", "12m 5s", "8s")
		since := now.Sub(leaseEntry.LastSeen)
		if since < 0 {
			since = 0
		}
		lastSeenStr := func(d time.Duration) string {
			if d >= time.Hour {
				h := int(d / time.Hour)
				m := int((d % time.Hour) / time.Minute)
				if m > 0 {
					return fmt.Sprintf("%dh %dm", h, m)
				}
				return fmt.Sprintf("%dh", h)
			}
			if d >= time.Minute {
				m := int(d / time.Minute)
				s := int((d % time.Minute) / time.Second)
				if s > 0 {
					return fmt.Sprintf("%dm %ds", m, s)
				}
				return fmt.Sprintf("%dm", m)
			}
			s := int(d / time.Second)
			return fmt.Sprintf("%ds", s)
		}(since)
		lastSeenISO := leaseEntry.LastSeen.UTC().Format(time.RFC3339)

		// Check if connection is still active
		connected := serv.IsConnectionActive(leaseEntry.ConnectionID)

		// Skip entries that have been disconnected for 3 minutes or more
		if !connected && since >= 3*time.Minute {
			continue
		}

		// Use name from lease if available
		name := lease.Name
		if name == "" {
			name = "(unnamed)"
		}

		// Determine kind/type based on ALPN if available
		kind := "client"
		if len(lease.Alpn) > 0 {
			kind = lease.Alpn[0]
		}

		// Create DNS label from identity (first 8 chars for display)
		dnsLabel := identityID
		if len(dnsLabel) > 8 {
			dnsLabel = dnsLabel[:8] + "..."
		}

		// Use frontend pattern if available, otherwise fall back to portalHost
		var link string
		if portalFrontendPattern != "" {
			// For wildcard patterns like *.localhost:4017, replace * with lease name
			if strings.HasPrefix(portalFrontendPattern, "*.") {
				link = fmt.Sprintf("//%s%s", lease.Name, strings.TrimPrefix(portalFrontendPattern, "*"))
			} else {
				// For non-wildcard patterns, construct URL with lease name as subdomain
				link = fmt.Sprintf("//%s.%s/", lease.Name, portalFrontendPattern)
			}
		} else {
			link = fmt.Sprintf("//%s.%s/", lease.Name, portalHost)
		}

		// Get GeoIP information from RemoteAddr
		var region string
		var countryCode string
		if leaseEntry.RemoteAddr != "" {
			// Extract IP from RemoteAddr (format: "ip:port")
			ipStr := leaseEntry.RemoteAddr
			if idx := strings.LastIndex(ipStr, ":"); idx != -1 {
				ipStr = ipStr[:idx]
			}
			log.Info().
				Str("lease_id", identityID).
				Str("remote_addr", leaseEntry.RemoteAddr).
				Str("extracted_ip", ipStr).
				Msg("[GeoIP] Processing lease RemoteAddr")
			region, countryCode = getRegionFromIP(ipStr)
			log.Info().
				Str("lease_id", identityID).
				Str("region", region).
				Str("country_code", countryCode).
				Msg("[GeoIP] Region detected")
		} else {
			log.Info().
				Str("lease_id", identityID).
				Msg("[GeoIP] No RemoteAddr available for lease")
			region = "unknown"
			countryCode = ""
		}

		row := leaseRow{
			Peer:        identityID,
			Name:        name,
			Kind:        kind,
			Connected:   connected,
			DNS:         dnsLabel,
			LastSeen:    lastSeenStr,
			LastSeenISO: lastSeenISO,
			TTL:         ttlStr,
			Link:        link,
			StaleRed:    !connected && since >= 15*time.Second,
			Hide:        meta.Hide,
			Metadata:    lease.Metadata,
			Region:      region,
			CountryCode: countryCode,
		}

		if row.Hide != true {
			rows = append(rows, row)
		}
	}

	return rows
}

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}
