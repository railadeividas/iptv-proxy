#!/bin/sh
# Smoke test for the iptv-org/epg grabber inside the built image. It runs in the image and needs no internet:
#
#   docker build -t iptv-smoke .
#   docker run --rm -v "$PWD/scripts/epg-smoke.sh:/smoke.sh:ro" --entrypoint sh iptv-smoke /smoke.sh
#
# 1. every flag internal/epg passes to the grabber still exists (the grabber follows iptv-org's master branch);
# 2. a real run, the same command line as internal/epg, against a local fixture site produces valid XMLTV
#    (catches Node/runtime/dependency breakage, such as a Node major that the grabber cannot run on).
set -eu
cd /opt/epg
fail() { echo "EPG SMOKE TEST FAILED: $*" >&2; exit 1; }

echo "node $(node -v)"
help=$(npm run --silent grab -- --help 2>&1) || fail "grabber does not start: $help"
for flag in --channels --output --maxConnections --days; do
  echo "$help" | grep -q -e "$flag" || fail "grabber no longer has the $flag option (internal/epg uses it)"
done
echo "flags ok"

PORT=18999
node -e "require('http').createServer((q,r)=>r.end('ok')).listen($PORT,'127.0.0.1')" &
SERVER=$!
trap 'kill $SERVER 2>/dev/null || true; rm -rf sites/smoke.test /tmp/smoke' EXIT

mkdir -p sites/smoke.test /tmp/smoke
cat > sites/smoke.test/smoke.test.config.js <<EOF
const dayjs = require('dayjs')
module.exports = {
  site: 'smoke.test',
  days: 1,
  url: () => 'http://127.0.0.1:$PORT/',
  parser: () => { const s = dayjs(); return [{ title: 'Smoke show', start: s, stop: s.add(1, 'h') }] }
}
EOF
cat > /tmp/smoke/channels.xml <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<channels>
  <channel site="smoke.test" lang="en" xmltv_id="Smoke.us@SD" site_id="1">Smoke &amp; Mirrors</channel>
</channels>
EOF
sleep 1
npm run --silent grab -- --channels=/tmp/smoke/channels.xml --output=/tmp/smoke/guide.xml --maxConnections=1 --days=1 \
  > /tmp/smoke/log 2>&1 || fail "grab failed: $(tail -c 600 /tmp/smoke/log)"

[ -s /tmp/smoke/guide.xml ] || fail "grabber wrote no guide: $(tail -c 600 /tmp/smoke/log)"
grep -q '<tv' /tmp/smoke/guide.xml || fail "guide is not XMLTV"
grep -q 'id="Smoke.us@SD"' /tmp/smoke/guide.xml || fail "guide lost the channel id (the tvg-id) we send"
grep -q 'Smoke show' /tmp/smoke/guide.xml || fail "guide has no programme"
echo "grab ok ($(wc -c < /tmp/smoke/guide.xml) bytes)"
