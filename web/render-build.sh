#!/bin/sh
set -eu

api_url="${API_BASE_URL:-https://voltbarrio-medicion-demo.onrender.com}"
case "$api_url" in
  https://*|http://*) ;;
  *) api_url="https://${api_url}" ;;
esac

mkdir -p web/dist
cp web/index.html web/app.js web/styles.css web/dist/
printf 'window.VOLTBARRIO_API_BASE_URL = "%s";\n' "$api_url" > web/dist/config.js
sed -i 's#</head>#<script src="/config.js"></script></head>#' web/dist/index.html
