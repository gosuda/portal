package relaydns

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/rs/zerolog/log"
)

type RelayServer struct {
	ctx       context.Context
	h         host.Host
	protocol  string
	topicName string
	sub       *pubsub.Subscription

	storeMu sync.Mutex
	store   map[string]HostEntry
	ttl     time.Duration
	deadTTL time.Duration
}

func NewRelayServer(ctx context.Context, h host.Host, protocol, topic string) (*RelayServer, error) {
	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		return nil, err
	}
	t, err := ps.Join(topic)
	if err != nil {
		return nil, err
	}
	sub, err := t.Subscribe()
	if err != nil {
		return nil, err
	}
	d := &RelayServer{
		ctx:       ctx,
		h:         h,
		protocol:  protocol,
		topicName: topic,
		sub:       sub,
		store:     map[string]HostEntry{},
		ttl:       15 * time.Second,
		deadTTL:   10 * time.Minute,
	}
	go d.collect()
	go d.gc()
	return d, nil
}

func (s *RelayServer) Close() error {
	s.sub.Cancel()
	return nil
}

func (s *RelayServer) collect() {
	for {
		msg, err := s.sub.Next(s.ctx)
		if err != nil {
			return
		}
		var ad Advertise
		if err := json.Unmarshal(msg.Data, &ad); err != nil {
			continue
		}
		var ai *peer.AddrInfo
		// pick any addr that includes /p2p/ (dnsaddr resolved entries will)
		for _, s := range ad.Addrs {
			m, err := ma.NewMultiaddr(s)
			if err != nil {
				continue
			}
			if a, err := peer.AddrInfoFromP2pAddr(m); err == nil {
				ai = a
				break
			}
		}
		if ai == nil {
			continue
		}
		now := time.Now()
		s.storeMu.Lock()
		_, existed := s.store[ad.Peer]
		s.store[ad.Peer] = HostEntry{Info: ad, AddrInfo: ai, LastSeen: now, Connected: true}
		// refresh picker snapshot
		snap := make([]HostEntry, 0, len(s.store))
		for _, v := range s.store {
			snap = append(snap, v)
		}
		s.storeMu.Unlock()
		if existed {
			log.Debug().Str("peer", ad.Peer).Str("name", ad.Name).Msg("server: updated client advert")
		} else {
			log.Info().Str("peer", ad.Peer).Str("name", ad.Name).Msg("server: added client")
		}
		_ = snap // snapshot kept local; selection handled explicitly via /peer
	}
}

func (s *RelayServer) gc() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			now := time.Now()
			removed := make([]HostEntry, 0)
			s.storeMu.Lock()
			for k, v := range s.store {
				// Prefer client-provided TTL if present; fallback to default d.ttl
				ttl := s.ttl
				if v.Info.TTL > 0 {
					ttl = time.Duration(v.Info.TTL) * time.Second
				}
				// mark disconnected after ttl
				if now.Sub(v.LastSeen) > ttl && v.Connected {
					v.Connected = false
					s.store[k] = v
				}
				// remove only after extended dead TTL
				deadAfter := s.deadTTL
				if v.Info.TTL > 0 {
					da := time.Duration(v.Info.TTL) * time.Second * 5
					if da > deadAfter {
						deadAfter = da
					}
				}
				if now.Sub(v.LastSeen) > deadAfter {
					removed = append(removed, v)
					delete(s.store, k)
				}
			}
			snap := make([]HostEntry, 0, len(s.store))
			for _, v := range s.store {
				snap = append(snap, v)
			}
			s.storeMu.Unlock()
			for _, r := range removed {
				log.Info().Str("peer", r.Info.Peer).Str("name", r.Info.Name).Dur("idle", now.Sub(r.LastSeen)).Msg("director: removed stale client")
			}
			_ = snap
		}
	}
}

// Hosts returns a snapshot of current known hosts.
func (s *RelayServer) Hosts() []HostEntry {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	list := make([]HostEntry, 0, len(s.store))
	for _, v := range s.store {
		list = append(list, v)
	}
	// Sort by last-seen (most recent first)
	sort.SliceStable(list, func(i, j int) bool {
		return list[i].LastSeen.After(list[j].LastSeen)
	})
	return list
}

