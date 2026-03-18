package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/sdk"
	"github.com/gosuda/portal/v2/utils"
)

func proxyRelayConnections(ctx context.Context, relayListener net.Listener, localAddr string, connWG *sync.WaitGroup, connCount *atomic.Int64) error {
	logger := log.With().Str("component", "portal-tunnel").Logger()

	for {
		relayConn, err := relayListener.Accept()
		if err != nil {
			switch {
			case ctx.Err() != nil || errors.Is(err, context.Canceled):
				return nil
			case errors.Is(err, net.ErrClosed):
				return errors.New("all relay listeners stopped")
			default:
				return err
			}
		}

		connID := connCount.Add(1)
		logger.Info().
			Int64("conn_id", connID).
			Str("remote_addr", relayConn.RemoteAddr().String()).
			Msg("accepted relay connection")

		connWG.Add(1)
		go func(connID int64, relayConn net.Conn) {
			defer connWG.Done()
			if err := proxyConnection(ctx, localAddr, relayConn); err != nil {
				logger.Error().Err(err).Int64("conn_id", connID).Msg("proxy connection failed")
			}
			logger.Info().Int64("conn_id", connID).Msg("proxy connection closed")
		}(connID, relayConn)
	}
}

var bufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, 64*1024)
		return &b
	},
}

func proxyConnection(ctx context.Context, localAddr string, relayConn net.Conn) error {
	defer relayConn.Close()

	targetAddr, err := utils.NormalizeTargetAddr(localAddr)
	if err != nil {
		return fmt.Errorf("invalid target %q: %w", localAddr, err)
	}

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

// proxyExposureDatagrams receives datagrams from the exposure datagram plane
// and forwards them to the local UDP service, relaying responses back.
func proxyExposureDatagrams(ctx context.Context, exposure *sdk.Exposure, localAddr string) error {
	logger := log.With().Str("component", "portal-tunnel-udp").Logger()

	targetAddr, err := utils.NormalizeTargetAddr(localAddr)
	if err != nil {
		return fmt.Errorf("invalid --host value %q: %w", localAddr, err)
	}

	resolvedAddr, err := net.ResolveUDPAddr("udp", targetAddr)
	if err != nil {
		return fmt.Errorf("resolve udp addr %q: %w", targetAddr, err)
	}

	type flowKey struct {
		flowID   uint32
		leaseID  string
		relayURL string
	}
	type flowEntry struct {
		conn     *net.UDPConn
		lastSeen time.Time
		reply    func([]byte) error
	}

	var mu sync.Mutex
	flows := make(map[flowKey]*flowEntry)

	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				mu.Lock()
				now := time.Now()
				for key, f := range flows {
					if now.Sub(f.lastSeen) > 30*time.Second {
						_ = f.conn.Close()
						delete(flows, key)
					}
				}
				mu.Unlock()
			}
		}
	}()

	getOrCreateFlow := func(key flowKey, reply func([]byte) error) (*net.UDPConn, error) {
		mu.Lock()
		if f, ok := flows[key]; ok {
			f.lastSeen = time.Now()
			f.reply = reply
			mu.Unlock()
			return f.conn, nil
		}
		mu.Unlock()

		localConn, err := net.DialUDP("udp", nil, resolvedAddr)
		if err != nil {
			return nil, err
		}

		mu.Lock()
		if f, ok := flows[key]; ok {
			mu.Unlock()
			_ = localConn.Close()
			f.lastSeen = time.Now()
			f.reply = reply
			return f.conn, nil
		}
		flows[key] = &flowEntry{conn: localConn, lastSeen: time.Now(), reply: reply}
		mu.Unlock()

		go func() {
			buf := make([]byte, 65535)
			for {
				n, err := localConn.Read(buf)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					logger.Debug().
						Err(err).
						Uint32("flow_id", key.flowID).
						Str("lease_id", key.leaseID).
						Str("relay_url", key.relayURL).
						Msg("local read ended")
					return
				}

				mu.Lock()
				entry := flows[key]
				if entry != nil {
					entry.lastSeen = time.Now()
				}
				replyFn := func([]byte) error { return net.ErrClosed }
				if entry != nil && entry.reply != nil {
					replyFn = entry.reply
				}
				mu.Unlock()

				if sendErr := replyFn(buf[:n]); sendErr != nil {
					logger.Debug().
						Err(sendErr).
						Uint32("flow_id", key.flowID).
						Str("lease_id", key.leaseID).
						Str("relay_url", key.relayURL).
						Msg("send datagram to relay failed")
					return
				}
			}
		}()

		return localConn, nil
	}

	logger.Info().Str("target", targetAddr).Msg("udp proxy loop started, waiting for datagrams")
	for {
		dg, err := exposure.AcceptDatagram()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, net.ErrClosed) {
				break
			}
			return fmt.Errorf("accept datagram: %w", err)
		}

		logger.Debug().
			Uint32("flow_id", dg.FlowID).
			Int("bytes", len(dg.Payload)).
			Str("lease_id", dg.LeaseID).
			Str("relay_url", dg.RelayURL).
			Str("udp_addr", dg.UDPAddr).
			Str("target", targetAddr).
			Msg("datagram received from relay, forwarding to local")

		key := flowKey{
			flowID:   dg.FlowID,
			leaseID:  dg.LeaseID,
			relayURL: dg.RelayURL,
		}
		localConn, err := getOrCreateFlow(key, dg.Reply)
		if err != nil {
			logger.Warn().
				Err(err).
				Uint32("flow_id", dg.FlowID).
				Str("lease_id", dg.LeaseID).
				Str("relay_url", dg.RelayURL).
				Msg("dial local udp failed")
			continue
		}

		if _, err := localConn.Write(dg.Payload); err != nil {
			logger.Warn().
				Err(err).
				Uint32("flow_id", dg.FlowID).
				Str("lease_id", dg.LeaseID).
				Str("relay_url", dg.RelayURL).
				Msg("write to local udp failed")
		}
	}

	return nil
}
