package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gosuda/relaydns/relaydns"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "relayserver",
	Short: "A lightweight, DNS-driven peer-to-peer proxy layer built on libp2p",
	RunE:  runServer,
}

var (
	flagBootstraps []string
	flagRelay      bool

	ingressTCP   string // e.g. :22 (raw TCP ingress, SSH)
	ingressHTTP  string // e.g. :8082 (HTTP ingress, Browser)
	adminHTTP    string // e.g. :8080 (admin API)
	outBoundPort int    // e.g. :4001 (outbound connections)
	protocol     string // e.g. /relaydns/http/1.0
	topic        string // e.g. relaydns.backends
)

func init() {
	flags := rootCmd.PersistentFlags()
	flags.StringSliceVar(&flagBootstraps, "bootstrap", nil, "multiaddrs with /p2p/ (supports /dnsaddr/ that resolves to /p2p/)")
	flags.BoolVar(&flagRelay, "relay", true, "enable libp2p relay support")

	flags.StringVar(&ingressTCP, "ingress-tcp", ":22", "L4 TCP ingress (e.g. :22 for SSH/raw TCP)")
	flags.StringVar(&ingressHTTP, "ingress-http", ":8082", "HTTP ingress (browser-friendly TCP port for HTTP backends)")
	flags.StringVar(&adminHTTP, "admin-http", ":8080", "Admin HTTP API (status/control)")
	flags.IntVar(&outBoundPort, "outbound-port", 4001, "Outbound connections")

	flags.StringVar(&protocol, "protocol", "/relaydns/http/1.0", "libp2p protocol id for streams (must match clients)")
	flags.StringVar(&topic, "topic", "relaydns.backends", "pubsub topic for backend adverts")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

func runServer(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h, err := relaydns.MakeHost(ctx, outBoundPort, flagRelay)
	if err != nil {
		return err
	}
	relaydns.ConnectBootstraps(ctx, h, flagBootstraps)

	d, err := relaydns.NewDirector(ctx, h, protocol, topic)
	if err != nil {
		return err
	}

	// 1) admin API
	go func() {
		if adminHTTP == "" {
			return
		}
		log.Println("[server] admin http:", adminHTTP)
		if err := d.ServeHTTP(adminHTTP); err != nil {
			log.Println("[server] admin http error:", err)
			cancel()
		}
	}()

	// 2) L4 TCP ingress (SSH/raw TCP)
	go func() {
		if ingressTCP == "" {
			return
		}
		log.Println("[server] tcp ingress:", ingressTCP)
		if err := d.ServeTCP(ingressTCP); err != nil {
			log.Println("[server] tcp ingress error:", err)
			cancel()
		}
	}()

	// 3) HTTP ingress (browser/HTTP traffic)
	go func() {
		if ingressHTTP == "" {
			return
		}
		log.Println("[server] http ingress (tcp-level):", ingressHTTP)
		if err := d.ServeTCP(ingressHTTP); err != nil {
			log.Println("[server] http ingress error:", err)
			cancel()
		}
	}()

	// graceful shutdown
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	cancel()
	time.Sleep(300 * time.Millisecond)
	return nil
}
