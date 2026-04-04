package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/sdk"
	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

func main() {
	log.Logger = log.Output(zerolog.NewConsoleWriter())
	if err := utils.RunCommands(os.Args[1:], os.Stdout, os.Stderr, printRootUsage, map[string]utils.CommandFunc{
		"expose": runExposeCommand,
		"list":   runListCommand,
		"help":   runHelpCommand,
	}); err != nil {
		log.Error().Err(err).Msg("portal tunnel exited with error")
		os.Exit(1)
	}
}

type exposeFlags struct {
	relayCSV     string
	discovery    bool
	banMITM      bool
	identityPath string
	identityJSON string
	name         string
	desc         string
	tags         string
	owner        string
	thumbnail    string
	hide         bool
	targetAddr   string
	httpRoutes   []string
	udp          bool
	udpAddr      string
	tcp          bool
}

func runExposeCommand(args []string) error {
	flags := exposeFlags{}
	fs := utils.NewFlagSet("expose", printExposeUsage)

	utils.StringFlag(fs, &flags.relayCSV, "relays", "", "Additional Portal relay server API URLs (comma-separated; scheme omitted defaults to https)")
	utils.BoolFlag(fs, &flags.discovery, "discovery", true, "Include public registry relays and discover additional relay bootstraps")
	utils.BoolFlagEnv(fs, &flags.banMITM, "ban-mitm", true, "Ban relay when the MITM self-probe detects TLS termination", "BAN_MITM")
	utils.StringFlagEnv(fs, &flags.identityPath, "identity-path", "identity.json", "identity json file path", "IDENTITY_PATH")
	utils.StringFlagEnv(fs, &flags.identityJSON, "identity-json", "", "identity json payload; overrides --identity-path contents and is persisted there when both are set", "IDENTITY_JSON")
	utils.StringFlag(fs, &flags.name, "name", "", "Public hostname prefix (single DNS label); auto-generated when omitted")
	utils.StringFlag(fs, &flags.desc, "description", "", "Service description metadata")
	utils.StringFlag(fs, &flags.tags, "tags", "", "Service tags metadata (comma-separated)")
	utils.StringFlag(fs, &flags.owner, "owner", "", "Service owner metadata")
	utils.StringFlag(fs, &flags.thumbnail, "thumbnail", "", "Service thumbnail URL metadata")
	utils.BoolFlag(fs, &flags.hide, "hide", false, "Hide service from relay listing screens")
	utils.RepeatedStringFlag(fs, &flags.httpRoutes, "http-route", "HTTP route mapping in PATH=UPSTREAM form; repeat to aggregate multiple local HTTP services behind one public URL")
	utils.BoolFlagEnv(fs, &flags.udp, "udp", false, "Enable public UDP relay in addition to the default TCP relay", "UDP_ENABLED")
	utils.StringFlagEnv(fs, &flags.udpAddr, "udp-addr", "", "Local UDP target address for relayed datagrams (host:port or port only); defaults to the target when --udp is enabled", "UDP_ADDR")
	utils.BoolFlagEnv(fs, &flags.tcp, "tcp", false, "Request a dedicated TCP port on the relay for raw TCP services (no TLS; e.g., Minecraft, game servers)", "TCP_ENABLED")

	if err := utils.ParseFlagSet(fs, args, printExposeUsage); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	var err error
	flags.targetAddr, err = utils.OptionalSingleArg(fs.Args(), "target")
	if err != nil {
		printExposeUsage(os.Stderr)
		return err
	}
	switch {
	case flags.targetAddr == "" && len(flags.httpRoutes) == 0:
		printExposeUsage(os.Stderr)
		return errors.New("target or at least one --http-route is required")
	case flags.targetAddr != "" && len(flags.httpRoutes) > 0:
		printExposeUsage(os.Stderr)
		return errors.New("target cannot be combined with --http-route")
	case len(flags.httpRoutes) > 0 && flags.udp:
		printExposeUsage(os.Stderr)
		return errors.New("--udp cannot be combined with --http-route")
	}
	if flags.name == "" && strings.TrimSpace(flags.identityJSON) != "" {
		identity, err := utils.ParseIdentityJSON(flags.identityJSON)
		if err != nil {
			return fmt.Errorf("parse --identity-json: %w", err)
		}
		if strings.TrimSpace(identity.Name) != "" {
			flags.name = identity.Name
		}
	}
	if flags.name == "" {
		storedIdentity, err := utils.LoadIdentity(flags.identityPath)
		if err == nil && strings.TrimSpace(storedIdentity.Name) != "" {
			flags.name = storedIdentity.Name
		}
	}
	if flags.name == "" {
		defaultTarget := flags.targetAddr
		if defaultTarget == "" && len(flags.httpRoutes) > 0 {
			defaultTarget = strings.Join(flags.httpRoutes, ",")
		}
		flags.name, err = defaultExposeName(defaultTarget, utils.RandomID("cli_"))
		if err != nil {
			return fmt.Errorf("derive service name: %w", err)
		}
	}
	flags.name, err = utils.NormalizeDNSLabel(flags.name)
	if err != nil {
		return fmt.Errorf("invalid service name: %w", err)
	}
	ctx, stop := utils.SignalContext()
	defer stop()

	exposure, err := sdk.Expose(ctx, sdk.ExposeConfig{
		RelayURLs:    utils.SplitCSV(flags.relayCSV),
		IdentityPath: flags.identityPath,
		IdentityJSON: flags.identityJSON,
		Name:         flags.name,
		TargetAddr:   flags.targetAddr,
		UDPAddr:      flags.udpAddr,
		UDPEnabled:   flags.udp,
		TCPEnabled:   flags.tcp,
		BanMITM:      flags.banMITM,
		Discovery:    flags.discovery,
		Metadata: types.LeaseMetadata{
			Description: flags.desc,
			Tags:        utils.SplitCSV(flags.tags),
			Owner:       flags.owner,
			Thumbnail:   flags.thumbnail,
			Hide:        flags.hide,
		},
	})
	if err != nil {
		return fmt.Errorf("service %s: failed to start relays: %w", flags.name, err)
	}
	if len(flags.httpRoutes) > 0 {
		handler, err := newHTTPRouteHandler(flags.httpRoutes)
		if err != nil {
			_ = exposure.Close()
			return err
		}
		handler = newOGInjectHandler(handler)
		defer exposure.Close()
		return exposure.RunHTTP(ctx, handler, "")
	}
	if flags.targetAddr != "" && !flags.udp && !flags.tcp {
		handler, err := newHTTPRouteHandler([]string{"/=" + flags.targetAddr})
		if err != nil {
			_ = exposure.Close()
			return err
		}
		handler = newOGInjectHandler(handler)
		defer exposure.Close()
		return exposure.RunHTTP(ctx, handler, "")
	}
	return proxyExposure(ctx, exposure)
}

