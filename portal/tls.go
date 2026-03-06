package portal

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"

	keylesslib "github.com/gosuda/keyless_tls/keyless"
)

type TLSMaterialConfig struct {
	Keyless *RemoteSignerConfig
	CertPEM []byte
	KeyPEM  []byte
}

type RemoteSignerConfig struct {
	Endpoint      string
	ServerName    string
	KeyID         string
	ClientCertPEM []byte
	ClientKeyPEM  []byte
	RootCAPEM     []byte
}

func attachAPITLS(server *http.Server, cfg TLSMaterialConfig) (io.Closer, error) {
	if server == nil {
		return nil, errors.New("http server is required")
	}
	if cfg.Keyless != nil {
		remoteSigner, err := keylesslib.AttachToHTTPServer(server, keylesslib.HTTPServerAttachConfig{
			CertPEM: cfg.CertPEM,
			RemoteSigner: keylesslib.RemoteSignerConfig{
				Endpoint:      cfg.Keyless.Endpoint,
				ServerName:    cfg.Keyless.ServerName,
				KeyID:         cfg.Keyless.KeyID,
				ClientCertPEM: cfg.Keyless.ClientCertPEM,
				ClientKeyPEM:  cfg.Keyless.ClientKeyPEM,
				RootCAPEM:     cfg.Keyless.RootCAPEM,
			},
			NextProtos:    []string{"http/1.1"},
			MinTLSVersion: tls.VersionTLS12,
		})
		if err != nil {
			return nil, err
		}
		return remoteSigner, nil
	}

	cert, err := tls.X509KeyPair(cfg.CertPEM, cfg.KeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse api tls key pair: %w", err)
	}

	server.TLSConfig = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"http/1.1"},
		Certificates: []tls.Certificate{cert},
	}
	return nil, nil
}
