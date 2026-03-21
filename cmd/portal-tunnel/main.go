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
	relayCSV         string
	defaultRelays    bool
	discoveryEnabled bool
	privateKey       string
	name             string
	desc             string
	tags             string
	owner            string
	thumbnail        string
	hide             bool
	targetAddr       string
	udp              bool
	udpAddr          string
}

func runExposeCommand(args []string) error {
	flags := exposeFlags{}
	fs := utils.NewFlagSet("expose", printExposeUsage)

	utils.StringFlag(fs, &flags.relayCSV, "relays", "", "Additional Portal relay server API URLs (comma-separated; scheme omitted defaults to https)")
	utils.BoolFlag(fs, &flags.defaultRelays, "default-relays", true, "Include public registry relays")
	utils.BoolFlag(fs, &flags.discoveryEnabled, "discovery", false, "Advertise known relay URLs and discover additional relay bootstraps")
	utils.StringFlag(fs, &flags.privateKey, "private-key", "", "Owner private key used to derive a discovery address")
	utils.StringFlag(fs, &flags.name, "name", "", "Public hostname prefix (single DNS label); auto-generated when omitted")
	utils.StringFlag(fs, &flags.desc, "description", "", "Service description metadata")
	utils.StringFlag(fs, &flags.tags, "tags", "", "Service tags metadata (comma-separated)")
	utils.StringFlag(fs, &flags.owner, "owner", "", "Service owner metadata")
	utils.StringFlag(fs, &flags.thumbnail, "thumbnail", "", "Service thumbnail URL metadata")
	utils.BoolFlag(fs, &flags.hide, "hide", false, "Hide service from discovery")
	utils.BoolFlagEnv(fs, &flags.udp, "udp", false, "Enable public UDP relay in addition to the default TCP relay", "UDP_ENABLED")
	utils.StringFlagEnv(fs, &flags.udpAddr, "udp-addr", "", "Local UDP target address for relayed datagrams (host:port or port only); defaults to the target when --udp is enabled", "UDP_ADDR")

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
	if flags.targetAddr == "" {
		printExposeUsage(os.Stderr)
		return errors.New("target is required")
	}
	if flags.name == "" {
		flags.name, err = defaultExposeName(flags.targetAddr, utils.RandomID("cli_"))
		if err != nil {
			return fmt.Errorf("derive service name: %w", err)
		}
	}

	ctx, stop := utils.SignalContext()
	defer stop()

	exposure, err := sdk.Expose(ctx, sdk.ExposeConfig{
		RelayURLs:           utils.SplitCSV(flags.relayCSV),
		DefaultRelayEnabled: flags.defaultRelays,
		Name:                flags.name,
		TargetAddr:          flags.targetAddr,
		UDPAddr:             flags.udpAddr,
		UDPEnabled:          flags.udp,
		Discovery:           flags.discoveryEnabled,
		Metadata: types.LeaseMetadata{
			Description: flags.desc,
			Tags:        utils.SplitCSV(flags.tags),
			Owner:       flags.owner,
			Thumbnail:   flags.thumbnail,
			Hide:        flags.hide,
		},
		OwnerPrivateKey: flags.privateKey,
	})
	if err != nil {
		return fmt.Errorf("service %s: failed to start relays: %w", flags.name, err)
	}
	return proxyExposure(ctx, exposure, flags.name)
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

	relayURLs, err := sdk.ResolveRelayURLs(ctx, relayInputs, flags.defaultRelays)
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
			"portal list [flags]",
		},
		[]string{
			"portal expose 3000",
			"portal expose --name my-app localhost:8080",
			"portal expose --udp --udp-addr 127.0.0.1:5353 3000",
			"portal list",
		},
	)
}

func printExposeUsage(w io.Writer) {
	utils.WriteCommandUsage(w,
		[]string{
			"portal expose [flags] <target>",
		},
		[]string{
			"portal expose 3000",
			"portal expose --name my-app localhost:8080",
			"portal expose --udp --udp-addr 127.0.0.1:5353 3000",
			"portal expose --relays https://portal.example.com --default-relays=false 3000",
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
