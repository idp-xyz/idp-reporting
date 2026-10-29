// Package embedding is the platform's trusted configuration of who may show its embed pages in a frame (ADR 0006): for each
// integration, the exact origins of the product pages that may frame the embed entry points. It is read at startup from
// operator-controlled configuration and never from a request; a request can only be checked against it.
//
// An origin is held in exactly the form a browser serializes it (https://host[:port], the scheme and the host lower case, no
// default port, no path, no query, no fragment, no user information, no wildcard), because the check compares the text of the
// request's parent parameter with it and the value is written into a frame-ancestors source list: an origin that is not in that
// form is a configuration error, not something to be repaired. Plain http is accepted only for a loopback address and only when
// the deployment asks for it (the demo).
package embedding

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Limits keep the configuration, and the capabilities lookup that walks it, small and reviewable.
const (
	MaxIntegrations          = 32
	MaxOriginsPerIntegration = 16
	maxOriginBytes           = 255
	maxIntegrationBytes      = 128
)

// Options says what the deployment accepts beyond the default of https origins.
type Options struct {
	// LoopbackHTTP accepts http://<loopback address>[:port] origins; it exists for the local demo and the tests.
	LoopbackHTTP bool
}

// Config maps integrations to the origins that may frame their embed pages. The zero value and nil allow nothing.
type Config struct {
	origins map[string][]string
}

// New validates and copies the configuration. Every integration identifier and every origin must be valid and no origin may be
// listed twice for an integration; an integration with no origin is an error (leave it out instead).
func New(origins map[string][]string, options Options) (*Config, error) {
	if len(origins) > MaxIntegrations {
		return nil, fmt.Errorf("embedding: more than %d integrations", MaxIntegrations)
	}
	out := make(map[string][]string, len(origins))
	for integration, list := range origins {
		if !validIntegration(integration) {
			return nil, errors.New("embedding: an integration identifier is not valid (1-128 of A-Za-z0-9._-, starting with a letter or digit)")
		}
		if len(list) == 0 || len(list) > MaxOriginsPerIntegration {
			return nil, fmt.Errorf("embedding: integration %q needs 1-%d origins", integration, MaxOriginsPerIntegration)
		}
		for _, origin := range list {
			if err := validateOrigin(origin, options.LoopbackHTTP); err != nil {
				return nil, fmt.Errorf("embedding: integration %q: %w", integration, err)
			}
			if slices.Contains(out[integration], origin) {
				return nil, fmt.Errorf("embedding: integration %q lists %q twice", integration, origin)
			}
			out[integration] = append(out[integration], origin)
		}
	}
	return &Config{origins: out}, nil
}

// Parse reads repeated integration=origin values (the demo's -embed-origin flag) into a Config. An empty list is a valid,
// empty configuration.
func Parse(values []string, options Options) (*Config, error) {
	grouped := map[string][]string{}
	for _, value := range values {
		integration, origin, found := strings.Cut(value, "=")
		if !found || integration == "" || origin == "" {
			return nil, errors.New("embedding: an embed origin must be written integration=origin")
		}
		grouped[integration] = append(grouped[integration], origin)
	}
	return New(grouped, options)
}

// Allows reports whether the exact origin may frame the embed pages of the integration. It is false for an unknown
// integration, for an origin of another integration and for any text that is not an origin on the integration's list.
func (c *Config) Allows(integration, origin string) bool {
	if c == nil {
		return false
	}
	list, known := c.origins[integration]
	return known && slices.Contains(list, origin)
}

// Configured reports whether embedding is configured for at least one of the integrations (those of an authenticated caller).
func (c *Config) Configured(integrations []string) bool {
	if c == nil {
		return false
	}
	for _, integration := range integrations {
		if len(c.origins[integration]) > 0 {
			return true
		}
	}
	return false
}

// Integrations lists the integrations that have origins, sorted: for a deployment to check them against its own connections.
func (c *Config) Integrations() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.origins))
	for integration := range c.origins {
		out = append(out, integration)
	}
	slices.Sort(out)
	return out
}

