FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -o /out/whatsapp-bridge .

FROM debian:stable-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates ffmpeg tzdata && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/whatsapp-bridge /usr/local/bin/whatsapp-bridge
WORKDIR /app
ENTRYPOINT ["whatsapp-bridge"]
