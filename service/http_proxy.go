package service

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

// NewHTTPClient builds the client every outbound request should go through.
// A desktop launch inherits no HTTP_PROXY, so without SystemProxy the app would
// connect directly while the user's browser goes through their local proxy.
func NewHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = SystemProxy
	return &http.Client{Timeout: timeout, Transport: transport}
}

// SystemProxy resolves the proxy for a request: the environment wins, and the
// platform's own proxy configuration is the fallback.
func SystemProxy(request *http.Request) (*url.URL, error) {
	proxy, err := http.ProxyFromEnvironment(request)
	if err != nil || proxy != nil {
		return proxy, err
	}
	config := platformProxyConfig()
	if config == nil {
		return nil, nil
	}
	return config.ProxyFunc()(request.URL)
}

// parseProxyServer reads the Windows ProxyServer value, which is either a bare
// "host:port" used for every scheme or a "scheme=host:port;..." list.
func parseProxyServer(value string) (httpProxy, httpsProxy string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ""
	}
	if !strings.Contains(value, "=") {
		return value, value
	}
	for _, entry := range strings.Split(value, ";") {
		scheme, address, found := strings.Cut(strings.TrimSpace(entry), "=")
		if !found || strings.TrimSpace(address) == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(scheme)) {
		case "http":
			httpProxy = strings.TrimSpace(address)
		case "https":
			httpsProxy = strings.TrimSpace(address)
		}
	}
	return httpProxy, httpsProxy
}

// parseProxyOverride converts the Windows bypass list into NO_PROXY syntax.
func parseProxyOverride(value string) string {
	var hosts []string
	for _, entry := range strings.Split(value, ";") {
		entry = strings.TrimSpace(entry)
		switch {
		case entry == "":
		case strings.EqualFold(entry, "<local>"):
			hosts = append(hosts, "localhost", "127.0.0.1", "::1")
		default:
			hosts = append(hosts, entry)
		}
	}
	return strings.Join(hosts, ",")
}
