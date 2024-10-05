FROM caddy:2.8.4-builder AS builder

ADD . /src

RUN xcaddy build \
    --with github.com/predakanga/caddy-mirror=/src

FROM caddy:2.8.4

COPY --from=builder /usr/bin/caddy /usr/bin/caddy
