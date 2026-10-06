FROM golang:1.26.4-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY main.go ./
COPY bundle ./bundle
COPY config ./config
COPY gen ./gen
COPY scrape ./scrape
COPY serve ./serve
COPY web ./web
RUN CGO_ENABLED=0 go build -trimpath -o /cragcast .

FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
COPY --from=build /cragcast /usr/local/bin/cragcast
COPY regions ./regions

ENTRYPOINT ["cragcast"]
