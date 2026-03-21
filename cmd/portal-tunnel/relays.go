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
	"github.com/gosuda/portal/v2/types"
)

func proxyExposure(ctx context.Context, exposure *sdk.Exposure, serviceName string) error {
	defer exposure.Close()
	if len(exposure.ActiveRelayURLs()) == 0 {
		return errors.New("no relay URLs provided")
	}

	tcpTarget := exposure.TargetAddr
	udpTarget := exposure.UDPAddr
	udpEnabled := udpTarget != ""

	log.Info().
		Str("release_version", types.ReleaseVersion).
		Str("tcp_target", tcpTarget).
		Str("service_name", serviceName).
		Strs("relays", exposure.ActiveRelayURLs()).
		Msg("starting portal tunnel")
	if udpEnabled {
		log.Info().
			Str("udp_target", udpTarget).
			Str("service_name", serviceName).
			Msg("udp relay enabled")
	}

	var connWG sync.WaitGroup
	var connCount atomic.Int64
	var udpErrCh chan error

	if udpEnabled {
		udpErrCh = make(chan error, 1)
		go func() {
			if err := runUDPProxy(ctx, exposure, udpTarget); err != nil && ctx.Err() == nil {
				udpErrCh <- err
				_ = exposure.Close()
			}
		}()
	}

	go func() {
		<-ctx.Done()
		_ = exposure.Close()
	}()

	waitErr := proxyRelayConnections(ctx, exposure, tcpTarget, &connWG, &connCount)
	if waitErr != nil {
		_ = exposure.Close()
	}

	var udpErr error
	if udpErrCh != nil {
		select {
		case udpErr = <-udpErrCh:
		default:
		}
	}

	closeErr := exposure.Close()
	if waitErr != nil {
		log.Error().Err(waitErr).Msg("relay supervisor exited with error")
	}
	if udpErr != nil {
		log.Error().Err(udpErr).Msg("udp proxy exited with error")
	}
	if closeErr != nil {
		log.Error().Err(closeErr).Msg("relay shutdown failed")
	}

	if ctx.Err() != nil {
		log.Info().Msg("tunnel shutting down")
	}

	done := make(chan struct{})
	go func() {
		connWG.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		log.Warn().Msg("tunnel shutdown timeout; connections still active")
	}

	log.Info().Msg("tunnel shutdown complete")
	return errors.Join(waitErr, udpErr, closeErr)
}

func proxyRelayConnections(ctx context.Context, exposure *sdk.Exposure, localAddr string, connWG *sync.WaitGroup, connCount *atomic.Int64) error {
	for {
		relayConn, err := exposure.Accept()
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
		log.Info().
			Int64("conn_id", connID).
			Str("remote_addr", relayConn.RemoteAddr().String()).
			Msg("accepted relay connection")

		connWG.Add(1)
		go func(connID int64, relayConn net.Conn) {
			defer connWG.Done()
			if err := proxyConnection(ctx, localAddr, relayConn); err != nil {
				log.Error().Err(err).Int64("conn_id", connID).Msg("proxy connection failed")
			}
			log.Info().Int64("conn_id", connID).Msg("proxy connection closed")
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

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	localConn, err := dialer.DialContext(ctx, "tcp", localAddr)
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

// runUDPProxy waits for the exposure datagram plane and proxies it to the
// configured local UDP target.
func runUDPProxy(ctx context.Context, exposure *sdk.Exposure, udpTarget string) error {
	udpAddrs, err := exposure.WaitDatagramReady(ctx)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return ctx.Err()
		}
		return fmt.Errorf("wait for udp readiness: %w", err)
	}
	if len(udpAddrs) == 0 {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("relay did not expose any UDP listeners")
	}

	for _, udpAddr := range udpAddrs {
		log.Info().
			Str("udp_addr", udpAddr).
			Msg("UDP tunnel ready")
	}

	return proxyExposureDatagrams(ctx, exposure, udpTarget)
}

// proxyExposureDatagrams receives datagrams from the exposure datagram plane
// and forwards them to the local UDP service, relaying responses back.
func proxyExposureDatagrams(ctx context.Context, exposure *sdk.Exposure, localAddr string) error {
	resolvedAddr, err := net.ResolveUDPAddr("udp", localAddr)
	if err != nil {
		return fmt.Errorf("resolve udp addr %q: %w", localAddr, err)
	}

	type flowKey struct {
		flowID   uint32
		leaseID  string
		relayURL string
	}
	type flowEntry struct {
		conn     *net.UDPConn
		lastSeen time.Time
		frame    types.DatagramFrame
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

	getOrCreateFlow := func(frame types.DatagramFrame) (*net.UDPConn, error) {
		key := flowKey{
			flowID:   frame.FlowID,
			leaseID:  frame.LeaseID,
			relayURL: frame.RelayURL,
		}

		mu.Lock()
		if f, ok := flows[key]; ok {
			f.lastSeen = time.Now()
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
			return f.conn, nil
		}
		flows[key] = &flowEntry{
			conn:     localConn,
			lastSeen: time.Now(),
			frame: types.DatagramFrame{
				FlowID:   frame.FlowID,
				LeaseID:  frame.LeaseID,
				RelayURL: frame.RelayURL,
				UDPAddr:  frame.UDPAddr,
			},
		}
		mu.Unlock()

		go func() {
			buf := make([]byte, 65535)
			for {
				n, err := localConn.Read(buf)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					log.Debug().
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
				replyFrame := types.DatagramFrame{}
				if entry != nil {
					replyFrame = entry.frame
					replyFrame.Payload = append([]byte(nil), buf[:n]...)
				}
				mu.Unlock()

				if sendErr := exposure.SendDatagram(replyFrame); sendErr != nil {
					log.Debug().
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

	log.Info().Str("target", localAddr).Msg("udp proxy loop started, waiting for datagrams")
	for {
		frame, err := exposure.AcceptDatagram()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, net.ErrClosed) {
				break
			}
			return fmt.Errorf("accept datagram: %w", err)
		}

		log.Debug().
			Uint32("flow_id", frame.FlowID).
			Int("bytes", len(frame.Payload)).
			Str("lease_id", frame.LeaseID).
			Str("relay_url", frame.RelayURL).
			Str("udp_addr", frame.UDPAddr).
			Str("target", localAddr).
			Msg("datagram received from relay, forwarding to local")

		localConn, err := getOrCreateFlow(frame)
		if err != nil {
			log.Warn().
				Err(err).
				Uint32("flow_id", frame.FlowID).
				Str("lease_id", frame.LeaseID).
				Str("relay_url", frame.RelayURL).
				Msg("dial local udp failed")
			continue
		}

		if _, err := localConn.Write(frame.Payload); err != nil {
			log.Warn().
				Err(err).
				Uint32("flow_id", frame.FlowID).
				Str("lease_id", frame.LeaseID).
				Str("relay_url", frame.RelayURL).
				Msg("write to local udp failed")
		}
	}

	return nil
}
