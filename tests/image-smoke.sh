#!/bin/sh
# Runtime image smoke test for vibekit. Invoked by the central CI docker job:
#   sh tests/image-smoke.sh <image-ref>
#
# Starts the assembled image and waits for the container's own HEALTHCHECK
# (HTTP GET /api/health on :9847) to report "healthy" — proving the server
# binds, the embedded static UI is present, and the health endpoint serves.
# vibekit installs dev tooling into /config on first boot, so the timeout is
# generous and exceeds the image's 60s healthcheck start-period.
set -eu

IMG="${1:?usage: image-smoke.sh <image-ref>}"
NAME="smoke-vibekit-$$"
TIMEOUT=240

# shellcheck disable=SC2329  # invoked indirectly via trap
cleanup() {
	echo "--- container logs (tail) ---"
	docker logs "$NAME" 2>&1 | tail -40 || true
	docker rm -f "$NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run -d --name "$NAME" "$IMG" >/dev/null

i=0
status=starting
while [ "$i" -lt "$TIMEOUT" ]; do
	status=$(docker inspect --format '{{ if .State.Health }}{{ .State.Health.Status }}{{ else }}no-healthcheck{{ end }}' "$NAME" 2>/dev/null || echo gone)
	case "$status" in
	healthy) echo "vibekit image smoke: ok (healthy after ${i}s)"; exit 0 ;;
	unhealthy) echo "FAIL: vibekit reported unhealthy"; exit 1 ;;
	no-healthcheck) echo "FAIL: image has no HEALTHCHECK to assert against"; exit 1 ;;
	gone) echo "FAIL: vibekit container exited early"; exit 1 ;;
	esac
	i=$((i + 1))
	sleep 1
done
echo "FAIL: vibekit did not become healthy within ${TIMEOUT}s (last status: $status)"
exit 1
