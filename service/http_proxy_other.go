//go:build !windows

package service

import "golang.org/x/net/http/httpproxy"

// platformProxyConfig has no source of proxy settings outside the environment,
// which SystemProxy already consulted.
func platformProxyConfig() *httpproxy.Config { return nil }
