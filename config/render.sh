#!/bin/sh
# Render Kratos config from the baked template + env vars and drop everything
# into the shared volume that the kratos / kratos-migrate services mount
# read-only at /etc/config/kratos.
set -eu

OUT="${OUT:-/out}"
mkdir -p "$OUT"

envsubst < /src/kratos.yml.tmpl > "$OUT/kratos.yml"

for f in identity.schema.json \
         oidc.google.jsonnet \
         oidc.pocket-id.jsonnet \
         oidc.github.jsonnet \
         oidc.gitlab.jsonnet \
         oidc.microsoft.jsonnet; do
    cp "/src/$f" "$OUT/$f"
done

echo "Rendered Kratos config to $OUT:"
ls -la "$OUT"
