FROM golang:alpine@sha256:1b455a3f7786e5765dbeb4f7ab32a36cdc0c3f4ddd35406606df612dc6e3269b as app-builder

WORKDIR /go/src/app
COPY . .
RUN apk add git

RUN CGO_ENABLED=0 go install -ldflags '-extldflags "-static"' -tags timetzdata

FROM scratch

COPY --from=app-builder /go/bin/faviconapi /faviconapi
COPY --from=alpine:latest /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
ENTRYPOINT ["/faviconapi"]