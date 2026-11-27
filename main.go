// Command mdns-unicast-relay queries a local mDNS responder for chosen
// DNS-SD service types and relays its answers as unicast datagrams to
// receivers on routed networks, which cannot hear link-local multicast.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var version = "dev"

type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }

func (l *listFlag) Set(v string) error {
	*l = append(*l, v)
	return nil
}

func main() {
	var (
		ifaceName   string
		services    listFlag
		targets     listFlag
		interval    time.Duration
		debug       bool
		showVersion bool
	)
	flag.StringVar(&ifaceName, "interface", "", "interface the local mDNS responder answers on")
	flag.Var(&services, "service", "DNS-SD service type to relay, such as _meshcop._udp.local (repeatable)")
	flag.Var(&targets, "target", "host:port to send relayed responses to (repeatable)")
	flag.DurationVar(&interval, "interval", time.Minute, "how often to query for the services; keep it below half the shortest record TTL")
	flag.BoolVar(&debug, "debug", false, "log each relayed response")
	flag.BoolVar(&showVersion, "version", false, "print the version and exit")
	flag.Parse()

	if showVersion {
		fmt.Println(version)
		return
	}

	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if err := run(ifaceName, services, targets, interval, log); err != nil {
		log.Error("exiting", "err", err)
		os.Exit(1)
	}
}

func run(ifaceName string, services, targets []string, interval time.Duration, log *slog.Logger) error {
	if ifaceName == "" || len(services) == 0 || len(targets) == 0 {
		return errors.New("-interface, -service and -target are required")
	}
	if interval <= 0 {
		return errors.New("-interval must be positive")
	}
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return err
	}
	for i, s := range services {
		services[i] = normalizeService(s)
	}

	r := &relay{
		iface:    iface,
		services: services,
		targets:  targets,
		interval: interval,
		log:      log,
		isLocal:  localAddress,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("relaying", "version", version, "interface", ifaceName, "services", services, "targets", targets, "interval", interval)
	return r.run(ctx)
}
