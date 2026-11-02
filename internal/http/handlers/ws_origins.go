package handlers

import (
	"net/url"
	"strings"
)

// wsOriginPatterns turns the service's CORS origins ("https://riderapp.example.com") into the
// host patterns coder/websocket matches the handshake Origin against ("riderapp.example.com").
// The library compares hosts only, so passing the CORS list unchanged refused every browser
// connection. An entry without a scheme is taken as a host pattern already; an empty list falls
// back to "*" (dev convenience).
func wsOriginPatterns(origins []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, o := range origins {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		host := o
		if strings.Contains(o, "://") {
			u, err := url.Parse(o)
			if err != nil || u.Host == "" {
				continue
			}
			host = u.Host
		}
		if !seen[host] {
			seen[host] = true
			out = append(out, host)
		}
	}
	if len(out) == 0 {
		return []string{"*"}
	}
	return out
}
