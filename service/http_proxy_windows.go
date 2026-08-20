//go:build windows

package service

import (
	"golang.org/x/net/http/httpproxy"
	"golang.org/x/sys/windows/registry"
)

// platformProxyConfig reads the proxy Windows itself hands to browsers, so a
// user running a local proxy does not have to launch the app from a shell that
// exports HTTP_PROXY. Read per request: the user can toggle the proxy while the
// app stays open.
func platformProxyConfig() *httpproxy.Config {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer key.Close()

	if enabled, _, err := key.GetIntegerValue("ProxyEnable"); err != nil || enabled == 0 {
		return nil
	}
	server, _, err := key.GetStringValue("ProxyServer")
	if err != nil {
		return nil
	}
	httpProxy, httpsProxy := parseProxyServer(server)
	if httpProxy == "" && httpsProxy == "" {
		return nil
	}
	override, _, _ := key.GetStringValue("ProxyOverride")
	return &httpproxy.Config{
		HTTPProxy:  httpProxy,
		HTTPSProxy: httpsProxy,
		NoProxy:    parseProxyOverride(override),
	}
}
