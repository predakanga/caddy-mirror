package mirror

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestRequestSerializationClearsConnectionClose(t *testing.T) {
	req := &http.Request{
		Header: map[string][]string{
			"Connection": {"close"},
		},
		RemoteAddr: "127.0.0.1:58080",
		URL:        &url.URL{},
	}

	if serReq := serializeRequest(req); strings.ToLower(serReq.headers.Get("Connection")) == "close" {
		t.Errorf("Serialization must clear Connection: Close header")
	}
}
