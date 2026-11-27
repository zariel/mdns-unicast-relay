# mdns-unicast-relay

`mdns-unicast-relay` makes a local mDNS responder's services discoverable by
receivers on routed networks, which cannot hear link-local multicast. It runs
beside the responder, in the same network namespace, and queries it for
chosen DNS-SD service types. It then sends the records of those services to
each receiver as an ordinary unicast UDP datagram.

The motivating case is an OpenThread Border Router in Kubernetes. OTBR
advertises its border agent (`_meshcop._udp`) only on its Thread
infrastructure link, while Home Assistant's Thread integration finds border
routers only through mDNS. Run as a sidecar in the OTBR pod, the relay
delivers that advertisement to Home Assistant over the pod network, so Home
Assistant needs no attachment to the Thread link.

## How it works

1. The relay joins the mDNS group (`ff02::fb`, and `224.0.0.251` when the
   interface has an IPv4 address) on `-interface` and, every `-interval`,
   sends a multicast query for each `-service`. The responder shares the
   host, so it hears the query through multicast loopback, and the relay
   hears its multicast answer the same way.
2. The relay keeps only responses whose source address belongs to this host,
   so it never relays other responders' adverts on the link, such as those a
   gateway's mDNS reflector repeats there.
3. It reduces each response to the services' PTR, SRV and TXT records and the
   A and AAAA records of the hosts their SRV records name, keeping each
   record's TTL and cache-flush bit, and sends the result to every
   `-target`. Targets are resolved on every send, so a Kubernetes Service
   name follows pod restarts.

The relay binds the group address rather than the wildcard, so the
responder's own port-5353 unicast traffic still reaches the responder.

Receivers cache the records like any other mDNS response, and they expire at
their TTLs if the relay stops. Keep `-interval` below half the shortest TTL
the responder uses; OTBR's address records last 120 seconds.

## Requirements and limits

- Receivers must accept mDNS responses that arrive by unicast from a
  non-link-local source and from a port other than 5353. RFC 6762 tells
  receivers to ignore these, but python-zeroconf, which Home Assistant uses,
  caches them.
- The relay only pushes answers. Receivers' own queries are not forwarded,
  and receivers learn of a change at the next query interval or when the
  responder announces it.
- NSEC records are dropped, because their rdata may carry names compressed
  against the original message.

## Usage

```sh
mdns-unicast-relay \
  -interface net1 \
  -service _meshcop._udp.local \
  -target home-assistant-mdns.default.svc.cluster.local:5353
```

| Flag | Meaning |
| --- | --- |
| `-interface` | Interface the local responder answers on |
| `-service` | DNS-SD service type to relay; repeatable |
| `-target` | `host:port` to send relayed responses to; repeatable |
| `-interval` | Query interval, default `1m` |
| `-debug` | Log each relayed response |

The container image is published as `ghcr.io/zariel/mdns-unicast-relay`. It
runs as a non-root user and needs no capabilities: port 5353 is unprivileged,
and the relay shares it with the responder through `SO_REUSEADDR` and
`SO_REUSEPORT`.

## Development

```sh
go test -race ./...
```

Releases are cut by Release Please from Conventional Commits on `main`.
