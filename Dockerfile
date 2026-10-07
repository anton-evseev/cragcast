FROM golang:1.26.4-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY main.go ./
COPY bundle ./bundle
COPY config ./config
COPY scrape ./scrape
COPY serve ./serve
COPY web ./web
RUN CGO_ENABLED=0 go build -trimpath -o /cragcast .

FROM node:24-bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*
RUN npm install -g wrangler@4.148.0 && npm cache clean --force

WORKDIR /app
COPY --from=build /cragcast /usr/local/bin/cragcast
COPY regions ./regions
COPY wrangler.jsonc ./wrangler.jsonc

ENTRYPOINT ["cragcast"]
