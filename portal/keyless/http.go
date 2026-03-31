package keyless

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func NewRelayHTTPClient(ctx context.Context, relayURL *url.URL, rootCAPEM []byte, timeout time.Duration) (*tls.Config, *http.Client, error) {
	if relayURL == nil {
		return nil, nil, errors.New("relay url is required")
	}

	serverName := strings.TrimSpace(relayURL.Hostname())
	if serverName == "" {
		return nil, nil, errors.New("relay hostname is required")
	}

	rootCAs, err := RelayRootCAs(ctx, relayURL.String(), serverName, rootCAPEM)
	if err != nil {
		return nil, nil, err
	}

	rawTLSConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: serverName,
		RootCAs:    rootCAs,
		NextProtos: []string{"http/1.1"},
	}
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:   rawTLSConfig.Clone(),
			ForceAttemptHTTP2: false,
		},
		Timeout: timeout,
	}
	return rawTLSConfig, httpClient, nil
}
