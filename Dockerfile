FROM golang:1.12-stretch as bin

LABEL author="Team SRE <sre@lastminute.com>"

ARG APPNAME
COPY . /work
WORKDIR /work

RUN go build -a -ldflags '-extldflags "-static"' -o /$APPNAME . && ls /$APPNAME && ls -la /$APPNAME && ls -la /

FROM debian:stretch

COPY --from=bin /$APPNAME /heimdall

RUN apt update && DEBIAN_FRONTEND=noninteractive apt install -y ca-certificates && update-ca-certificates
RUN ls -la / && ls -la /heimdall
RUN chmod +x /heimdall

ENTRYPOINT ["/heimdall"]

