FROM golang:1.14 as builder
ENV PATH /go/bin:/usr/local/go/bin:$PATH
ENV GOPATH /go
COPY . /go/src/github.com/liqotech/liqo
WORKDIR /go/src/github.com/liqotech/liqo
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/discovery/
RUN cp discovery /usr/bin/discovery

FROM alpine:3.12
RUN apk update && apk add --no-cache ca-certificates bash && rm -rf /var/cache/apk/*
COPY scripts/discovery/entrypoint.sh /usr/bin/entrypoint.sh
COPY --from=builder /usr/bin/discovery /usr/bin/discovery
ENTRYPOINT [ "/usr/bin/entrypoint.sh", "/usr/bin/discovery" ]
