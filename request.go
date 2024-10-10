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
	user    *url.Userinfo
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

	toRet := Request{
		r.Host,
		r.Method,
		headers,
		r.URL.Path,
		r.URL.RawQuery,
		nil,
	}

	// Copy the user if it exists
	if r.URL.User != nil {
		toRet.user = &*r.URL.User
	}

	return toRet
}

func (r Request) deserialize(baseUrl *url.URL) *http.Request {
	targetUrl := *baseUrl
	targetUrl.Path = r.path
	targetUrl.RawQuery = r.query
	targetUrl.User = r.user

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
