package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"syscall"
	"time"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"
)

const mdnsPort = 5353

var (
	groupIPv4 = net.IPv4(224, 0, 0, 251)
	groupIPv6 = net.ParseIP("ff02::fb")
)

type relay struct {
	iface    *net.Interface
	services []string
	targets  []string
	interval time.Duration
	log      *slog.Logger

	// isLocal reports whether a response's source address belongs to this
	// host, so only the local responder's answers are relayed.
	isLocal func(net.IP) bool
}

// groupConn is one address family's membership of the mDNS group on the
// relay's interface.
type groupConn struct {
	conn  net.PacketConn
	group *net.UDPAddr
}

func (r *relay) run(ctx context.Context) error {
	q, err := query(r.services)
	if err != nil {
		return fmt.Errorf("build query: %w", err)
	}

	conns, err := r.join()
	if err != nil {
		return err
	}
	defer func() {
		for _, c := range conns {
			c.conn.Close()
		}
	}()

	errc := make(chan error, len(conns))
	for _, c := range conns {
		go func() { errc <- r.receive(ctx, c.conn) }()
	}

	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		for _, c := range conns {
			if _, err := c.conn.WriteTo(q, c.group); err != nil {
				r.log.Warn("send query", "group", c.group, "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case err := <-errc:
			return err
		case <-t.C:
		}
	}
}

// join opens a socket in the mDNS group for each address family the interface
// has an address in.
//
// Each socket binds the group address rather than the wildcard. The local
// responder already holds port 5353, and a wildcard bind would share its
// unicast traffic; a group bind receives only datagrams sent to the group.
// The kernel still picks an interface source address for queries sent from
// it, so responders treat them as ordinary port-5353 multicast queries.
func (r *relay) join() ([]groupConn, error) {
	addrs, err := r.iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("list %s addresses: %w", r.iface.Name, err)
	}
	var has4, has6 bool
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if n.IP.To4() != nil {
				has4 = true
			} else {
				has6 = true
			}
		}
	}

	var conns []groupConn
	closeAll := func() {
		for _, c := range conns {
			c.conn.Close()
		}
	}
	if has6 {
		c, err := r.join6()
		if err != nil {
			closeAll()
			return nil, err
		}
		conns = append(conns, c)
	}
	if has4 {
		c, err := r.join4()
		if err != nil {
			closeAll()
			return nil, err
		}
		conns = append(conns, c)
	}
	if len(conns) == 0 {
		return nil, fmt.Errorf("%s has no IP addresses", r.iface.Name)
	}
	return conns, nil
}

func (r *relay) join6() (groupConn, error) {
	group := &net.UDPAddr{IP: groupIPv6, Port: mdnsPort, Zone: r.iface.Name}
	conn, err := listenShared("udp6", group.String())
	if err != nil {
		return groupConn{}, err
	}
	p := ipv6.NewPacketConn(conn)
	err = errors.Join(
		p.JoinGroup(r.iface, &net.UDPAddr{IP: groupIPv6}),
		p.SetMulticastInterface(r.iface),
		p.SetMulticastHopLimit(255),
		// The responder shares this host, so it hears queries only through
		// multicast loopback.
		p.SetMulticastLoopback(true),
	)
	if err != nil {
		conn.Close()
		return groupConn{}, fmt.Errorf("join %s: %w", group, err)
	}
	return groupConn{conn: conn, group: group}, nil
}

func (r *relay) join4() (groupConn, error) {
	group := &net.UDPAddr{IP: groupIPv4, Port: mdnsPort}
	conn, err := listenShared("udp4", group.String())
	if err != nil {
		return groupConn{}, err
	}
	p := ipv4.NewPacketConn(conn)
	err = errors.Join(
		p.JoinGroup(r.iface, &net.UDPAddr{IP: groupIPv4}),
		p.SetMulticastInterface(r.iface),
		p.SetMulticastTTL(255),
		p.SetMulticastLoopback(true),
	)
	if err != nil {
		conn.Close()
		return groupConn{}, fmt.Errorf("join %s on %s: %w", group, r.iface.Name, err)
	}
	return groupConn{conn: conn, group: group}, nil
}

// listenShared binds address alongside the local responder's own socket.
func listenShared(network, address string) (net.PacketConn, error) {
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			serr = errors.Join(
				unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1),
				unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1),
			)
		})
		return errors.Join(err, serr)
	}}
	conn, err := lc.ListenPacket(context.Background(), network, address)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", address, err)
	}
	return conn, nil
}

func (r *relay) receive(ctx context.Context, conn net.PacketConn) error {
	buf := make([]byte, 9000)
	for {
		n, src, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read: %w", err)
		}
		udp, ok := src.(*net.UDPAddr)
		if !ok || !r.isLocal(udp.IP) {
			continue
		}
		r.handle(ctx, buf[:n], udp)
	}
}

// handle relays the services' records from a local response to every target.
func (r *relay) handle(ctx context.Context, msg []byte, src *net.UDPAddr) {
	out, ok, err := filter(msg, r.services)
	if err != nil {
		r.log.Debug("ignore malformed message", "src", src, "err", err)
		return
	}
	if !ok {
		return
	}
	for _, t := range r.targets {
		if err := send(ctx, t, out); err != nil {
			r.log.Warn("relay response", "target", t, "err", err)
			continue
		}
		r.log.Debug("relayed response", "src", src, "target", t, "bytes", len(out))
	}
}

// send delivers msg to target, resolving its name each time so a Service or
// pod address change is followed. The datagram leaves from an ephemeral port;
// receivers must accept mDNS responses from ports other than 5353.
func send(ctx context.Context, target string, msg []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", target)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write(msg)
	return err
}

// localAddress reports whether ip is assigned to an interface of this host.
func localAddress(ip net.IP) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}
