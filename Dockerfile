FROM golang as builder
COPY . /go/src/github.com/jpillora/go-tcp-proxy
WORKDIR /go/src/github.com/jpillora/go-tcp-proxy
RUN go get ./... && \
    CGO_ENABLED=0 GOOS=linux go build -o tcp-proxy cmd/tcp-proxy/main.go

FROM scratch
COPY --from=builder /go/src/github.com/jpillora/go-tcp-proxy/tcp-proxy /tcp-proxy
WORKDIR /
ENTRYPOINT ["./tcp-proxy"]
