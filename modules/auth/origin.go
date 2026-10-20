package auth

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/vernal96/go-cms-kernel/security"
)

func canonicalOrigin(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.ForceQuery ||
		strings.Contains(raw, "#") || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil, errors.New("authentication origin is invalid")
	}
	host, port := strings.ToLower(parsed.Hostname()), parsed.Port()
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return nil, errors.New("authentication origin port is invalid")
		}
		if parsed.Scheme == "http" && number == 80 || parsed.Scheme == "https" && number == 443 {
			port = ""
		} else {
			port = strconv.Itoa(number)
		}
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return &url.URL{Scheme: parsed.Scheme, Host: host}, nil
}

func (s *Service) originForHost(host string) (string, error) {
	configured, err := canonicalOrigin(s.origin)
	if err != nil {
		return "", err
	}
	actual, err := canonicalOrigin(configured.Scheme + "://" + host)
	if err != nil || actual.Hostname() != configured.Hostname() {
		return "", security.ErrForbidden
	}
	return actual.String(), nil
}

func (s *Service) requestOrigin(request *http.Request) (string, error) {
	actual, err := s.originForHost(request.Host)
	if err != nil {
		return "", err
	}
	values := request.Header.Values("Origin")
	if len(values) > 1 {
		return "", security.ErrForbidden
	}
	if len(values) == 1 && values[0] != "" {
		origin, err := canonicalOrigin(values[0])
		if err != nil || origin.String() != actual {
			return "", security.ErrForbidden
		}
	}
	return actual, nil
}
