package vpnwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"time"
)

// Endpoint is where a Clash API listens and how to authenticate.
type Endpoint struct {
	Base   string // http://127.0.0.1:9090
	Secret string
	Source string // where it was found, for logs and -vpncheck
}

// EndpointFunc finds the API anew on every call: engines regenerate their
// config (and often the secret) on restart.
type EndpointFunc func() (Endpoint, error)

// StaticEndpoint is used when the address is set in the bot settings.
func StaticEndpoint(controller, secret string) EndpointFunc {
	return func() (Endpoint, error) {
		base, err := baseURL(controller)
		return Endpoint{Base: base, Secret: secret, Source: "settings"}, err
	}
}

// baseURL turns "0.0.0.0:9090", ":9090" or "http://host:9090" into a URL
// the router itself can reach.
func baseURL(ctl string) (string, error) {
	if u, err := url.Parse(ctl); err == nil && u.Scheme != "" && u.Host != "" {
		ctl = u.Host
	}
	h, port, err := net.SplitHostPort(ctl)
	if err != nil {
		return "", fmt.Errorf("clash API address %q: %w", ctl, err)
	}
	// Listening on every address still answers on loopback.
	if h == "" || h == "0.0.0.0" || h == "::" || h == "[::]" {
		h = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(h, port), nil
}

// Clash talks to the Clash API that mihomo (Clash.Meta) and sing-box
// share: GET /proxies and GET /proxies/{name}/delay.
type Clash struct {
	Endpoint EndpointFunc
	HTTP     *http.Client
	Timeout  time.Duration // per delay test, default 8 s
}

func (c *Clash) get(ctx context.Context, path string, out any) error {
	ep, err := c.Endpoint()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.Base+path, nil)
	if err != nil {
		return err
	}
	if ep.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+ep.Secret)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &e) == nil && e.Message != "" {
			return errors.New(e.Message)
		}
		return fmt.Errorf("clash API %s: HTTP %d", path, resp.StatusCode)
	}
	return json.Unmarshal(body, out)
}

// groupTypes are the Clash API types of proxy groups; everything else
// (VLESS, Direct, Reject...) is a single outbound.
var groupTypes = map[string]bool{
	"Selector": true, "URLTest": true, "Fallback": true, "LoadBalance": true, "Relay": true,
}

// global is the built-in group that lists every outbound in both engines.
const global = "GLOBAL"

type proxyInfo struct {
	Type string   `json:"type"`
	All  []string `json:"all"`
}

// Groups returns the top-level proxy groups: those no other group contains.
// That is one entry per route the user set up (a ZeroBlock section, a
// mihomo "Proxy" selector) without its nested auto-select pools.
func (c *Clash) Groups(ctx context.Context) ([]string, error) {
	var r struct {
		Proxies map[string]proxyInfo `json:"proxies"`
	}
	if err := c.get(ctx, "/proxies", &r); err != nil {
		return nil, err
	}
	return topGroups(r.Proxies), nil
}

func topGroups(proxies map[string]proxyInfo) []string {
	nested := map[string]bool{}
	for name, p := range proxies {
		if name == global || !groupTypes[p.Type] {
			continue
		}
		for _, m := range p.All {
			nested[m] = true
		}
	}
	var out []string
	for name, p := range proxies {
		if name != global && groupTypes[p.Type] && !nested[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Delay makes the engine fetch u through the group's current outbound.
func (c *Clash) Delay(ctx context.Context, group, u string) (time.Duration, error) {
	t := c.Timeout
	if t <= 0 {
		t = 8 * time.Second
	}
	q := url.Values{"url": {u}, "timeout": {fmt.Sprint(t.Milliseconds())}}
	path := "/proxies/" + url.PathEscape(group) + "/delay?" + q.Encode()
	ctx, cancel := context.WithTimeout(ctx, t+3*time.Second)
	defer cancel()
	var r struct {
		Delay int `json:"delay"`
	}
	if err := c.get(ctx, path, &r); err != nil {
		return 0, err
	}
	return time.Duration(r.Delay) * time.Millisecond, nil
}