// ProxyHTTP proxies the given HTTP request to the specified peer and writes the response to w.
func (d *RelayServer) ProxyHTTP(w http.ResponseWriter, r *http.Request, peerID, pathSuffix string) {
	d.storeMu.Lock()
	entry, ok := d.store[peerID]
	d.storeMu.Unlock()
	if !ok || entry.AddrInfo == nil {
		http.Error(w, "peer not found", http.StatusNotFound)
		return
	}
	if err := d.h.Connect(d.ctx, *entry.AddrInfo); err != nil {
		log.Error().Err(err).Msgf("connect %s failed", entry.AddrInfo.ID)
		http.Error(w, "upstream connect failed", http.StatusBadGateway)
		return
	}
	s, err := d.h.NewStream(d.ctx, entry.AddrInfo.ID, protocol.ID(d.protocol))
	if err != nil {
		log.Error().Err(err).Msg("new stream")
		http.Error(w, "open stream failed", http.StatusBadGateway)
		return
	}
	defer s.Close()
	outReq := r.Clone(d.ctx)
	outReq.URL = &url.URL{Path: pathSuffix, RawQuery: r.URL.RawQuery}
	outReq.RequestURI = ""
	if err := outReq.Write(s); err != nil {
		log.Error().Err(err).Msg("write upstream request")
		http.Error(w, "write upstream failed", http.StatusBadGateway)
		return
	}
	br := bufio.NewReader(s)
	resp, err := http.ReadResponse(br, outReq)
	if err != nil {
		log.Error().Err(err).Msg("read upstream response")
		http.Error(w, "bad upstream response", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Handle WebSocket Upgrade: write 101 response and then raw-tunnel bytes
	if resp.StatusCode == http.StatusSwitchingProtocols && strings.Contains(strings.ToLower(resp.Header.Get("Upgrade")), "websocket") {
		if hj, ok := w.(http.Hijacker); ok {
			clientConn, clientBuf, err := hj.Hijack()
			if err != nil {
				log.Error().Err(err).Msg("hijack client conn")
				return
			}
			defer clientConn.Close()
			// Write upstream 101 response (headers)
			if err := resp.Write(clientBuf); err == nil {
				_ = clientBuf.Flush()
			}
			// Flush any bytes already buffered by http.ReadResponse's bufio.Reader
			if n := br.Buffered(); n > 0 {
				tmp := make([]byte, n)
				if _, err := io.ReadFull(br, tmp); err == nil {
					_, _ = clientConn.Write(tmp)
				}
			}
			// Raw byte tunnel between client and upstream stream
			go io.Copy(s, clientConn)
			io.Copy(clientConn, s)
			return
		}
	}
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	// If upstream is SSE, forward with Flush to avoid buffering
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		if f, ok := w.(http.Flusher); ok {
			buf := make([]byte, 4096)
			for {
				n, err := resp.Body.Read(buf)
				if n > 0 {
					if _, werr := w.Write(buf[:n]); werr == nil {
						f.Flush()
					}
				}
				if err != nil {
					break
				}
			}
			return
		}
	}
	_, _ = io.Copy(w, resp.Body)
}

// ProxyTCP opens a libp2p stream to peerID using the Director protocol and
// pipes raw bytes between the accepted TCP connection and the libp2p stream.
func (d *RelayServer) ProxyTCP(c net.Conn, peerID string) error {
	defer c.Close()
	d.storeMu.Lock()
	entry, ok := d.store[peerID]
	d.storeMu.Unlock()
	if !ok || entry.AddrInfo == nil {
		return fmt.Errorf("peer not found")
	}
	if err := d.h.Connect(d.ctx, *entry.AddrInfo); err != nil {
		return err
	}
	s, err := d.h.NewStream(d.ctx, entry.AddrInfo.ID, protocol.ID(d.protocol))
	if err != nil {
		return err
	}
	defer s.Close()
	// bidirectional copy
	go io.Copy(s, c)
	_, _ = io.Copy(c, s)
	return nil
}
