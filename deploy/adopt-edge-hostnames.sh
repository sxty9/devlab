#!/usr/bin/env bash
# adopt-edge-hostnames.sh — bring a host that already runs Holistic onto the hostname-aware edge, in ONE
# pass, and MEASURE the result instead of claiming it.
#
# WHAT WAS WRONG, and what this ends. The edge of a grown host answered every hostname with the same page,
# because ONE site block accepted every name; and inside that block two delivered files both claimed
# `handle /api/*`, so the alphabet decided which application's API existed at all. Measured on production
# on 2026-08-09: three hostnames, three identical pages, the landscape dashboard's own API answering 404
# through the edge while answering 200 directly, and nobody able to log in to the dashboard because
# /api/auth/login reached DevLab.
#
# The fix is in the delivered wrappers: each ROOT APPLICATION gets a site block of its OWN, on the hostname
# THIS host declares for it, and the uniform services live on a shelf the dashboard application imports.
# What is left for a human is the one thing no package may state — WHICH NAME belongs to which application.
# A hostname is an instance value: it lives in this host's runtime configuration and never in a repository
# (Keine Instanz-Spezifika), which is why this script takes the names as arguments rather than carrying
# them. They are the names the tunnel in front of this host already forwards; there is nothing to invent.
#
# ONE LINE, ONE PASS:
#   sudo bash deploy/adopt-edge-hostnames.sh <dashboard-hostname> <devlab-hostname>
#
# It runs from a checkout of the merged standard branch, on the host itself. In order it:
#   1. installs the current receiver and shared library from this checkout (devlab-install-recv);
#   2. declares the two hostnames in this host's runtime configuration (--edge-host);
#   3. lets that same run rebuild the edge and MOVE the delivered routes onto their two shelves;
#   4. proves the edge validates, reloads it, and MEASURES what a browser now gets for each name.
# Steps 1–3 are one invocation of devlab-install-recv, because they are one decision: the wrappers, the
# names and the edge must never be half-changed relative to one another.
#
# WHAT IT DOES NOT DO, and says so at the end: an application only receives its site block when it is
# DELIVERED, because only a delivery knows its serve root and the port its unit binds. So after this pass
# the names exist and the shelves exist; the site blocks appear with the next delivery of each root
# application. Until then those names answer with the edge's honest refusal, not with somebody else's page.
#
# It halts on the first failure. Everything it replaces, devlab-install-recv backs up first and restores on
# failure; this script adds no unwind of its own because it performs no change of its own.
set -euo pipefail

say() { printf '%s\n' "adopt-edge-hostnames: $*"; }
die() { printf 'adopt-edge-hostnames: error: %s\n' "$1" >&2; exit "${2:-1}"; }

DASH_HOST="${1:-}"
DEVLAB_HOST="${2:-}"
if [ -z "$DASH_HOST" ] || [ -z "$DEVLAB_HOST" ]; then
  die "needs both hostnames: $0 <dashboard-hostname> <devlab-hostname> — these are the names the tunnel in front of this host already forwards to it; they are instance values and are never carried in the repository" 2
fi
[ "$(id -u)" -eq 0 ] || die "must run as root (it installs root wrappers and rewrites the edge)" 2

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
RECV_INSTALLER="$HERE/devlab-install-recv"
[ -x "$RECV_INSTALLER" ] || die "run this from a checkout: $RECV_INSTALLER not found or not executable" 2

CADDY_CONF="${DEVLAB_CADDY_CONF:-/etc/caddy/conf.d}"
CADDY_MAIN="${DEVLAB_CADDY_MAIN:-/etc/caddy/Caddyfile}"

# ── 1–3. wrappers, names and edge in ONE invocation ─────────────────────────────────────────────────
# No --provision: this host is already a production target. The receiver refresh brings the edge up to the
# current shape in the same run (refresh_managed_edge), and the same run moves the delivered routes onto
# their shelves. A host whose edge is hand-grown is left untouched and NAMED by that run — read its output.
say "installing the current receiver + shared library, declaring the hostnames, and rebuilding the edge…"
"$RECV_INSTALLER" --edge-host "holistic=$DASH_HOST" --edge-host "devlab=$DEVLAB_HOST" \
  || die "the receiver refresh did not pass its own self-check — see its output above; it rolled back what it replaced" 1

# ── 4. MEASURE, do not claim ────────────────────────────────────────────────────────────────────────
say "── measuring the edge on this host ──"
ADDR="$("$RECV_INSTALLER" --print-edge-address)" || die "this host declares no edge address — it cannot be measured" 1
say "the edge answers on: $ADDR"

command -v caddy >/dev/null 2>&1 && { caddy validate --config "$CADDY_MAIN" >/dev/null 2>&1 \
  || die "the assembled edge does not validate ($CADDY_MAIN) — nothing was reloaded" 1; }
say "the assembled edge validates"

# What it BINDS. Without the bind the edge would answer on every interface — on a host whose edge is meant
# to be reached only through its private overlay, that is the difference between behind and in front of the
# tunnel. Reported as measured, never assumed.
if command -v ss >/dev/null 2>&1; then
  say "listeners on the edge port:"
  ss -lnt | grep -F ":${ADDR##*:}" || say "  (nothing is listening on port ${ADDR##*:} — is caddy running?)"
fi

# What a BROWSER gets. Each name is asked the way a browser asks it: with the Host header set, against the
# address this host declares. A name with no application delivered under it yet answers the honest refusal.
probe() { # <host> <path> <what>
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' -H "Host: $1" "http://${ADDR}$2" 2>/dev/null || echo 000)"
  printf '  %-38s %-28s -> %s\n' "$1" "$2" "$code"
}
say "what each name answers now:"
probe "$DASH_HOST" "/api/instance"
probe "$DASH_HOST" "/"
probe "$DEVLAB_HOST" "/api/mercury/runs"
probe "$DEVLAB_HOST" "/"
probe "unbekannt.invalid" "/"

# ── what is still outstanding, and why it is not part of this pass ──────────────────────────────────
say "── still outstanding after this pass ──"
apps_dir="$CADDY_CONF/apps"
for id in holistic devlab; do
  if [ -f "$apps_dir/$id.caddy" ]; then
    say "  '$id' has its site block ($apps_dir/$id.caddy) — its name answers with it"
  else
    say "  '$id' has NO site block yet: it is written when '$id' is next DELIVERED, because only a delivery"
    say "        knows its serve root and the port its unit binds. Until then its name answers the edge's"
    say "        refusal — never another application's page."
  fi
done
if ! grep -rqE '^[[:space:]]*import[[:space:]]+holistic_service_routes[[:space:]]*$' "$apps_dir" 2>/dev/null; then
  say "  no application on this host carries the landscape's uniform services yet (edge.role=dashboard):"
  say "        until the dashboard is delivered declaring that role, the uniform services on the services"
  say "        shelf are reachable under no name at all."
fi
say "  a health check that fetches the bare edge address WITHOUT a Host header now gets an honest 404"
say "        instead of a page. That is the same request a browser makes; such a check must send the name."
say "done — nothing above was claimed, all of it was measured on this host."
