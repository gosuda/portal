package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"

	"github.com/gosuda/portal/v2/sdk"
)

var bufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, 64*1024)
		return &b
	},
}

func runProxyLoop(ctx context.Context, listener *sdk.Listener, targetAddr string, connGroup *errgroup.Group, connCount *atomic.Int64) error {
	logger := log.With().Str("component", "portal-tunnel").Logger()

	for {
		relayConn, entry, err := listener.AcceptEntry()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return nil
			}
			return err
		}

		connID := connCount.Add(1)
		logger.Info().
			Int64("conn_id", connID).
			Str("remote_addr", relayConn.RemoteAddr().String()).
			Str("relay", entry.RelayURL).
			Str("lease_id", entry.LeaseID).
			Msg("accepted relay connection")

		connGroup.Go(func() error {
			if err := proxyConnection(ctx, targetAddr, relayConn); err != nil {
				logger.Error().Err(err).Int64("conn_id", connID).Msg("proxy connection failed")
			}
			logger.Info().Int64("conn_id", connID).Msg("proxy connection closed")
			return nil
		})
	}
}

func proxyConnection(ctx context.Context, targetAddr string, relayConn net.Conn) error {
	defer relayConn.Close()

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	localConn, err := dialer.DialContext(ctx, "tcp", targetAddr)
	if err != nil {
		return writeEmptyHTTPResponse(relayConn)
	}
	defer localConn.Close()

	errCh := make(chan error, 2)
	stopCh := make(chan struct{})

	go func() {
		select {
		case <-ctx.Done():
			_ = relayConn.Close()
			_ = localConn.Close()
		case <-stopCh:
		}
	}()

	go func() {
		bufPtr := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(bufPtr)
		_, err := io.CopyBuffer(localConn, relayConn, *bufPtr)
		if tcpConn, ok := localConn.(*net.TCPConn); ok {
			_ = tcpConn.CloseWrite()
		}
		errCh <- err
	}()

	go func() {
		bufPtr := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(bufPtr)
		_, err := io.CopyBuffer(relayConn, localConn, *bufPtr)
		_ = relayConn.Close()
		errCh <- err
	}()

	var firstErr error
	for range 2 {
		if err := <-errCh; err != nil && firstErr == nil {
			firstErr = err
		}
	}

	close(stopCh)
	if errors.Is(firstErr, io.EOF) || errors.Is(firstErr, net.ErrClosed) {
		return nil
	}
	return firstErr
}

func writeEmptyHTTPResponse(conn net.Conn) error {
	htmlBody := `<!DOCTYPE html>
<html>
<head><title>Service Unavailable</title></head>
<body style="font-family:sans-serif;text-align:center;padding:50px;">
<h1>Service Unavailable</h1>
<p>The local service is not currently running.</p>
<p>Please start your local application and refresh this page.</p>
</body>
</html>`
	response := fmt.Sprintf("HTTP/1.1 503 Service Unavailable\r\n"+
		"Content-Type: text/html; charset=utf-8\r\n"+
		"Content-Length: %d\r\n"+
		"Connection: close\r\n"+
		"\r\n%s", len(htmlBody), htmlBody)
	_, err := conn.Write([]byte(response))
	return err
}

func normalizeTargetAddr(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("target address is required")
	}

	if strings.Contains(raw, "://") {
		targetURL, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("parse target url: %w", err)
		}
		if !strings.EqualFold(targetURL.Scheme, "http") && !strings.EqualFold(targetURL.Scheme, "https") {
			return "", fmt.Errorf("unsupported target url scheme %q", targetURL.Scheme)
		}
		if targetURL.Host == "" {
			return "", errors.New("target url host is empty")
		}
		if targetURL.Path != "" && targetURL.Path != "/" {
			return "", errors.New("target url path is not supported")
		}
		if targetURL.RawQuery != "" {
			return "", errors.New("target url query is not supported")
		}
		if targetURL.Fragment != "" {
			return "", errors.New("target url fragment is not supported")
		}
		raw = targetURL.Host
	}

	if _, _, err := net.SplitHostPort(raw); err == nil {
		return raw, nil
	}
	if strings.Count(raw, ":") == 0 {
		return net.JoinHostPort(raw, "80"), nil
	}
	if ip := net.ParseIP(raw); ip != nil {
		return net.JoinHostPort(raw, "80"), nil
	}
	return "", fmt.Errorf("invalid target address %q", raw)
}
