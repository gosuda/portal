package sdk

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/gosuda/relaydns/relaydns"
	"github.com/gosuda/relaydns/relaydns/core/cryptoops"
	"github.com/gosuda/relaydns/relaydns/core/proto/rdverb"
	"github.com/gosuda/relaydns/relaydns/utils/wsstream"
)

func NewCredential() (*cryptoops.Credential, error) {
	return cryptoops.NewCredential()
}

func webSocketDialer() func(context.Context, string) (io.ReadWriteCloser, error) {
	return func(ctx context.Context, url string) (io.ReadWriteCloser, error) {
		wsConn, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			return nil, err
		}
		return &wsstream.WsStream{Conn: wsConn}, nil
	}
}

type RDClientConfig struct {
	BootstrapServers []string
	Dialer           func(context.Context, string) (io.ReadWriteCloser, error)
}

type Option func(*RDClientConfig)

type rdRelay struct {
	addr   string
	client *relaydns.RelayClient
	stop   chan struct{}
}

type RDConnection struct {
	via        *rdRelay
	localAddr  string
	remoteAddr string
	conn       io.ReadWriteCloser
}

type RDListener struct {
	mu sync.Mutex

	cred  *cryptoops.Credential
	conns map[*RDConnection]struct{}

	connCh chan *RDConnection
}

type RDClient struct {
	mu sync.Mutex

	relays    map[string]*rdRelay
	listeners map[string]*RDListener

	stopch chan struct{}
}

var (
	ErrNoAvailableRelay = errors.New("no available relay")
)

func NewClient(opt ...Option) (*RDClient, error) {
	return &RDClient{}, nil
}

func (g *RDClient) Dial(cred *cryptoops.Credential, leaseID string, alpn string) (*RDConnection, error) {
	var relays []*rdRelay

	g.mu.Lock()
	for _, server := range g.relays {
		relays = append(relays, server)
	}
	g.mu.Unlock()

	var wg sync.WaitGroup
	var availableRelaysMu sync.Mutex
	var availableRelays []*rdRelay

	for _, relay := range relays {
		wg.Add(1)
		go func(relay *rdRelay) {
			defer wg.Done()
			info, err := relay.client.GetRelayInfo()
			if err != nil {
				return
			}

			if slices.Contains(info.Leases, leaseID) {
				availableRelaysMu.Lock()
				availableRelays = append(availableRelays, relay)
				availableRelaysMu.Unlock()
			}
		}(relay)
	}
	wg.Wait()

	if len(availableRelays) == 0 {
		return nil, ErrNoAvailableRelay
	}

	for _, relay := range availableRelays {
		code, conn, err := relay.client.RequestConnection(leaseID, alpn, cred)
		if err != nil || code != rdverb.ResponseCode_RESPONSE_CODE_ACCEPTED {
			continue
		}
		return &RDConnection{via: relay, conn: conn, localAddr: conn.LocalID(), remoteAddr: conn.RemoteID()}, nil
	}

	return nil, ErrNoAvailableRelay
}

func (g *RDClient) Listen(cred *cryptoops.Credential, name string, alpns []string) (*RDListener, error) {

}

func (g *RDClient) listenerWorker(server *rdRelay) {
	for {
		select {
		case <-server.stop:
			return
		case conn := <-server.client.IncommingConnection():
			lease := conn.LeaseID()

			g.mu.Lock()
			listener, ok := g.listeners[lease]
			g.mu.Unlock()

			if !ok {
				continue
			}

			rdConn := &RDConnection{via: server, conn: conn, localAddr: conn.LocalID(), remoteAddr: conn.RemoteID()}

			listener.mu.Lock()
			listener.conns[rdConn] = struct{}{}
			listener.mu.Unlock()

			listener.connCh <- rdConn
		}
	}
}

func (g *RDClient) Close() error {
	var errs []error

	close(g.stopch)

	g.mu.Lock()
	for _, listener := range g.listeners {
		if err := listener.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	g.mu.Unlock()

	g.mu.Lock()
	for _, server := range g.relays {
		if err := server.client.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	g.mu.Unlock()

	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}
