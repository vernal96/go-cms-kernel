package core

import (
	"net/http/httptest"
	"testing"

	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

func TestResourceViewNavigationHeuristics(t *testing.T) {
	tests := []struct {
		name, method, header, value string
		preview, want               bool
	}{
		{name: "ordinary GET", method: "GET", want: true},
		{name: "browser API fetch", method: "GET", header: "Sec-Fetch-Dest", value: "empty", want: true},
		{name: "repeat navigation", method: "GET", header: "User-Agent", value: "Mozilla/5.0", want: true},
		{name: "HEAD", method: "HEAD"},
		{name: "preview", method: "GET", preview: true},
		{name: "bot", method: "GET", header: "User-Agent", value: "Googlebot/2.1"},
		{name: "spider", method: "GET", header: "User-Agent", value: "Yahoo! Slurp"},
		{name: "prefetch", method: "GET", header: "Purpose", value: "prefetch"},
		{name: "sec-purpose", method: "GET", header: "Sec-Purpose", value: "prefetch;prerender"},
		{name: "Firefox prefetch", method: "GET", header: "X-Moz", value: "prefetch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "/resources/resolve/page", nil)
			if test.header != "" {
				request.Header.Set(test.header, test.value)
			}
			request = request.WithContext(httptransport.WithPreview(request.Context(), test.preview))
			if got := resourceViewNavigation(request); got != test.want {
				t.Fatalf("got=%t want=%t", got, test.want)
			}
		})
	}
}
