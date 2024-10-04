# caddy-mirror
#### A basic request mirroring/shadowing middleware for [Caddy 2](https://caddyserver.com/)

---

This module provides an HTTP handler which mirrors matching requests to an additional target.

It was written to solve two shortcomings with ngx_http_mirror_module - mirror requests were tied to the lifecycle of the main requests, and could not reuse connections.

Because the module is a minimum viable product, it has several of it's own shortcomings:

- Request bodies are not currently supported
- Requests are not identical to reverse_proxied requests
- Error logging is slow and spammy

### Usage

```Caddyfile
example.com {
	route /hello {
		mirror http://mirror_server
		reverse_proxy http://hello_backend
	}
	
	route /world {
	    mirror http://mirror_server {
	        # Mirror half of all incoming requests
	        sample 0.5
	        # Use two goroutines/connections to send requests
	        concurrency 2
	        # Queue of 1024 requests shared between goroutines
	        backlog 1024
	    }
	    reverse_proxy http://world_backend
    }
}
```

### Todo

- Add tests
- Add a circuit breaker for upstreams
- Try to adapt Caddy's reverse proxy as a transport