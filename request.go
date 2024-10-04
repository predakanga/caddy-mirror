package mirror

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

type Request struct {
	host    string
	method  string
	headers http.Header
	path    string
	query   string
}

func serializeRequest(r *http.Request) Request {
	headers := r.Header.Clone()

	// Remove Connection: close if provided - we want to reuse connections as much as possible
	if strings.ToLower(headers.Get("Connection")) == "close" {
		headers.Del("Connection")
	}

	// Apply X-Forwarded-For before serialization
	remoteIp, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		panic(fmt.Errorf("invalid remote address: %s", r.RemoteAddr))
	}
	xffHeader := headers.Get("X-Forwarded-For")
	if xffHeader != "" {
		headers.Set("X-Forwarded-For", xffHeader+", "+remoteIp)
	} else {
		headers.Set("X-Forwarded-For", remoteIp)
	}

	return Request{
		r.Host,
		r.Method,
		headers,
		r.URL.RawPath,
		r.URL.RawQuery,
	}
}

func (r Request) deserialize(baseUrl *url.URL) *http.Request {
	targetUrl := *baseUrl
	targetUrl.RawPath = r.path
	targetUrl.RawQuery = r.query

	return &http.Request{
		Host:       r.host,
		Method:     r.method,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     r.headers,
		URL:        &targetUrl,
	}
}
