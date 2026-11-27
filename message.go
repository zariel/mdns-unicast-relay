package main

import (
	"errors"
	"strings"

	"golang.org/x/net/dns/dnsmessage"
)

// normalizeService returns name in the form message names are compared in:
// lower case with a trailing dot.
func normalizeService(name string) string {
	name = strings.ToLower(name)
	if !strings.HasSuffix(name, ".") {
		name += "."
	}
	return name
}

// under reports whether name is service itself or an instance of it.
func under(name, service string) bool {
	name = strings.ToLower(name)
	return name == service || strings.HasSuffix(name, "."+service)
}

// query builds a multicast (QM) query for the PTR records of services.
func query(services []string) ([]byte, error) {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{})
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	for _, s := range services {
		name, err := dnsmessage.NewName(s)
		if err != nil {
			return nil, err
		}
		q := dnsmessage.Question{Name: name, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET}
		if err := b.Question(q); err != nil {
			return nil, err
		}
	}
	return b.Finish()
}

type section int

const (
	answers section = iota
	additionals
)

type record struct {
	section  section
	resource dnsmessage.Resource
}

// filter reduces an mDNS response to the records describing services: those
// named by a service or one of its instances, and the A and AAAA records of
// the hosts those instances' SRV records name. Records keep their section,
// TTL and cache-flush bit. ok is false when msg is a query or holds nothing
// to relay.
func filter(msg []byte, services []string) (out []byte, ok bool, err error) {
	var p dnsmessage.Parser
	h, err := p.Start(msg)
	if err != nil {
		return nil, false, err
	}
	if !h.Response {
		return nil, false, nil
	}
	if err := p.SkipAllQuestions(); err != nil {
		return nil, false, err
	}

	var records []record
	for {
		r, err := p.Answer()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			break
		}
		if err != nil {
			return nil, false, err
		}
		records = append(records, record{answers, r})
	}
	if err := p.SkipAllAuthorities(); err != nil {
		return nil, false, err
	}
	for {
		r, err := p.Additional()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			break
		}
		if err != nil {
			return nil, false, err
		}
		records = append(records, record{additionals, r})
	}

	keep := make([]bool, len(records))
	hosts := map[string]bool{}
	for i, r := range records {
		if !relayedType(r.resource.Header.Type) || !underAny(r.resource.Header.Name.String(), services) {
			continue
		}
		keep[i] = true
		if srv, ok := r.resource.Body.(*dnsmessage.SRVResource); ok {
			hosts[strings.ToLower(srv.Target.String())] = true
		}
	}
	for i, r := range records {
		t := r.resource.Header.Type
		if (t == dnsmessage.TypeA || t == dnsmessage.TypeAAAA) && hosts[strings.ToLower(r.resource.Header.Name.String())] {
			keep[i] = true
		}
	}

	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{Response: true, Authoritative: true})
	b.EnableCompression()
	kept := 0
	for _, s := range []section{answers, additionals} {
		var err error
		if s == answers {
			err = b.StartAnswers()
		} else {
			err = b.StartAdditionals()
		}
		if err != nil {
			return nil, false, err
		}
		for i, r := range records {
			if !keep[i] || r.section != s {
				continue
			}
			if err := appendResource(&b, r.resource); err != nil {
				return nil, false, err
			}
			kept++
		}
	}
	if kept == 0 {
		return nil, false, nil
	}
	out, err = b.Finish()
	return out, err == nil, err
}

// relayedType reports whether records of type t describe a service instance.
// NSEC is left out: its rdata may hold a name compressed against the original
// message, which cannot be copied into a new one.
func relayedType(t dnsmessage.Type) bool {
	switch t {
	case dnsmessage.TypePTR, dnsmessage.TypeSRV, dnsmessage.TypeTXT, dnsmessage.TypeA, dnsmessage.TypeAAAA:
		return true
	}
	return false
}

func underAny(name string, services []string) bool {
	for _, s := range services {
		if under(name, s) {
			return true
		}
	}
	return false
}

func appendResource(b *dnsmessage.Builder, r dnsmessage.Resource) error {
	switch body := r.Body.(type) {
	case *dnsmessage.PTRResource:
		return b.PTRResource(r.Header, *body)
	case *dnsmessage.SRVResource:
		return b.SRVResource(r.Header, *body)
	case *dnsmessage.TXTResource:
		return b.TXTResource(r.Header, *body)
	case *dnsmessage.AResource:
		return b.AResource(r.Header, *body)
	case *dnsmessage.AAAAResource:
		return b.AAAAResource(r.Header, *body)
	}
	return errors.New("unsupported record type " + r.Header.Type.String())
}
