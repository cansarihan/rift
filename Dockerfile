FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=none
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/rift ./cmd/rift
# Two builds of the same program: one with a missing bounds check, one with it restored.
RUN go build -trimpath -o /out/sample-old ./deploy/sample/old \
 && go build -trimpath -o /out/sample-new ./deploy/sample/new

FROM alpine:3.20
RUN adduser -D -u 10001 rift
COPY --from=build /out/rift /usr/local/bin/rift
COPY --from=build /out/sample-old /srv/sample-old
COPY --from=build /out/sample-new /srv/sample-new
USER rift
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/rift"]
CMD ["serve", "-listen", "0.0.0.0:8080", "-old", "/srv/sample-old", "-new", "/srv/sample-new"]
