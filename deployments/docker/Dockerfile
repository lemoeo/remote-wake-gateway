FROM node:26-alpine AS web
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/web/dist ./internal/web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /gateway ./cmd/remotewake-gateway

FROM alpine:3.22
RUN addgroup -S -g 10001 remotewake && adduser -S -D -H -u 10001 -G remotewake remotewake && mkdir /data && chown remotewake:remotewake /data
COPY --from=build /gateway /usr/local/bin/remotewake-gateway
USER 10001:10001
WORKDIR /data
EXPOSE 8080
ENTRYPOINT ["remotewake-gateway"]
CMD ["serve", "--data-dir", "/data", "--listen", "0.0.0.0:8080"]
