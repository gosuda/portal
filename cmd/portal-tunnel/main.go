package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/sdk"
	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

const shortClientIDSuffixLen = 6

func main() {
	zerolog.TimeFieldFormat = time.RFC3339
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	if err := run(os.Args[1:]); err != nil {
		log.Error().Err(err).Msg("portal tunnel exited with error")
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printRootUsage(os.Stdout)
		return nil
	}

	command := strings.TrimSpace(args[0])
	switch command {
	case "help", "-h", "--help":
		printRootUsage(os.Stdout)
		return nil
	case "expose":
		return runExposeCommand(args[1:])
	case "list":
		return runListCommand(args[1:])
	default:
		printRootUsage(os.Stderr)
		return fmt.Errorf("unknown command %q", command)
	}
}

func runExposeCommand(args []string) error {
	cfg, cfgPath, err := loadCLIConfig()
	if err != nil {
		return fmt.Errorf("load portal config: %w", err)
	}

	defaultRelays := true

	fs := flag.NewFlagSet("expose", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var (
		relayCSV  string
		target    string
		name      string
		desc      string
		tags      string
		thumbnail string
		owner     string
		hide      bool
	)
	fs.StringVar(&relayCSV, "relays", "", "Additional Portal relay server API URLs (comma-separated; scheme omitted defaults to https)")
	fs.BoolVar(&defaultRelays, "default-relays", defaultRelays, "Include public registry relays")
	fs.StringVar(&name, "name", "", "Public hostname prefix (single DNS label); auto-generated when omitted")
	fs.StringVar(&desc, "description", "", "Service description metadata")
	fs.StringVar(&tags, "tags", "", "Service tags metadata (comma-separated)")
	fs.StringVar(&thumbnail, "thumbnail", "", "Service thumbnail URL metadata")
	fs.StringVar(&owner, "owner", "", "Service owner metadata")
	fs.BoolVar(&hide, "hide", false, "Hide service from discovery")
	fs.Usage = func() {
		printExposeUsage(fs.Output())
	}

	if err := fs.Parse(args); err != nil {
		printExposeUsage(os.Stderr)
		return err
	}

	if positionals := fs.Args(); len(positionals) > 0 {
		if len(positionals) > 1 {
			return errors.New("only one target is supported")
		}
		target = positionals[0]
	}

	target = strings.TrimSpace(target)
	if target == "" {
		printExposeUsage(os.Stderr)
		return errors.New("target is required")
	}
	if _, err := strconv.Atoi(target); err == nil {
		target = net.JoinHostPort("127.0.0.1", target)
	} else {
		targetAddr, err := utils.NormalizeTargetAddr(target)
		if err != nil {
			printExposeUsage(os.Stderr)
			return fmt.Errorf("invalid target %q: %w", target, err)
		}
		target = targetAddr
	}

	if strings.TrimSpace(name) == "" {
		if strings.TrimSpace(cfg.ClientID) == "" {
			cfg.ClientID = utils.RandomID("cli_")
		}
		name, err = defaultExposeName(target, cfg.ClientID)
		if err != nil {
			return fmt.Errorf("derive service name: %w", err)
		}
		if saveErr := saveCLIConfig(cfgPath, cfg); saveErr != nil {
			return fmt.Errorf("persist portal config: %w", saveErr)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	relayInputs := append([]string(nil), cfg.Relays...)
	if explicitRelays := strings.TrimSpace(relayCSV); explicitRelays != "" {
		relayInputs = []string{explicitRelays}
	}

	relayURLs, err := resolveRelayURLs(ctx, "", relayInputs, defaultRelays)
	if err != nil {
		return fmt.Errorf("resolve relay urls: %w", err)
	}
	if len(relayURLs) == 0 {
		return errors.New("no relay URLs configured; run the installer first or pass --relays")
	}

	return runTunnel(
		ctx,
		stop,
		relayURLs,
		target,
		name,
		types.LeaseMetadata{
			Description: desc,
			Tags:        utils.SplitCSV(tags),
			Owner:       owner,
			Thumbnail:   thumbnail,
			Hide:        hide,
		},
	)
}

func runListCommand(args []string) error {
	cfg, _, err := loadCLIConfig()
	if err != nil {
		return fmt.Errorf("load portal config: %w", err)
	}

	defaultRelays := true

	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var relayCSV string
	fs.StringVar(&relayCSV, "relays", "", "Additional Portal relay server API URLs (comma-separated; scheme omitted defaults to https)")
	fs.BoolVar(&defaultRelays, "default-relays", defaultRelays, "Include public registry relays")
	fs.Usage = func() {
		printListUsage(fs.Output())
	}

	if err := fs.Parse(args); err != nil {
		printListUsage(os.Stderr)
		return err
	}
	if len(fs.Args()) > 0 {
		printListUsage(os.Stderr)
		return errors.New("list does not accept positional arguments")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	relayInputs := append([]string(nil), cfg.Relays...)
	if explicitRelays := strings.TrimSpace(relayCSV); explicitRelays != "" {
		relayInputs = []string{explicitRelays}
	}

	relayURLs, err := resolveRelayURLs(ctx, "", relayInputs, defaultRelays)
	if err != nil {
		return fmt.Errorf("resolve relay urls: %w", err)
	}
	if len(relayURLs) == 0 {
		return errors.New("no relay URLs configured")
	}

	for _, relayURL := range relayURLs {
		fmt.Println(relayURL)
	}
	return nil
}

func runTunnel(
	ctx context.Context,
	stop func(),
	relayURLs []string,
	target string,
	name string,
	metadata types.LeaseMetadata,
) error {
	logger := log.With().Str("component", "portal").Logger()

	exposure, err := sdk.Expose(ctx, relayURLs, name, metadata)
	if err != nil {
		return fmt.Errorf("service %s: failed to start relays: %w", name, err)
	}
	if exposure == nil {
		return errors.New("no relay URLs provided")
	}
	defer exposure.Close()

	logger.Info().
		Str("release_version", types.ReleaseVersion).
		Str("local", target).
		Str("service_name", name).
		Strs("relays", exposure.RelayURLs()).
		Msg("starting portal tunnel")

	var connWG sync.WaitGroup
	var connCount atomic.Int64

	go func() {
		<-ctx.Done()
		_ = exposure.Close()
	}()

	waitErr := proxyRelayConnections(ctx, exposure, target, &connWG, &connCount)
	if waitErr != nil && stop != nil {
		stop()
	}
	closeErr := exposure.Close()
	if waitErr != nil {
		logger.Error().Err(waitErr).Msg("relay supervisor exited with error")
	}
	if closeErr != nil {
		logger.Error().Err(closeErr).Msg("relay shutdown failed")
	}

	if ctx.Err() != nil {
		logger.Info().Msg("tunnel shutting down")
	}

	done := make(chan struct{})
	go func() {
		connWG.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		logger.Warn().Msg("tunnel shutdown timeout; connections still active")
	}

	logger.Info().Msg("tunnel shutdown complete")
	return errors.Join(waitErr, closeErr)
}

func resolveRelayURLs(ctx context.Context, registryURL string, inputs []string, includeDefaultRelays bool) ([]string, error) {
	if includeDefaultRelays {
		relayURLs := sdk.WithDefaultRelayURLs(ctx, registryURL, inputs...)
		if len(relayURLs) == 0 {
			return nil, nil
		}
		return relayURLs, nil
	}
	return utils.NormalizeRelayURLs(inputs)
}

func defaultExposeName(target, clientID string) (string, error) {
	trimmed := strings.TrimSpace(clientID)
	if trimmed == "" {
		trimmed = "relay"
	}
	if cut, ok := strings.CutPrefix(trimmed, "cli_"); ok {
		trimmed = cut
	}
	if len(trimmed) > shortClientIDSuffixLen {
		trimmed = trimmed[:shortClientIDSuffixLen]
	}
	if trimmed == "" {
		trimmed = "relay"
	}

	port := "app"
	if _, rawPort, err := net.SplitHostPort(target); err == nil {
		if rawPort != "" {
			port = rawPort
		}
	}

	return utils.NormalizeDNSLabel("app-" + port + "-" + trimmed)
}

func printRootUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  portal expose [flags] <target>")
	fmt.Fprintln(w, "  portal list [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  portal expose 3000")
	fmt.Fprintln(w, "  portal expose --name my-app localhost:8080")
	fmt.Fprintln(w, "  portal list")
}

func printExposeUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  portal expose [flags] <target>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  portal expose 3000")
	fmt.Fprintln(w, "  portal expose --name my-app localhost:8080")
	fmt.Fprintln(w, "  portal expose --relays https://portal.example.com --default-relays=false 3000")
}

func printListUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  portal list [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  portal list")
	fmt.Fprintln(w, "  portal list --relays https://portal.example.com --default-relays=false")
}
