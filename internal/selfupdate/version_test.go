package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsReleaseVersion(t *testing.T) {
	cases := map[string]bool{
		"":        false,
		"dev":     false,
		"unknown": false,
		" dev ":   false,
		"v0.4.0":  true,
		"0.4.0":   true,
	}
	for in, want := range cases {
		if got := IsReleaseVersion(in); got != want {
			t.Errorf("IsReleaseVersion(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestUpdateAvailable(t *testing.T) {
	tests := []struct {
		name              string
		current, latest   string
		wantAvail, wantOK bool
	}{
		{"update available", "v0.3.1", "v0.4.0", true, true},
		{"patch update", "0.4.0", "0.4.1", true, true},
		{"already latest", "v0.4.0", "v0.4.0", false, true},
		{"current newer", "v0.5.0", "v0.4.0", false, true},
		{"prerelease latest", "v0.4.0", "v0.4.1-next", true, true},
		{"dev current not comparable", "dev", "v0.4.0", false, false},
		{"unknown current not comparable", "unknown", "v0.4.0", false, false},
		{"unparseable latest", "v0.4.0", "not-a-version", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			avail, ok := UpdateAvailable(tt.current, tt.latest)
			if avail != tt.wantAvail || ok != tt.wantOK {
				t.Errorf("UpdateAvailable(%q,%q) = (%v,%v), want (%v,%v)",
					tt.current, tt.latest, avail, ok, tt.wantAvail, tt.wantOK)
			}
		})
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in   string
		want [3]int
		ok   bool
	}{
		{"v1.2.3", [3]int{1, 2, 3}, true},
		{"1.2.3", [3]int{1, 2, 3}, true},
		{"0.4.0-next", [3]int{0, 4, 0}, true},
		{"1.2", [3]int{1, 2, 0}, true},
		{"", [3]int{}, false},
		{"vabc", [3]int{}, false},
	}
	for _, tt := range tests {
		got, ok := parseVersion(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("parseVersion(%q) = (%v,%v), want (%v,%v)", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestLatestTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("missing Accept header")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tag_name":"v0.4.0","name":"pigo 0.4.0"}`))
	}))
	defer srv.Close()

	// LatestTag builds the URL from repo; use a transport that redirects to the
	// test server regardless of host.
	client := srv.Client()
	client.Transport = rewriteHost{base: srv.URL, rt: client.Transport}

	tag, err := LatestTag(context.Background(), client, "smallnest/pigo")
	if err != nil {
		t.Fatalf("LatestTag: %v", err)
	}
	if tag != "v0.4.0" {
		t.Errorf("tag = %q, want v0.4.0", tag)
	}
}

func TestLatestTagErrors(t *testing.T) {
	t.Run("non-200", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()
		client := srv.Client()
		client.Transport = rewriteHost{base: srv.URL, rt: client.Transport}
		if _, err := LatestTag(context.Background(), client, "smallnest/pigo"); err == nil {
			t.Fatal("expected error on 403")
		}
	})

	t.Run("empty tag", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"tag_name":""}`))
		}))
		defer srv.Close()
		client := srv.Client()
		client.Transport = rewriteHost{base: srv.URL, rt: client.Transport}
		if _, err := LatestTag(context.Background(), client, "smallnest/pigo"); err == nil {
			t.Fatal("expected error on empty tag_name")
		}
	})
}

// rewriteHost redirects every request to base, so tests can point the fixed
// GitHub API URL at an httptest server.
type rewriteHost struct {
	base string
	rt   http.RoundTripper
}

func (rw rewriteHost) RoundTrip(req *http.Request) (*http.Response, error) {
	u, err := req.URL.Parse(rw.base)
	if err != nil {
		return nil, err
	}
	req.URL.Scheme = u.Scheme
	req.URL.Host = u.Host
	rt := rw.rt
	if rt == nil {
		rt = http.DefaultTransport
	}
	return rt.RoundTrip(req)
}
