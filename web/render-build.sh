#!/bin/sh
set -eu

case "${API_BASE_URL:-}" in
  https://*) api_url="$API_BASE_URL" ;;
  http://*) api_url="$API_BASE_URL" ;;
  *) api_url="https://${API_BASE_URL:?Render did not provide API_BASE_URL}" ;;
esac

mkdir -p web/dist
cp web/index.html web/app.js web/styles.css web/dist/
printf 'window.VOLTBARRIO_API_BASE_URL = "%s";\n' "$api_url" > web/dist/config.js
sed -i 's#</head>#<script src="/config.js"></script></head>#' web/dist/index.html
