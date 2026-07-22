# syntax=docker/dockerfile:1.19.0

ARG GO_IMAGE=golang:1.26-trixie
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian13:nonroot

FROM ${GO_IMAGE} AS builder
ENV CGO_ENABLED=0 GOOS=linux
WORKDIR /source
COPY go.mod go.sum ./*.go ./
COPY e2e/go.mod e2e/go.sum ./e2e/
COPY e2e/main.go ./e2e/main.go
WORKDIR /source/e2e
RUN go mod tidy -diff
RUN go mod download
RUN go build -mod=readonly -buildvcs=false -trimpath -ldflags="-s -w" -o /server .

FROM ${RUNTIME_IMAGE} AS runtime
COPY --from=builder --chmod=0555 /server /server
USER 65532:65532
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/server"]
