FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/invite .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/invite /invite
USER nonroot
ENTRYPOINT ["/invite"]
