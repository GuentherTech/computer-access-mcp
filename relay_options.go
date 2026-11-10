package main

import (
	"errors"
	"flag"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/GuentherTech/computer-access-mcp/internal/link"
)

type relayOptions struct {
	listen, publicURL, data string
	maxDevices              int
}

func parseRelayOptions(args []string) (relayOptions, error) {
	options := relayOptions{listen: "127.0.0.1:8788", data: "relay-data/registry.json", maxDevices: 1000}
	if port := os.Getenv("PORT"); port != "" {
		options.listen = net.JoinHostPort("0.0.0.0", port)
	}
	options.publicURL = os.Getenv("RELAY_PUBLIC_URL")
	if options.publicURL == "" {
		if domain := os.Getenv("RAILWAY_PUBLIC_DOMAIN"); domain != "" {
			options.publicURL = "https://" + domain
		}
	}
	volume := os.Getenv("RAILWAY_VOLUME_MOUNT_PATH")
	if volume != "" {
		options.data = filepath.Join(volume, "registry.json")
	}
	flags := flag.NewFlagSet("relay", flag.ContinueOnError)
	flags.StringVar(&options.listen, "listen", options.listen, "HTTP listener; defaults to 0.0.0.0:PORT when PORT is set")
	flags.StringVar(&options.publicURL, "public-url", options.publicURL, "Public HTTPS origin; defaults to RELAY_PUBLIC_URL or Railway's public domain")
	flags.StringVar(&options.data, "data", options.data, "Device registry; defaults to registry.json on the Railway volume")
	flags.IntVar(&options.maxDevices, "max-devices", options.maxDevices, "Maximum enrolled devices")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if len(flags.Args()) != 0 {
		return options, errors.New("unexpected relay arguments")
	}
	if os.Getenv("RAILWAY_SERVICE_ID") != "" && volume == "" {
		return options, errors.New("attach a Railway volume at /data before starting the relay; the device registry must persist across deployments")
	}
	host, port, err := net.SplitHostPort(options.listen)
	if err != nil {
		return options, err
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 0 || number > 65535 {
		return options, errors.New("relay listener port must be between 0 and 65535")
	}
	if options.publicURL == "" && !link.IsLoopback(host) {
		return options, errors.New("generate a Railway public domain, set RELAY_PUBLIC_URL, or pass -public-url for a public listener")
	}
	if options.publicURL != "" {
		base, err := link.BaseURL(options.publicURL)
		if err != nil {
			return options, err
		}
		if !strings.HasPrefix(base, "https://") && !link.IsLoopback(host) {
			return options, errors.New("public relays require HTTPS termination")
		}
		options.publicURL = base
	}
	if options.maxDevices < 1 {
		return options, errors.New("max-devices must be positive")
	}
	return options, nil
}
