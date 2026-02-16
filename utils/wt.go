package utils

import (
	"context"
	"crypto/tls"

	"github.com/quic-go/webtransport-go"

	"gosuda.org/portal/portal"
)

// NewWebTransportDialer returns a dialer that establishes WebTransport sessions.
// If tlsConfig is nil, the default TLS configuration is used.
func NewWebTransportDialer(tlsConfig *tls.Config) func(context.Context, string) (portal.Session, error) {
	return func(ctx context.Context, url string) (portal.Session, error) {
		var d webtransport.Dialer
		d.TLSClientConfig = tlsConfig
		_, sess, err := d.Dial(ctx, url, nil)
		if err != nil {
			return nil, err
		}
		return portal.NewWTSession(sess), nil
	}
}
