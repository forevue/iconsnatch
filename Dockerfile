FROM nixos/nix:latest as builder

COPY . /tmp/build
WORKDIR /tmp/build

RUN nix --extra-experimental-features "nix-command flakes" --option filter-syscalls false build

RUN mkdir /tmp/nix-store-closure
RUN cp -R $(nix-store -qR result/) /tmp/nix-store-closure

FROM scratch

COPY --from=builder /tmp/nix-store-closure /nix/store
COPY --from=builder /tmp/build/result /app

COPY --from=alpine:latest /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

CMD ["/app/bin/faviconapi"]