func validIntegration(id string) bool {
	if id == "" || len(id) > maxIntegrationBytes {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || c != '.' && c != '_' && c != '-') {
			return false
		}
	}
	return true
}

// validateOrigin accepts only the serialized origin of an https page (or of a loopback http page when asked): see the package comment.
func validateOrigin(origin string, loopbackHTTP bool) error {
	if origin == "" || len(origin) > maxOriginBytes {
		return errors.New("an origin is empty or too long")
	}
	for i := 0; i < len(origin); i++ {
		if c := origin[i]; c <= 0x20 || c >= 0x7f || c == '*' || c == '\\' || c == '%' || c == ',' || c == ';' || c == '\'' || c == '"' {
			return errors.New("an origin has a character no origin may have")
		}
	}
	scheme, rest, found := strings.Cut(origin, "://")
	if !found || (scheme != "https" && scheme != "http") {
		return errors.New("an origin must be https://host[:port] exactly (lower case, no path, query, fragment or user information)")
	}
	if strings.ContainsAny(rest, "/?#@") || rest == "" || strings.HasSuffix(rest, ":") {
		return errors.New("an origin must be https://host[:port] exactly (lower case, no path, query, fragment or user information)")
	}
	// The host part is a bracketed IPv6 literal, an IPv4 literal or a DNS name, optionally followed by :port.
	host, port := rest, ""
	if strings.HasPrefix(rest, "[") {
		end := strings.Index(rest, "]")
		if end < 0 {
			return errors.New("an origin has an unterminated IPv6 literal")
		}
		host = rest[1:end]
		switch tail := rest[end+1:]; {
		case tail == "":
		case strings.HasPrefix(tail, ":"):
			port = tail[1:]
		default:
			return errors.New("an origin has text after its IPv6 literal")
		}
		address, err := netip.ParseAddr(host)
		if err != nil || !address.Is6() || address.Is4In6() || address.Zone() != "" || address.String() != host {
			return errors.New("an origin has an IPv6 literal that is not in canonical form")
		}
		if scheme == "http" && !address.IsLoopback() {
			return errors.New("http origins are accepted for loopback addresses only")
		}
	} else {
		name, tail, hasPort := strings.Cut(rest, ":")
		host = name
		if hasPort {
			port = tail
		}
		if address, err := netip.ParseAddr(host); err == nil {
			if !address.Is4() || address.String() != host {
				return errors.New("an origin has an IP literal that is not in canonical form")
			}
			if scheme == "http" && !address.IsLoopback() {
				return errors.New("http origins are accepted for loopback addresses only")
			}
		} else if scheme == "http" {
			return errors.New("http origins are accepted for loopback IP addresses only")
		} else if !validHostname(host) {
			return errors.New("an origin has a host name that is not a lower-case ASCII DNS name")
		}
	}
	if scheme == "http" && !loopbackHTTP {
		return errors.New("http origins are not accepted here")
	}
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || strconv.Itoa(number) != port || number < 1 || number > 65535 {
			return errors.New("an origin has a port that is not a plain number from 1 to 65535")
		}
		if (scheme == "https" && number == 443) || (scheme == "http" && number == 80) {
			return errors.New("an origin must leave out its scheme's default port")
		}
	}
	// A last check through the URL parser: it must read back exactly the same origin and nothing else.
	parsed, err := url.Parse(origin)
	if err != nil || parsed.User != nil || parsed.Opaque != "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Scheme+"://"+parsed.Host != origin {
		return errors.New("an origin must be https://host[:port] exactly (lower case, no path, query, fragment or user information)")
	}
	return nil
}

// validHostname is a lower-case ASCII DNS name of letters, digits and inner hyphens in labels of 1-63 characters whose last label
// is not all digits (so that it can be read as nothing but a name, never as an IPv4 form).
func validHostname(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	last := labels[len(labels)-1]
	return strings.Trim(last, "0123456789") != ""
}
