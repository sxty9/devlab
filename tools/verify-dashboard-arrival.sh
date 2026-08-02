#!/usr/bin/env bash
# tools/verify-dashboard-arrival.sh — proves the built dashboard ARRIVED where the browser reads it.
#
# The measure of a ui-bearing delivery is NOT that the dashboard was built, but that the freshly
# built bundle sits at the serve root the browser fetches from. The bug this guards against was a
# serve root OLDER than the checkout's build output (measured 2026-08-01: build 00:56:30, serve
# 19:25:41 the day before) — a rebuilt-but-undelivered dashboard reported green. This check reads the
# same instance configuration as the root installer (devlab-install) and confirms the inversion is
# gone: the serve root is at least as fresh as the dashboard build output.
#
# It runs in ONE pass, needs no arguments, resolves every value from the instance's own configuration,
# and HALTS on the first failure. It changes nothing — it only measures. Run it after the pipeline has
# delivered a ui-bearing service; run it twice, after delivering each of TWO services, to prove a ui
# change reaches the browser for more than one service (e.g. presentr, then contax).
#
#   sudo /var/lib/devlab/.../tools/verify-dashboard-arrival.sh        # or from a checkout: tools/verify-dashboard-arrival.sh
#
# Exit codes: 0 arrived (serve root ≥ build output) · 1 NOT arrived (the browser serves a stale bundle)
#             · 2 configuration missing (cannot locate checkout or serve root)
set -euo pipefail

# Instance configuration — the SAME seams devlab-install reads (env override, else root-owned file,
# else the holistic convention). No value is asked of the caller; all are resolved here.
HOLISTIC_REPO="${DEVLAB_HOLISTIC_REPO:-}"
HOLISTIC_REPO_FILE="${DEVLAB_HOLISTIC_REPO_FILE:-/etc/devlab/holistic-repo}"
HOLISTIC_FRONTEND="${DEVLAB_HOLISTIC_FRONTEND:-frontend}"
HOLISTIC_DIST="${DEVLAB_HOLISTIC_DIST:-app/dist}"
HOLISTIC_WWW="${DEVLAB_HOLISTIC_WWW:-}"
HOLISTIC_WWW_FILE="${DEVLAB_HOLISTIC_WWW_FILE:-/etc/devlab/holistic-www}"
HOLISTIC_WWW_DEFAULT="/opt/holistic/www"

die() { printf 'verify-dashboard-arrival: %s\n' "$1" >&2; exit "${2:-2}"; }

if [ -z "$HOLISTIC_REPO" ] && [ -r "$HOLISTIC_REPO_FILE" ]; then
  HOLISTIC_REPO="$(head -n1 -- "$HOLISTIC_REPO_FILE" | tr -d '[:space:]')"
fi
[ -n "$HOLISTIC_REPO" ] || die "no holistic dashboard checkout configured (DEVLAB_HOLISTIC_REPO / $HOLISTIC_REPO_FILE)" 2

if [ -z "$HOLISTIC_WWW" ] && [ -r "$HOLISTIC_WWW_FILE" ]; then
  HOLISTIC_WWW="$(head -n1 -- "$HOLISTIC_WWW_FILE" | tr -d '[:space:]')"
fi
[ -n "$HOLISTIC_WWW" ] || HOLISTIC_WWW="$HOLISTIC_WWW_DEFAULT"

DIST_DIR="$HOLISTIC_REPO/$HOLISTIC_FRONTEND/$HOLISTIC_DIST"

newest() {  # newest file mtime under a tree, as an epoch second (0 when empty/absent)
  local t=""
  if [ -d "$1" ] && [ -n "$(ls -A "$1" 2>/dev/null)" ]; then
    # `|| true`: a permission-denied in find must not abort this read-only check under pipefail.
    t="$({ find "$1" -type f -printf '%T@\n' 2>/dev/null | sort -n | tail -n1 | cut -d. -f1; } || true)"
  fi
  echo "${t:-0}"
}
stamp() { date -d "@$1" '+%Y-%m-%d %H:%M:%S' 2>/dev/null || echo "$1"; }

[ -d "$DIST_DIR" ] || die "dashboard build output '$DIST_DIR' not found — has the dashboard been built at all?" 2
[ -d "$HOLISTIC_WWW" ] || die "serve root '$HOLISTIC_WWW' not found — the dashboard has never been delivered to the browser" 1

BUILD_T="$(newest "$DIST_DIR")"
SERVE_T="$(newest "$HOLISTIC_WWW")"

printf 'dashboard build output : %s  (%s)\n' "$(stamp "$BUILD_T")" "$DIST_DIR"
printf 'serve root (browser)   : %s  (%s)\n' "$(stamp "$SERVE_T")" "$HOLISTIC_WWW"

if [ "$SERVE_T" -lt "$BUILD_T" ]; then
  die "NOT ARRIVED — the serve root is OLDER than the build output: the dashboard was rebuilt but never delivered, so the browser still serves the old bundle. Re-run the ui-bearing delivery through the chain." 1
fi

# A fresh bundle the webserver cannot READ is the same outage from the other side — measured 2026-08-01:
# the serve root was present and current, yet the webserver (a different account) could not read it and
# answered 404 over a present page. Freshness alone would report OK. So the reachability of the browser
# is proven the same way the delivery now proves it: as root, drop to the unprivileged `nobody` (if it
# can read the start page, any webserver account can); otherwise measure the other-read bit directly.
INDEX="$HOLISTIC_WWW/index.html"
[ -f "$INDEX" ] || die "the serve root '$HOLISTIC_WWW' has no index.html — the browser has no start page to fetch (delivery incomplete)" 1
if [ "$(id -u)" = 0 ] && command -v runuser >/dev/null 2>&1 && getent passwd nobody >/dev/null 2>&1; then
  runuser -u nobody -- test -r "$INDEX" \
    || die "NOT READABLE — the start page '$INDEX' is not readable by an unprivileged reader ('nobody'), so the webserver answers 404 over a present page. The serve root's permissions do not match its public role — re-run the delivery, which now sets them by role." 1
else
  MODE="$(stat -c %a "$INDEX" 2>/dev/null || echo 0)"
  [ "$(( 8#${MODE:-0} & 4 ))" = 4 ] \
    || die "NOT READABLE — the start page '$INDEX' lacks other-read (mode $MODE), so the webserver could not read it and answers 404. Re-run the delivery, which now sets serve-root permissions by role." 1
fi

echo "OK — the serve root is at least as fresh as the build output AND readable by the webserver's role: the browser fetches the new bundle."
exit 0
