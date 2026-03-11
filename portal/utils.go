package portal

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"

	"golang.org/x/sync/errgroup"

	"github.com/gosuda/portal/v2/portal/keyless"
)

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, defaultControlBodyLimit)
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(dst)
}

func tokenMatches(expected, actual string) bool {
	if len(expected) == 0 || len(actual) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}

func validateAPITLS(apiTLS keyless.TLSMaterialConfig) error {
	if len(apiTLS.CertPEM) == 0 {
		return errors.New("api tls certificate is required")
	}
	if len(apiTLS.KeyPEM) == 0 && apiTLS.Keyless == nil {
		return errors.New("api tls key or keyless signer is required")
	}
	return nil
}

func bridgeConns(left, right net.Conn) {
	defer left.Close()
	defer right.Close()

	var group errgroup.Group
	group.Go(func() error {
		_, err := io.Copy(right, left)
		closeWrite(right)
		return err
	})
	group.Go(func() error {
		_, err := io.Copy(left, right)
		closeWrite(left)
		return err
	})
	_ = group.Wait()
}

func closeWrite(conn net.Conn) {
	type closeWriter interface {
		CloseWrite() error
	}
	if cw, ok := conn.(closeWriter); ok {
		_ = cw.CloseWrite()
	}
}