type listFlags struct {
	relayCSV      string
	defaultRelays bool
}

func runListCommand(args []string) error {
	flags := listFlags{}
	fs := utils.NewFlagSet("list", printListUsage)

	utils.StringFlag(fs, &flags.relayCSV, "relays", "", "Additional Portal relay server API URLs (comma-separated; scheme omitted defaults to https)")
	utils.BoolFlag(fs, &flags.defaultRelays, "default-relays", true, "Include public registry relays")

	if err := utils.ParseFlagSet(fs, args, printListUsage); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := utils.RequireNoArgs(fs.Args(), "list"); err != nil {
		printListUsage(os.Stderr)
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	relayInputs := utils.SplitCSV(flags.relayCSV)

	relayURLs, err := utils.ResolvePortalRelayURLs(ctx, relayInputs, flags.defaultRelays)
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

func runHelpCommand(args []string) error {
	if len(args) == 0 {
		printRootUsage(os.Stdout)
		return nil
	}
	if len(args) > 1 {
		printRootUsage(os.Stderr)
		return errors.New("only one help topic is supported")
	}

	switch strings.TrimSpace(args[0]) {
	case "", "help", "-h", "--help":
		printRootUsage(os.Stdout)
		return nil
	case "expose":
		printExposeUsage(os.Stdout)
		return nil
	case "list":
		printListUsage(os.Stdout)
		return nil
	default:
		printRootUsage(os.Stderr)
		return fmt.Errorf("unknown help topic %q", strings.TrimSpace(args[0]))
	}
}

var exposeNameOpeners = []string{
	"arcade", "bouncy", "bravo", "bubble", "candy", "cosmic", "dapper", "electric",
	"fancy", "fizzy", "flashy", "fuzzy", "gentle", "glitter", "golden", "happy",
	"hyper", "jazzy", "jolly", "lively", "lucky", "magic", "mellow", "minty",
	"misty", "moonlit", "mystic", "neon", "nova", "peppy", "pixel", "playful",
	"poppy", "rapid", "rocket", "rowdy", "snappy", "snazzy", "sparkly", "spicy",
	"sprightly", "starry", "sunny", "swift", "tangy", "tidy", "toasty", "turbo",
	"velvet", "vivid", "wavy", "whimsy", "wild", "wonky", "zany", "zesty",
}

var exposeNameCenters = []string{
	"alpaca", "badger", "banjo", "beacon", "biscuit", "capybara", "comet", "cricket",
	"dragon", "falcon", "feather", "fjord", "fox", "gadget", "gecko", "gizmo",
	"harbor", "heron", "iguana", "jelly", "koala", "lemur", "mango", "narwhal",
	"nebula", "noodle", "octopus", "otter", "panda", "pepper", "phoenix", "pickle",
	"puffin", "quokka", "radar", "ranger", "rocket", "scooter", "seahorse", "skylark",
	"sprocket", "starling", "sunbeam", "taco", "thimble", "tiger", "toucan", "triton",
	"walrus", "widget", "willow", "wombat", "yeti", "zeppelin", "zigzag", "zinnia",
}

var exposeNameClosers = []string{
	"arcade", "beacon", "boogie", "bounce", "burst", "cascade", "chorus", "dash",
	"disco", "drift", "echo", "fiesta", "flare", "flash", "flight", "flip",
	"glow", "groove", "jam", "jive", "launch", "loop", "march", "orbit",
	"parade", "party", "pulse", "quest", "rally", "riot", "ripple", "rodeo",
	"roll", "rush", "serenade", "shuffle", "signal", "sketch", "spark", "sprint",
	"starlight", "stride", "sway", "swoop", "twirl", "uplift", "vibe", "voyage",
	"whirl", "wink", "zap", "zenith", "zip", "zoom", "zest", "zone",
}

func defaultExposeName(target, rawSeed string) (string, error) {
	seed := strings.TrimSpace(rawSeed)
	if cut, ok := strings.CutPrefix(seed, "cli_"); ok {
		seed = cut
	}
	if seed == "" {
		seed = "portal"
	}

	sum := sha256.Sum256([]byte(seed + "|" + strings.TrimSpace(target)))
	label := strings.Join([]string{
		exposeNameOpeners[int(sum[0])%len(exposeNameOpeners)],
		exposeNameCenters[int(sum[1])%len(exposeNameCenters)],
		exposeNameClosers[int(sum[2])%len(exposeNameClosers)],
	}, "-")

	return utils.NormalizeDNSLabel(label)
}

func printRootUsage(w io.Writer) {
	utils.WriteCommandUsage(w,
		[]string{
			"portal expose [flags] <target>",
			"portal expose [flags] --http-route PATH=UPSTREAM [--http-route PATH=UPSTREAM]",
			"portal list [flags]",
		},
		[]string{
			"portal expose 3000",
			"portal expose localhost:8080 --name my-app",
			"portal expose --http-route /api=http://127.0.0.1:3001 --http-route /=http://127.0.0.1:5173 --name my-app",
			"portal expose 3000 --udp --udp-addr 127.0.0.1:5353",
			"portal list",
		},
	)
}

func printExposeUsage(w io.Writer) {
	utils.WriteCommandUsage(w,
		[]string{
			"portal expose [flags] <target>",
			"portal expose [flags] --http-route PATH=UPSTREAM [--http-route PATH=UPSTREAM]",
		},
		[]string{
			"portal expose 3000",
			"portal expose localhost:8080 --name my-app",
			"portal expose --http-route /api=http://127.0.0.1:3001 --http-route /=http://127.0.0.1:5173 --name my-app",
			"portal expose 3000 --udp --udp-addr 127.0.0.1:5353",
			"portal expose 3000 --ban-mitm",
			"portal expose 3000 --relays https://portal.example.com --discovery=false",
		},
	)
}

func printListUsage(w io.Writer) {
	utils.WriteCommandUsage(w,
		[]string{
			"portal list [flags]",
		},
		[]string{
			"portal list",
			"portal list --relays https://portal.example.com --default-relays=false",
		},
	)
}
