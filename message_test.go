package main

import (
	"errors"
	"net"
	"slices"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

const cacheFlush = dnsmessage.ClassINET | 0x8000

type rr struct {
	section section
	name    string
	typ     dnsmessage.Type
}

func mustName(t *testing.T, s string) dnsmessage.Name {
	t.Helper()
	n, err := dnsmessage.NewName(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// borderRouterResponse resembles OTBR's answer to a gateway's combined query:
// a Thread border agent alongside an unrelated Matter node and its host.
func borderRouterResponse(t *testing.T) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{Response: true, Authoritative: true})
	b.EnableCompression()
	hdr := func(name string, typ dnsmessage.Type, class dnsmessage.Class, ttl uint32) dnsmessage.ResourceHeader {
		return dnsmessage.ResourceHeader{Name: mustName(t, name), Type: typ, Class: class, TTL: ttl}
	}
	addr := func(s string) [16]byte { return [16]byte(net.ParseIP(s).To16()) }

	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(b.StartAnswers())
	check(b.PTRResource(hdr("_meshcop._udp.local.", dnsmessage.TypePTR, dnsmessage.ClassINET, 4500),
		dnsmessage.PTRResource{PTR: mustName(t, "OpenThread BorderRouter #D0EB._meshcop._udp.local.")}))
	check(b.PTRResource(hdr("_matter._tcp.local.", dnsmessage.TypePTR, dnsmessage.ClassINET, 4500),
		dnsmessage.PTRResource{PTR: mustName(t, "149491B946195377-0000000000000004._matter._tcp.local.")}))
	check(b.StartAdditionals())
	check(b.SRVResource(hdr("OpenThread BorderRouter #D0EB._meshcop._udp.local.", dnsmessage.TypeSRV, cacheFlush, 120),
		dnsmessage.SRVResource{Port: 49154, Target: mustName(t, "otc21c624d2a01d0eb.local.")}))
	check(b.TXTResource(hdr("OpenThread BorderRouter #D0EB._meshcop._udp.local.", dnsmessage.TypeTXT, cacheFlush, 4500),
		dnsmessage.TXTResource{TXT: []string{"nn=home-thread", "rv=1"}}))
	check(b.AAAAResource(hdr("OTC21C624D2A01D0EB.local.", dnsmessage.TypeAAAA, cacheFlush, 120),
		dnsmessage.AAAAResource{AAAA: addr("fd74:f571:d3bd:1060::2")}))
	check(b.SRVResource(hdr("149491B946195377-0000000000000004._matter._tcp.local.", dnsmessage.TypeSRV, cacheFlush, 120),
		dnsmessage.SRVResource{Port: 5540, Target: mustName(t, "42D059C95CB28957.local.")}))
	check(b.AAAAResource(hdr("42D059C95CB28957.local.", dnsmessage.TypeAAAA, cacheFlush, 120),
		dnsmessage.AAAAResource{AAAA: addr("fd42:dfb5:24e4:1:8cd8:d422:728:791e")}))
	msg, err := b.Finish()
	check(err)
	return msg
}

func parseAll(t *testing.T, msg []byte) (dnsmessage.Header, []rr, []dnsmessage.Resource) {
	t.Helper()
	var p dnsmessage.Parser
	h, err := p.Start(msg)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SkipAllQuestions(); err != nil {
		t.Fatal(err)
	}
	var got []rr
	var resources []dnsmessage.Resource
	next := []func() (dnsmessage.Resource, error){p.Answer, nil, p.Additional}
	for s, read := range next {
		if read == nil {
			if err := p.SkipAllAuthorities(); err != nil {
				t.Fatal(err)
			}
			continue
		}
		for {
			r, err := read()
			if errors.Is(err, dnsmessage.ErrSectionDone) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			sec := answers
			if s == 2 {
				sec = additionals
			}
			got = append(got, rr{sec, r.Header.Name.String(), r.Header.Type})
			resources = append(resources, r)
		}
	}
	return h, got, resources
}

func TestFilter(t *testing.T) {
	services := []string{normalizeService("_MESHCOP._udp.local")}

	out, ok, err := filter(borderRouterResponse(t), services)
	if err != nil || !ok {
		t.Fatalf("filter: ok=%v err=%v", ok, err)
	}

	h, got, resources := parseAll(t, out)
	if !h.Response || !h.Authoritative {
		t.Errorf("header = %+v, want an authoritative response", h)
	}
	want := []rr{
		{answers, "_meshcop._udp.local.", dnsmessage.TypePTR},
		{additionals, "OpenThread BorderRouter #D0EB._meshcop._udp.local.", dnsmessage.TypeSRV},
		{additionals, "OpenThread BorderRouter #D0EB._meshcop._udp.local.", dnsmessage.TypeTXT},
		{additionals, "OTC21C624D2A01D0EB.local.", dnsmessage.TypeAAAA},
	}
	if !slices.Equal(got, want) {
		t.Errorf("records:\n got %v\nwant %v", got, want)
	}

	srv := resources[1]
	if srv.Header.TTL != 120 || srv.Header.Class != cacheFlush {
		t.Errorf("SRV TTL=%d class=%#x, want 120 and the cache-flush bit", srv.Header.TTL, uint16(srv.Header.Class))
	}
	if txt := resources[2].Body.(*dnsmessage.TXTResource).TXT; !slices.Equal(txt, []string{"nn=home-thread", "rv=1"}) {
		t.Errorf("TXT = %q", txt)
	}
}

func TestFilterIgnores(t *testing.T) {
	services := []string{normalizeService("_meshcop._udp.local")}

	q, err := query(services)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := filter(q, services); ok || err != nil {
		t.Errorf("query: ok=%v err=%v, want ignored", ok, err)
	}

	other := []string{normalizeService("_hap._tcp.local")}
	if _, ok, err := filter(borderRouterResponse(t), other); ok || err != nil {
		t.Errorf("response without the service: ok=%v err=%v, want ignored", ok, err)
	}

	// A name that only ends with the service's text is not an instance of it.
	near := []string{normalizeService("cop._udp.local")}
	if _, ok, err := filter(borderRouterResponse(t), near); ok || err != nil {
		t.Errorf("label-suffix match: ok=%v err=%v, want ignored", ok, err)
	}
}

func TestQuery(t *testing.T) {
	q, err := query([]string{"_meshcop._udp.local.", "_trel._udp.local."})
	if err != nil {
		t.Fatal(err)
	}

	var p dnsmessage.Parser
	h, err := p.Start(q)
	if err != nil {
		t.Fatal(err)
	}
	if h.Response || h.ID != 0 {
		t.Errorf("header = %+v, want a query with ID 0", h)
	}
	qs, err := p.AllQuestions()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, q := range qs {
		// The top bit of the class requests a unicast reply, which would go
		// to the relay's group socket's port on the responder's own host.
		if q.Type != dnsmessage.TypePTR || q.Class != dnsmessage.ClassINET {
			t.Errorf("question %v: type %v class %#x, want a PTR QM question", q.Name, q.Type, uint16(q.Class))
		}
		got = append(got, q.Name.String())
	}
	if want := []string{"_meshcop._udp.local.", "_trel._udp.local."}; !slices.Equal(got, want) {
		t.Errorf("questions = %v, want %v", got, want)
	}
}
