package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

// TestReceive feeds responses to the relay's receive loop and observes what
// reaches a target over UDP.
func TestReceive(t *testing.T) {
	services := []string{normalizeService("_meshcop._udp.local")}
	resp := borderRouterResponse(t)
	want, _, err := filter(resp, services)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		local   bool
		relayed bool
	}{
		{"local responder", true, true},
		{"remote responder", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, err := net.ListenPacket("udp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()

			in, err := net.ListenPacket("udp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			r := &relay{
				services: services,
				targets:  []string{target.LocalAddr().String()},
				log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
				isLocal:  func(net.IP) bool { return tc.local },
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- r.receive(ctx, in) }()

			sender, err := net.Dial("udp4", in.LocalAddr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer sender.Close()
			if _, err := sender.Write(resp); err != nil {
				t.Fatal(err)
			}

			buf := make([]byte, 9000)
			target.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			n, _, err := target.ReadFrom(buf)
			switch {
			case tc.relayed && err != nil:
				t.Fatalf("target received nothing: %v", err)
			case tc.relayed && !bytes.Equal(buf[:n], want):
				t.Errorf("target received %x, want the filtered response %x", buf[:n], want)
			case !tc.relayed && err == nil:
				t.Errorf("target received %d bytes, want nothing", n)
			}

			cancel()
			in.Close()
			if err := <-done; err != nil {
				t.Errorf("receive: %v", err)
			}
		})
	}
}
