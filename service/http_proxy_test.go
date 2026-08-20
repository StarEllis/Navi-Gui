package service

import "testing"

func TestParseProxyServer(t *testing.T) {
	tests := []struct {
		value               string
		wantHTTP, wantHTTPS string
	}{
		{"127.0.0.1:7897", "127.0.0.1:7897", "127.0.0.1:7897"},
		{"http=127.0.0.1:7890;https=127.0.0.1:7891", "127.0.0.1:7890", "127.0.0.1:7891"},
		{"ftp=127.0.0.1:7892", "", ""},
		{"  ", "", ""},
	}
	for _, test := range tests {
		gotHTTP, gotHTTPS := parseProxyServer(test.value)
		if gotHTTP != test.wantHTTP || gotHTTPS != test.wantHTTPS {
			t.Errorf("parseProxyServer(%q) = %q/%q, want %q/%q", test.value, gotHTTP, gotHTTPS, test.wantHTTP, test.wantHTTPS)
		}
	}
}

func TestParseProxyOverride(t *testing.T) {
	got := parseProxyOverride("localhost;192.168.*;<local>;")
	want := "localhost,192.168.*,localhost,127.0.0.1,::1"
	if got != want {
		t.Errorf("parseProxyOverride = %q, want %q", got, want)
	}
}
