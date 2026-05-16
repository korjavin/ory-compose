FROM alpine:3.20
RUN apk add --no-cache gettext
COPY kratos /src
COPY render.sh /usr/local/bin/render
RUN chmod +x /usr/local/bin/render
ENTRYPOINT ["/usr/local/bin/render"]
