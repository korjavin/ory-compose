FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/consent .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/consent /consent
EXPOSE 3001
USER nonroot
ENTRYPOINT ["/consent"]
