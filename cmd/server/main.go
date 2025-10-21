package main

import (
	"context"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gosuda/relaydns/relaydns"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "relayserver",
	Short: "A lightweight, DNS-driven peer-to-peer proxy layer built on libp2p",
	RunE:  runServer,
}

var (
	flagBootstraps []string

	httpAddr string // unified admin + HTTP proxy (e.g. :8080)
	protocol string
	topic    string
	tcpAddr  string // optional raw TCP ingress (e.g. :2222 for SSH)
)

func init() {
	flags := rootCmd.PersistentFlags()
	flags.StringSliceVar(&flagBootstraps, "bootstrap", nil, "multiaddrs with /p2p/ (supports /dnsaddr/ that resolves to /p2p/)")

	flags.StringVar(&httpAddr, "http", ":8080", "Unified admin UI and HTTP proxy listen address")
	flags.StringVar(&protocol, "protocol", "/relaydns/http/1.0", "libp2p protocol id for streams (must match clients)")
	flags.StringVar(&topic, "topic", "relaydns.backends", "pubsub topic for backend adverts")
	flags.StringVar(&tcpAddr, "tcp", "", "Optional raw TCP ingress (e.g. :2222 for SSH). Empty to disable")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal().Err(err).Msg("execute root command")
	}
}

func runServer(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const outBoundPort = 4001
	h, err := relaydns.MakeHost(ctx, outBoundPort, true)
	if err != nil {
		return err
	}
	relaydns.ConnectBootstraps(ctx, h, flagBootstraps)

	d, err := relaydns.NewDirector(ctx, h, protocol, topic)
	if err != nil {
		return err
	}

	// Admin UI + per-peer HTTP proxy served here
	go serveHTTP(ctx, httpAddr, d, h, cancel)

	// Optional raw TCP ingress (e.g., SSH)
	if tcpAddr != "" {
		go serveTCPIngress(ctx, tcpAddr, d)
	}

	// graceful shutdown
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	cancel()
	time.Sleep(300 * time.Millisecond)
	return nil
}

// serveTCPIngress listens on addr for raw TCP (e.g., SSH) and proxies
// incoming connections to a chosen peer over libp2p stream using Director.
func serveTCPIngress(ctx context.Context, addr string, d *relaydns.Director) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error().Err(err).Msgf("tcp ingress listen failed: %s", addr)
		return
	}
	log.Info().Msgf("[server] tcp ingress: %s", addr)
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			continue
		}
		go func(c net.Conn) {
			hosts := d.Hosts()
			if len(hosts) == 0 {
				log.Warn().Msg("tcp ingress: no backend peers available")
				_ = c.Close()
				return
			}
			// pick most recent (Hosts() sorted by last seen)
			peerID := hosts[0].Info.Peer
			if err := d.ProxyTCP(c, peerID); err != nil {
				log.Warn().Err(err).Msgf("tcp ingress proxy failed to %s", peerID)
			}
		}(conn)
	}
}
