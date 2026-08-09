# shellcheck shell=bash
# devlab-setup-lib.sh — the ONE source of a service's first-time SETUP product and the rules that
# guard it. It is SOURCED (never executed), defines only functions plus one constant, and touches no
# global state, so three consumers can share it without a second copy of any template or rule:
#
#   1. devlab-install (dev)      — GENERATES unit/route/account directly INTO this host at first-time
#                                   setup, from these templates with validated values.
#   2. devlab-exec artifact-build/emit-setup (dev, as the runner) — EMITS exactly the same product into
#                                   the delivery artifact (<artifact>/setup/…), so the bytes that reach
#                                   production are the bytes this one template produces.
#   3. devlab-deploy-recv (prod) — INSTALLS the delivered product on the production host when the unit
#                                   is missing, guarded by the SAME name/namespace/identity rules.
#
# "Ein Erzeuger, ein Installierer; die Vorlage bleibt an einer Stelle": the unit and route TEMPLATES
# live here and nowhere else, and the checks a first-time setup owes (name grammar, reserved names, a
# foreign systemd unit, a unit that would not run as its own service account) live here too — so
# devlab-install and devlab-deploy-recv can never drift apart on what a valid setup is.
#
# The route template's CONTAINER lives here too: a route is a naked `handle` block, valid only inside a
# site block, so setup_edge_caddyfile_text — the edge shell that devlab-install-recv --provision builds
# a bare host's edge from — sits beside setup_route_text. The container and its contents are one
# contract; keeping them in one file is what stops --provision from erecting an edge the delivered
# routes cannot live in.
#
# This file carries NO instance-specific value: every path is derived from the repo name (/opt/<repo>,
# /var/lib/<repo>) exactly as the wrappers that source it do. It must be root-owned and world-readable
# (mode 0755/0644), like the wrappers, so the unprivileged runner can source it during the build.

# Reserved identities, and the RULE they follow — copied verbatim from the namespace that devlab-install
# has always enforced, now SHARED so devlab-deploy-recv applies the identical list on the production
# host. Reserved is an identity that belongs to the operating system, to a third-party package, or to
# the landscape as a whole — one that can appear on a host without anybody's decision and that a setup
# must therefore never create, borrow or shadow. `root` is the sharpest case, because the unit template
# carries `User=<repo>`. A name the ORGANISATION can legitimately give a service of its own is NOT on
# this list even where a like-named OS account exists (a landscape may run a service called `mail`);
# what decides there are the identity checks below (foreign unit, foreign account), not the spelling.
SETUP_RESERVED_REPOS="root daemon bin sys nobody nogroup www-data adm
halt shutdown reboot init systemd udev dbus messagebus polkitd syslog rsyslog
sshd ssh cron atd chrony ntp named dnsmasq exim postfix dovecot sudo
caddy nginx apache apache2 httpd postgres postgresql mysql mariadb redis mongod
docker containerd kubelet etcd snapd holistic"

# ── the build KIND: how a service is built, DECLARED not guessed ─────────────────────────────────────
# The unified install path once assumed EVERY service is a Go daemon — it demanded a prebuilt <repo>d
# binary and refused anything else, even when the delivery package fully described what to start. That
# trapped `holistic`, a Python (uvicorn) service, out of production. The landscape now carries EXACTLY
# TWO named build kinds, and no silent third: a service DECLARES which it is, and the installer installs
# what the artifact CONTAINS and the unit DESCRIBES — it never infers the build kind from a filename.
#
#   go-daemon   — a single prebuilt Go binary <repo>d. Installed to /opt/<repo>/bin/<repo>d; its unit's
#                 ExecStart runs that binary. This is the historical uniform shape; a Go service needs
#                 no declaration (artifact-build stamps `go-daemon` when it produced the binary), so it
#                 is delivered exactly as before.
#   python-app  — a prebuilt, SELF-CONTAINED payload tree (a --copies virtualenv, the app, AND the
#                 interpreter + full standard library it needs) that the installer copies VERBATIM to
#                 /opt/<repo>; its unit's ExecStart runs an interpreter out of that tree (…/venv/bin/…).
#                 It carries no <repo>d and never will. Because only the unit knows how to start it, a
#                 python-app MUST ship its own setup/<unit>.service — the unit is the source of truth,
#                 not a name convention. `python -m venv --copies` copies the interpreter but NOT the
#                 standard library: the interpreter fetches that from its build-host `home` (…/usr/bin →
#                 …/usr/lib/pythonX.Y). A target with a DIFFERENT python has no such directory, so the
#                 service dies before its first line ("No module named 'encodings'"). So the payload
#                 BUNDLES the interpreter and its stdlib (setup_bundle_python) and re-points the venv at
#                 them — the venv depends on nothing the target's own python provides — and the
#                 interpreter's runnability on the target is PROVEN before the copy (setup_python_payload_selftest).
#
# This is a DELIBERATE, bounded pair, not an open plugin surface: extending it is a named change to this
# one list, reviewed like any other. Uniformity ("Code-Struktur") is preserved where it actually binds —
# every service, whatever its kind, ships ONE artifact through the ONE chain, declares its unit in setup/,
# passes the identical name/path/identity hardening, and is proven by the identical honest running gate.
# The build kind changes only WHICH prebuilt bytes are copied and where — one path that carries two kinds,
# never a second installer and never a per-service branch.
SETUP_BUILD_KINDS="go-daemon python-app"

# setup_valid_build_kind <kind> — 0 when <kind> is one of the two named build kinds, 1 otherwise. The set
# lives HERE and nowhere else, so the dev installer, the production receiver and the artifact builder can
# never drift on what a valid Bauart is.
setup_valid_build_kind() {
  local kind="$1" k
  for k in $SETUP_BUILD_KINDS; do [ "$kind" = "$k" ] && return 0; done
  return 1
}

# setup_read_build_kind <artifact-dir> — echo the build kind an artifact DECLARES (the trimmed first line
# of <artifact-dir>/build.kind), or nothing when the file is absent or unreadable. The caller REFUSES an
# empty result BY NAME — it never falls back to a filename guess. The value is not validated here (a caller
# that reads it also runs setup_valid_build_kind, so an unknown value and an absent one get distinct,
# named refusals).
setup_read_build_kind() {
  local dir="$1"
  [ -r "$dir/build.kind" ] || return 0
  head -n1 -- "$dir/build.kind" | tr -d '[:space:]'
}

# setup_relocate_venv <venv-dir> <final-venv-path> — make a virtualenv built at one path runnable from
# another WITHOUT rebuilding it on the target (prod never builds). `python -m venv` bakes the venv's own
# absolute path into the shebang of every script under bin/ (e.g. `#!<venv-dir>/bin/python`), so a venv
# built at the artifact staging path would, once copied to /opt/<repo>/venv, have every console script
# point back at a path that does not exist there. This rewrites those shebangs from the build path to the
# FINAL install path, at BUILD time (as the unprivileged runner) — so the installer's job stays a pure
# copy of already-correct bytes. The interpreter itself is a real binary under `--copies` (no shebang),
# and site-packages resolve relative to it, so after the shebang rewrite the tree is fully relocatable.
# Only the FIRST-LINE shebang of regular files directly under bin/ is touched, and only when it names the
# build path — nothing else in the tree is rewritten.
setup_relocate_venv() {
  local venv="$1" final="$2" f first mode newfirst
  [ -d "$venv/bin" ] || return 0
  for f in "$venv"/bin/*; do
    [ -f "$f" ] || continue
    IFS= read -r first < "$f" || continue
    case "$first" in
      "#!$venv/"*) : ;;
      *) continue ;;
    esac
    # Rewrite ONLY the first line (the shebang), leaving every following byte untouched, and keep the
    # file's mode (console scripts are executable). Done in bash so no sed delimiter can collide with a
    # path that contains '#'.
    newfirst="#!$final/${first#"#!$venv/"}"
    mode="$(stat -c %a -- "$f" 2>/dev/null || echo 755)"
    { printf '%s\n' "$newfirst"; tail -n +2 -- "$f"; } > "$f.reloc" || { rm -f -- "$f.reloc"; continue; }
    chmod "$mode" "$f.reloc"; mv -f -- "$f.reloc" "$f"
  done
}

# setup_bundle_python <staging-venv-dir> <final-venv-path> — make a --copies venv TRULY portable across
# python VERSIONS by shipping the interpreter and its standard library WITH the payload, instead of leaving
# the venv to fetch its stdlib from whatever python the target happens to have.
#
# THE BUG THIS CLOSES: `python -m venv --copies` copies the interpreter binary but not the standard
# library. The copied interpreter still finds its stdlib through the `home` recorded in pyvenv.cfg — the
# build host's /usr/bin, whose sibling /usr/lib/pythonX.Y holds the stdlib. Copy that venv to a target
# whose python differs (build host 3.14, target 3.12) and that directory does not exist there: the
# interpreter cannot even import `encodings` and the unit dies in a restart loop before its first line.
# The shebang relocation (setup_relocate_venv) does NOT help — it fixes paths INSIDE the venv, not the
# venv's binding to a python version that lives OUTSIDE it.
#
# WHAT THIS DOES: it copies the base interpreter and its FULL standard library into <payload>/pybase (the
# payload being the venv's parent, which becomes the final install prefix), then rewrites the venv's
# pyvenv.cfg so `home`/`executable` point at that BUNDLED base at its FINAL location. After this the venv
# resolves its standard library from <final-prefix>/pybase — a path the payload itself carries — and needs
# NOTHING from the target's own python. The interpreter binary is self-contained (it links only the C
# runtime, not a separate libpython), so the bundle is a plain tree the installer copies verbatim; no build
# ever runs on the target. Values are READ from the venv's own interpreter (version, stdlib location), never
# guessed, so it stays correct across python minor versions. Echoes the bundled X.Y version for the caller's
# log/stamp; returns non-zero (with a named reason) if the interpreter cannot be introspected or bundled.
setup_bundle_python() {
  local venv="$1" final_venv="$2" payload final_prefix pybase ver full stdlib base_bin
  [ -x "$venv/bin/python" ] || { echo "setup_bundle_python: no venv interpreter at $venv/bin/python" >&2; return 1; }
  payload="$(dirname -- "$venv")"
  final_prefix="$(dirname -- "$final_venv")"
  pybase="$payload/pybase"
  # Read the interpreter's own account of itself — the ONLY reliable source of its version and where its
  # standard library actually lives (distros differ; guessing /usr/lib is how this class of bug is born).
  ver="$("$venv/bin/python" -c 'import sys;print("%d.%d"%sys.version_info[:2])' 2>/dev/null)" || ver=""
  full="$("$venv/bin/python" -c 'import sys;print(sys.version.split()[0])' 2>/dev/null)" || full="$ver"
  stdlib="$("$venv/bin/python" -c 'import sysconfig;print(sysconfig.get_path("stdlib"))' 2>/dev/null)" || stdlib=""
  base_bin="$("$venv/bin/python" -c 'import sys;print(sys._base_executable or sys.executable)' 2>/dev/null)" || base_bin=""
  base_bin="$(readlink -f -- "$base_bin" 2>/dev/null || printf '%s' "$base_bin")"
  { [ -n "$ver" ] && [ -d "$stdlib" ] && [ -x "$base_bin" ]; } \
    || { echo "setup_bundle_python: could not resolve the interpreter to bundle (version='$ver' stdlib='$stdlib' base='$base_bin')" >&2; return 1; }
  # Lay down <payload>/pybase/{bin/pythonX.Y, lib/pythonX.Y/<stdlib>}. __pycache__ is excluded — the
  # compiled caches are regenerated on first import and only carry stale absolute paths otherwise.
  rm -rf -- "$pybase"; mkdir -p -- "$pybase/bin" "$pybase/lib" || return 1
  cp -a -- "$base_bin" "$pybase/bin/python$ver" || { echo "setup_bundle_python: failed to copy the interpreter" >&2; return 1; }
  ln -sf "python$ver" "$pybase/bin/python3"
  ln -sf "python$ver" "$pybase/bin/python"
  if command -v rsync >/dev/null 2>&1; then
    rsync -a --exclude='__pycache__' "$stdlib/" "$pybase/lib/python$ver/" || { echo "setup_bundle_python: failed to copy the standard library" >&2; return 1; }
  else
    cp -a -- "$stdlib" "$pybase/lib/python$ver" || { echo "setup_bundle_python: failed to copy the standard library" >&2; return 1; }
    find "$pybase/lib/python$ver" -depth -type d -name __pycache__ -exec rm -rf -- {} + 2>/dev/null || true
  fi
  # Re-point the venv at the BUNDLED base at its FINAL install location. `home` is the base bin dir; the
  # interpreter derives its prefix (and thus the stdlib at <prefix>/lib/pythonX.Y) from it. site-packages
  # under the venv are unchanged; only the base the venv sits ON is now the shipped one, not the target's.
  {
    printf 'home = %s\n' "$final_prefix/pybase/bin"
    printf 'include-system-site-packages = false\n'
    printf 'version = %s\n' "$full"
    printf 'executable = %s\n' "$final_prefix/pybase/bin/python$ver"
  } > "$venv/pyvenv.cfg" || return 1
  printf '%s\n' "$ver"
}

# setup_python_payload_selftest <payload-dir> — PROVE, before a python-app payload is copied anywhere, that
# its bundled interpreter RUNS on THIS host and imports its bundled standard library. This is the pre-copy
# compatibility gate demanded by "Kein stummes Ausbleiben": a python-app carries its own interpreter + stdlib
# (setup_bundle_python), so it no longer depends on the target's python VERSION — but the interpreter is
# still a native binary, and a host whose C runtime is OLDER than the build host's could fail to load it or
# its compiled stdlib extensions. That incompatibility is caught HERE — at build AND at receive — named, and
# refused BEFORE the copy, never left to surface as a restart loop in the target's unit log.
#
# It runs the bundled BASE interpreter standalone (it resolves its stdlib relative to its own location, so no
# baked final path and no environment are needed — that self-location is exactly the portability this proves)
# and, when a venv is present, the VENV interpreter that ExecStart actually runs, pointed at the STAGED bundle
# via PYTHONHOME (the venv's baked `home` names the not-yet-existing final /opt path). The import set is kept
# to the standard library the reported failure was about (encodings) plus a compiled extension that links
# only the C runtime (math) — never libssl/libsqlite3, whose absence would be a separate, app-level fact, not
# an interpreter incompatibility. Echoes the proven X.Y version on success; prints a named reason and returns
# non-zero on a missing bundle or an incompatible host.
setup_python_payload_selftest() {
  local payload="$1" base venv ver out matches
  matches=("$payload"/pybase/bin/python3.[0-9]*)
  base="${matches[0]}"
  if [ ! -x "$base" ]; then
    echo "python-app payload at '$payload' carries no bundled interpreter (payload/pybase/bin/python3.*) — this is a non-relocatable payload that would depend on the target's own python; refusing before any copy" >&2
    return 1
  fi
  if ! out="$(env -i "$base" -c 'import sys, encodings, json, math, binascii; sys.stdout.write(sys.version.split()[0])' 2>&1)"; then
    echo "the bundled python interpreter cannot run on this host and import its own standard library — refusing to copy an unrunnable payload. This host is incompatible with the environment the payload was built on (most often an OLDER system C library than the build host). Detail: $(printf '%s' "$out" | tail -n 3)" >&2
    return 1
  fi
  ver="$out"
  venv="$payload/venv/bin/python"
  if [ -x "$venv" ]; then
    if ! out="$(env -i PYTHONHOME="$payload/pybase" "$venv" -c 'import encodings, sys; sys.stdout.write("ok")' 2>&1)"; then
      echo "the venv interpreter cannot find the bundled standard library — the payload is not self-contained. Detail: $(printf '%s' "$out" | tail -n 3)" >&2
      return 1
    fi
  fi
  printf '%s\n' "$ver"
}

# setup_valid_repo_shape <repo> — the pure SHAPE and NAMESPACE gate, WITHOUT the reserved-identity list:
# the grammar ^[a-z][a-z0-9-]{2,30}$, no doubled or trailing dash, and not systemd's own prefix. This is
# the check every use of a repo name in a path owes (path-safety), independent of whether the name is a
# reserved identity. The reserved list is a SEPARATE judgement (setup_valid_repo_name / the ownership gate
# below) because a reserved name is not always inadmissible: the service to which the identity already
# belongs may renew itself. Prints the reason to stderr and returns 1 on a bad shape.
setup_valid_repo_shape() {
  local repo="$1"
  [[ "$repo" =~ ^[a-z][a-z0-9-]{2,30}$ ]] || { echo "invalid repo name '$repo' (want ^[a-z][a-z0-9-]{2,30}\$)" >&2; return 1; }
  case "$repo" in
    *--* | *-) echo "invalid repo name '$repo' (no doubled and no trailing dash)" >&2; return 1 ;;
    systemd-*) echo "repo name '$repo' lies in systemd's own namespace" >&2; return 1 ;;
  esac
  return 0
}

# setup_valid_repo_name <repo> — the SHAPE and NAMESPACE gate PLUS the reserved-identity list: a name that
# is admissible for a FIRST-TIME setup, which owns nothing and therefore may never take a reserved
# identity. Returns 0 when the name is admissible; otherwise it prints the reason to stderr and returns 1.
# The caller turns that into its own die/exit with its own exit code, so both wrappers keep their
# established exit-code contract while sharing the one rule-set. This function alone ALWAYS refuses a
# reserved name, because on its own it cannot see the host. The caller lifts that refusal in the two cases
# the host reveals: the identity already belongs to the delivered service (a renewal — setup_owns_reserved_name),
# or nothing under the name exists on this host at all (a genuine first install on a bare host —
# setup_reserved_name_free). The reserved list itself is never shortened; only host-visible ownership or
# host-visible absence admits a reserved name.
setup_valid_repo_name() {
  local repo="$1" r
  setup_valid_repo_shape "$repo" || return 1
  for r in $SETUP_RESERVED_REPOS; do
    if [ "$repo" = "$r" ]; then
      echo "repo name '$repo' is reserved (operating-system, package or landscape identity) — a unit and a service account under this name would collide with an existing one" >&2
      return 1
    fi
  done
  return 0
}

# setup_owns_reserved_name <repo> <own-unit-file> <is-update> — decide whether the service being delivered
# ALREADY OWNS the on-host identity that its (reserved) <repo> name would otherwise be refused for. The
# reserved list forbids a service from CLAIMING an identity that is not its own — an operating-system
# account, a third-party package, or a landscape component such as the `holistic` dashboard. But it must
# NOT trap the ONE service to WHICH that identity already belongs: that service has to be able to renew
# itself. The refusal and the admission are two different cases the SPELLING alone cannot tell apart.
#
# Ownership is NEVER guessed from the repository NAME — deriving a decision from the name instead of from
# the delivered artifact and the host is the very fault this whole cascade is unwinding. It is read from
# what the delivery package NAMES and what the HOST confirms, and BOTH must hold:
#   * <is-update> is "1" when systemd already carries OUR OWN unit under the delivered unit name
#     (setup_unit_state = update; a FOREIGN unit was refused by the caller before this). So the host itself
#     confirms WE installed this unit — the name is already in our hands, not merely asked for.
#   * the on-host unit at <own-unit-file> declares `User=<repo>` — so the service ACCOUNT the reserved
#     name would collide with is the very account this, our, unit already runs as. Unit AND account.
# Returns 0 (owns → the reserved name is admitted) only when both hold; 1 otherwise. A first-time setup
# owns nothing (refused); an update whose unit runs as someone else does not own the account (refused).
# SHARED so devlab-install (dev) and devlab-deploy-recv (prod) lift the reserved refusal for the identity's
# rightful owner by ONE rule, and neither invents a second path.
setup_owns_reserved_name() {
  local repo="$1" own_file="$2" is_update="$3"
  [ "$is_update" = 1 ] || return 1
  [ -f "$own_file" ] || return 1
  setup_unit_declares_user "$own_file" "$repo"
}

# setup_reserved_name_free <repo> [<getent>] [<dpkg>] — the THIRD case the reserved list must tell apart,
# the one setup_owns_reserved_name cannot: the identity DOES NOT EXIST YET. The list guards against a
# service CLAIMING an identity that is not its own — but non-existence is not foreign ownership. On a bare
# host `holistic` (a reserved landscape name) owns nothing AND collides with nothing, so the FIRST install
# must be admitted; the protection is against foreign PROPERTY, not against absence. Refusing here is the
# fault that traps the one landscape service that isn't yet on a freshly provisioned host out of ever
# reaching it — the service could only exist where it already exists.
#
# The distinction is EXISTENCE of a foreign identity on the host, read from the host, never guessed from
# the name — the same rule the whole reserved list already stands on ("would collide with an EXISTING
# one"). This function is called by the caller ONLY for a first-time setup (is_update=0): a foreign UNIT
# was already refused before this (setup_unit_state = foreign → die), and our own unit means an update
# (setup_owns_reserved_name decides that case), so the unit is settled and only the ACCOUNT and the
# PACKAGE remain to check. Returns 0 (free → the first install may proceed) only when BOTH hold:
#   * no FOREIGN account under the name. An account is foreign when its shape is not the one THIS setup
#     would create — a system account (uid>0), nologin/false shell, home /var/lib/<repo>. root (uid 0),
#     an interactive login, or a foreign home (www-data → /var/www, daemon → /usr/sbin) is a foreign
#     identity and keeps the name claimed; the SAME shape rule the account guard already applies when it
#     decides whether an existing account may be adopted, so the two never diverge. An account already in
#     our own shape is a prior partial install of ours, not a foreign claim, and does not disqualify.
#   * no PACKAGE installed under the name (dpkg-query "install ok installed"). Every distro package that
#     owns a reserved name also creates its account, so this is belt to the account check — but the list
#     names a package as its own kind of identity, so it is checked in its own right where dpkg exists.
# Returns 1 (claimed → the caller keeps refusing) otherwise. The getent/dpkg seams default to the real
# tools; they exist only so a direct-invocation test can decide what the host carries, exactly as the
# systemctl seam lets a test decide what unit exists. SHARED so the dev installer and the prod receiver
# admit a genuine first-time install of a reserved landscape name by ONE rule, with no second path.
setup_reserved_name_free() {
  local repo="$1" getent_cmd="${2:-getent}" dpkg_cmd="${3:-dpkg-query}" line puid phome pshell
  # a FOREIGN account keeps the name claimed
  if line="$("$getent_cmd" passwd "$repo" 2>/dev/null)" && [ -n "$line" ]; then
    IFS=: read -r _ _ puid _ _ phome pshell <<<"$line"
    [ "${puid:-0}" -gt 0 ] 2>/dev/null || return 1
    [ "${phome:-}" = "/var/lib/$repo" ] || return 1
    case "${pshell:-}" in
      */nologin | */false) : ;;
      *) return 1 ;;
    esac
    # account exists in OUR shape — a prior partial install of ours, not a foreign identity; not disqualifying
  fi
  # a PACKAGE under the name keeps it claimed (checked only where dpkg is present)
  if command -v "$dpkg_cmd" >/dev/null 2>&1; then
    if "$dpkg_cmd" -W -f='${Status}' "$repo" 2>/dev/null | grep -q 'install ok installed'; then
      return 1
    fi
  fi
  return 0
}

# setup_unit_text <repo> <port> — THE systemd unit template. Every value is derived from the two
# validated arguments; the unit runs as User=<repo> (never root), confined by NoNewPrivileges/
# ProtectSystem to its own /var/lib/<repo>. This is the only place this text exists.
setup_unit_text() {
  local repo="$1" port="$2"
  cat <<UNIT
[Unit]
Description=${repo} — a holistic service (generated by devlab-install)
After=network.target

[Service]
User=${repo}
Environment=HOLISTIC_SECRET_FILE=/etc/holistic/jwt-secret
ExecStart=/opt/${repo}/bin/${repo}d --listen 127.0.0.1:${port}
Restart=on-failure
RestartSec=2
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/var/lib/${repo}
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
UNIT
}

# setup_route_text <repo> <port> — THE edge route template. Maps the service's API prefix to its
# loopback port; unit and route always carry the same port because they come from the same call.
# It is a NAKED `handle` block — a Caddy directive with no surrounding site block — because it is
# dropped into the shared route directory and IMPORTED into the edge's site block (see
# setup_edge_caddyfile_text). A naked directive is valid ONLY inside a site block; that is the
# contract these two templates share, and why they live together.
setup_route_text() {
  local repo="$1" port="$2"
  cat <<ROUTE
# Generated by devlab-install. Routes the service API to the daemon.
handle /api/services/${repo}/* {
	reverse_proxy 127.0.0.1:${port}
}
ROUTE
}

# setup_app_route_text <id> <host> <edge-port> <app-port> <www> [<is-dashboard>] — THE edge template for a
# ROOT APPLICATION: an application that owns the WHOLE `/api/*` prefix and serves a face at the root of a
# hostname (not `/api/services/<id>/*` under somebody else's page, like a uniform service).
#
# WHY IT IS A WHOLE SITE BLOCK AND NOT A FRAGMENT. There is more than one such application — the landscape
# dashboard and, on equal footing, DevLab — and each answers the same two paths (`/` and `/api/*`) with
# different content. Two `handle /api/*` fragments in ONE site block are not two answers but one: the
# alphabet decides, and the loser's API disappears (measured on production 2026-08-09 — `devlab.caddy`
# sorted before `holistic.caddy`, so holistic's own API answered 404 through the edge while answering 200
# directly, and nobody could log in to holistic because /api/auth/login reached DevLab). What separates
# them is the HOSTNAME the caller asked for, so each application gets a site block of its OWN, keyed on its
# own name. The predecessor of this template (setup_self_route_text) keyed the same property on the
# REPOSITORY NAME instead — `if [ "$repo" = devlab ]` — which is why exactly one member could ever hold it.
#
# THE DASHBOARD BUNDLE. `import holistic_service_routes` (the shelf of uniform `/api/services/<id>/*`
# fragments) is imported ONLY by the application whose role is `dashboard`: the uniform services are
# reached THROUGH the dashboard, under the dashboard's name. It is imported BEFORE the catch-all
# `handle /api/*` so the more specific per-service path still wins inside the dashboard's own site block.
# For an application that is not the dashboard the shelf is absent — the whole `/api/*` space is that
# application's, and a uniform service is not reachable under its name at all.
setup_app_route_text() {
  local id="$1" host="$2" edge_port="$3" app_port="$4" www="$5" dashboard="${6:-0}"
  { [ -n "$id" ] && [ -n "$host" ] && [ -n "$edge_port" ] && [ -n "$app_port" ] && [ -n "$www" ]; } || {
    echo "setup_app_route_text: needs <id> <host> <edge-port> <app-port> <www> [<is-dashboard>]" >&2; return 1; }
  local bundle=""
  [ "$dashboard" = 1 ] && bundle="	import holistic_service_routes
"
  cat <<ROUTE
# Generated by devlab-install. The root application '${id}' answers under the hostname this host declares
# for it; its whole /api/* space is its own, and its face is served at the root of that name.
http://${host}:${edge_port} {
${bundle}	handle /api/* {
		reverse_proxy 127.0.0.1:${app_port}
	}
	import app_web ${www}
}
ROUTE
}

# ── the edge address: ONE declaration both the edge and the routing layer read ───────────────────────
# WHERE THIS ENVIRONMENT'S EDGE ANSWERS is a single fact about a single entity, and it must be stated in
# exactly ONE place — otherwise the edge binds one address (historically the baked-in ":80") while the
# routing layer forwards to another (e.g. :8080), and every request for a production hostname ends as a
# 502 in front of a face that is listening somewhere else. That declaration lives in RUNTIME
# CONFIGURATION, never baked into a repository (Keine Instanz-Spezifika): a one-line file under the
# shared holistic config dir, beside jwt-secret, holding the address in Caddy site-address form
# (`host:port` or `:port`) — a form that is EQUALLY a valid forward target for the routing layer, so a
# production-side sxgate can own this declaration without changing its shape.
#
# This file is NOT the "dead twin" the prod-target sweep removes. A dead twin is a file that steers
# NOTHING (the one source is a single process's environment). This file steers TWO independent readers
# that cannot share one process's environment — the Caddy edge built here AND the routing layer — so a
# file is the only single source both can read. Both resolve it through setup_edge_address; neither
# guesses the other's default.
SETUP_EDGE_ADDRESS_FILE="${DEVLAB_EDGE_ADDRESS_FILE:-${DEVLAB_HOLISTIC_DIR:-/etc/holistic}/edge-address}"

# setup_valid_edge_address <addr> — true when <addr> is a Caddy site address / forward target: an
# optional host followed by ':' and a port in 1..65535. `:8080` (bind all interfaces) and
# `10.10.0.1:8080` (a specific overlay address) are both valid; a bare port or a bare host is not.
setup_valid_edge_address() {
  local addr="$1" port
  case "$addr" in *:*) : ;; *) return 1 ;; esac
  port="${addr##*:}"
  case "$port" in ''|*[!0-9]*) return 1 ;; esac
  [ "$port" -ge 1 ] && [ "$port" -le 65535 ]
}

# setup_edge_address [<file>] — read the ONE declaration of where this environment's edge answers, from
# the runtime-config file (default $SETUP_EDGE_ADDRESS_FILE). Comments and blank lines are ignored; the
# first real line is the address. This is the SINGLE reader both the edge builder and the routing layer
# use to answer "where does this environment answer?" — asking either yields the same address because
# they read the same file. Returns non-zero (with nothing printed) when the declaration is absent or
# malformed, so a caller NAMES the deficiency instead of guessing a default (Kein stummes Ausbleiben).
setup_edge_address() {
  local file="${1:-$SETUP_EDGE_ADDRESS_FILE}" addr=""
  [ -r "$file" ] || return 1
  addr="$(grep -vE '^[[:space:]]*(#|$)' -- "$file" 2>/dev/null | head -n1)"
  addr="$(printf '%s' "$addr" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
  [ -n "$addr" ] || return 1
  setup_valid_edge_address "$addr" || return 1
  printf '%s' "$addr"
}

# ── the edge HOSTNAMES: which name reaches which root application ───────────────────────────────────
# An environment answers on ONE socket (setup_edge_address above), and behind that socket several ROOT
# APPLICATIONS can stand side by side — the landscape dashboard and, on equal footing, any application
# whose whole `/api/*` space is its own. They are told apart by the NAME the caller asked for, and by
# nothing else: a single site block that accepts every name gives every name the same answer, which is
# exactly the state being removed (measured on production 2026-08-09: three different hostnames, three
# identical pages, and the dashboard's own API unreachable because a second `handle /api/*` won the
# alphabet).
#
# A HOSTNAME IS AN INSTANCE VALUE and lives ONLY in runtime configuration — one file per application,
# named after the application, holding the name it answers to. It is never written into a repository
# (Keine Instanz-Spezifika), and a package can never state it: what the PACKAGE declares is its ROLE
# (setup_read_edge_role), what the HOST declares is the NAME. That split is load-bearing. A package that
# declares itself a root application still gets no name of its own accord; without a name here its
# delivery dies BENANNT rather than quietly taking over the instance.
SETUP_EDGE_HOSTS_DIR="${DEVLAB_EDGE_HOSTS_DIR:-${DEVLAB_HOLISTIC_DIR:-/etc/holistic}/edge/hosts}"

# setup_valid_edge_host <name> — true when <name> has the SHAPE of a hostname the edge can match on:
# letters, digits, dots and hyphens only, at least one dot (a bare label is a machine name, not a name
# an environment is reached under), no empty label and no leading/trailing dot or hyphen. Shape only —
# whether the name actually resolves is the routing layer's business, not the edge's.
setup_valid_edge_host() {
  local h="$1"
  [ -n "$h" ] || return 1
  case "$h" in *[!A-Za-z0-9.-]*) return 1 ;; esac
  case "$h" in .*|*.|-*|*-|*..*|*.-*|*-.*) return 1 ;; esac
  case "$h" in *.*) : ;; *) return 1 ;; esac
  return 0
}

# setup_edge_host <id> [<dir>] — read the hostname THIS host declares for the root application <id>,
# from $SETUP_EDGE_HOSTS_DIR/<id>. The exact twin of setup_edge_address, for the same reason: comments
# and blank lines ignored, first real line trimmed, shape checked, and — decisively — a non-zero return
# WITH NOTHING PRINTED when the declaration is absent or unusable, so the caller NAMES the deficiency
# instead of inventing a name (Kein stummes Ausbleiben). This is the ONE place in the landscape a
# hostname is read from, and it reads it out of runtime configuration.
setup_edge_host() {
  local id="$1" dir="${2:-$SETUP_EDGE_HOSTS_DIR}" host=""
  [ -n "$id" ] || return 1
  [ -r "$dir/$id" ] || return 1
  host="$(grep -vE '^[[:space:]]*(#|$)' -- "$dir/$id" 2>/dev/null | head -n1)"
  host="$(printf '%s' "$host" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
  [ -n "$host" ] || return 1
  setup_valid_edge_host "$host" || return 1
  printf '%s' "$host"
}

# ── THE ROOT APPLICATION: what a host answers with at the root of the instance ───────────────────────
# The root of an instance is the root application of the LANDSCAPE — the dashboard through which every
# service is reached — and never whichever service happens to have laid files on the host. The receiver
# used to hand its edge the state directory of ONE service (devlab's), so a browser asking for the
# instance got that service's login screen instead of the dashboard, and the dashboard's own absence
# stayed invisible behind it (measured on production, 2026-08-09).
#
# WHICH application that is, is decided HERE and once. It is a LANDSCAPE fact, not an instance value: an
# instance chooses its address, its organisation and its paths — it does not choose which product it is.
# Holistic's root application is its dashboard, the service `holistic`; the reserved list above already
# names that identity as belonging "to the landscape as a whole", and this is the same identity read for
# the same reason. A host therefore never derives the root from a name it finds lying around, and no
# runtime switch can point the instance root at an arbitrary service — which is exactly the state being
# removed. What REMAINS instance configuration is where the edge answers (setup_edge_address), not what
# it answers with.
SETUP_ROOT_APP="holistic"

# The directory services are installed under (/opt/<repo>/bin, /opt/<repo>/www). Instance-neutral like
# every path in this library; the env override is a DIRECT-INVOCATION test seam only (sudo's env_reset
# strips it in production, and sshd hands the forced command no environment at all), so a test can build
# a whole fixture host under a temp root without a second copy of the serve-root convention.
SETUP_SERVICE_ROOT="${DEVLAB_SERVICE_ROOT:-/opt}"

# setup_service_www <repo> — THE serve-root convention: where a Holistic service's own web face is
# served from on a host, `/opt/<repo>/www`, symmetric to the `/opt/<repo>/bin` its program is installed
# into. This is the convention a delivery FOLLOWS when it stamps its own serve root (devlab-exec) and the
# yardstick an installer validates that stamp against — the installer itself never derives a destination
# from the repository name; it reads what the package declares (setup_read_web_root).
setup_service_www() {  # <repo>
  local repo="$1"
  [ -n "$repo" ] || return 1
  printf '%s/%s/www' "$SETUP_SERVICE_ROOT" "$repo"
}

# setup_root_app_www — the serve root the INSTANCE ROOT answers from: the root application's own serve
# root. The edge must know it BEFORE any delivery has arrived (a bare host is provisioned first and
# delivered to afterwards), so it comes from the two facts decided above — which service is the root
# application, and where a service serves its face from — and never from what is currently on disk.
setup_root_app_www() {
  setup_service_www "$SETUP_ROOT_APP"
}

# ── THE TWO SHELVES the edge assembles itself from ──────────────────────────────────────────────────
# Under the shared route directory the edge keeps its delivered parts on two shelves, because the parts
# are of two different KINDS and a Caddyfile treats them differently:
#   services/  naked `handle /api/services/<id>/*` FRAGMENTS (setup_route_text) — valid only INSIDE a
#              site block, so they are imported by the application that carries the dashboard bundle.
#   apps/      whole SITE BLOCKS (setup_app_route_text), one per root application, each keyed on its own
#              hostname — imported at the TOP level, where a site block is the only valid thing.
# Mixing the two in one flat directory is what made the edge unable to tell them apart. The names live
# HERE, beside the shell that imports them, so nothing that drops a file into a shelf can disagree with
# the shell about where the shell looks.
setup_edge_apps_dir()     { printf '%s/apps' "$1"; }      # <conf_dir>
setup_edge_services_dir() { printf '%s/services' "$1"; }  # <conf_dir>

# setup_edge_caddyfile_text <conf_dir> <site> — THE host edge shell: the frame the delivered parts are
# assembled into. It contains no application of its own; it declares WHERE the edge listens, defines the
# three pieces the delivered parts are built from, pulls in the apps shelf, and answers honestly for every
# name nobody claimed.
#
# ONE SITE BLOCK PER HOSTNAME. The shell used to be a SINGLE site block that imported every delivered file.
# A single block accepts every name, so all names got the same page, and two applications that each own
# `/api/*` collided inside it — the alphabet decided which one's API existed (measured on production
# 2026-08-09: holistic's own API answered 404 through the edge and nobody could log in, because
# devlab.caddy sorted first). Root applications are therefore separated by the HOSTNAME the caller asked
# for: each brings its own site block on its own name, and the shell only holds them.
#
# WHAT THE SHELL DECLARES, and why each piece is here rather than in a delivered file:
#   default_bind   the edge binds THE DECLARED ADDRESS and nothing else — emitted only when the declared
#                  address carries a host part (`:8080` MEANS all interfaces and gets no line). MEASURED:
#                  without it Caddy binds *:<port> for a host-bearing site label, which on a host whose
#                  edge is meant to answer only on its private overlay address would put every
#                  application on the public internet, past the tunnel that is supposed to front it.
#                  There is deliberately NO `admin` directive: the standard admin endpoint stays, so
#                  `systemctl reload caddy` keeps working exactly as before.
#   holistic_service_routes  the shelf of uniform service fragments, imported by the DASHBOARD application
#                  (and only by it) — a fragment is valid only inside a site block, and the site block it
#                  belongs inside is the dashboard's.
#   app_web        how a root application serves its own face. It tells the two states apart and STATES
#                  both (Kein stummes Ausbleiben): the face is on this host → served, deep links included;
#                  it is NOT → the answer says so with 503, rather than a bare file_server 404 over a
#                  deficiency nobody named.
#   edge_absage    the answer for a name no application claimed.
#
# `auto_https off` is deliberately absent and not needed: every site label the shell and the apps shelf
# emit carries an explicit `http://`, which already tells Caddy not to manage a certificate for it
# (measured). The edge is the plain-HTTP face behind the routing layer, which owns hostnames and TLS.
setup_edge_caddyfile_text() {
  local conf_dir="$1" site="${2:-}" apps services
  if ! setup_valid_edge_address "$site"; then
    echo "setup_edge_caddyfile_text: refusing to build an edge without a declared site address (got '${site}') — the edge address is read from the ONE declaration (setup_edge_address), never guessed" >&2
    return 1
  fi
  apps="$(setup_edge_apps_dir "$conf_dir")"
  services="$(setup_edge_services_dir "$conf_dir")"
  # The declared address is split into the part the edge BINDS and the port every site label carries. A
  # scheme, if the operator wrote one, is not part of either.
  local addr="$site" bind_host="" port
  case "$addr" in *://*) addr="${addr#*://}" ;; esac
  port="${addr##*:}"
  case "$addr" in *:*) bind_host="${addr%:*}" ;; esac
  local global=""
  [ -n "$bind_host" ] && global="{
	default_bind ${bind_host}
}

"
  cat <<EDGE
# Managed by devlab-install-recv — the Holistic edge shell. It holds no application of its own: the
# delivered parts live on two shelves under ${conf_dir} and are assembled here.
#   ${apps}/*.caddy      one whole site block per ROOT APPLICATION, keyed on its own hostname
#   ${services}/*.caddy  naked \`handle\` fragments for uniform services, imported by the dashboard
# A second site block beside these is not in itself a problem — what Caddy refuses as an ambiguous site
# definition is a second block on the SAME address (measured), which is why every root application must
# carry a hostname of its own. Regenerated by devlab-install-recv; do not edit by hand.
${global}(holistic_service_routes) {
	import ${services}/*.caddy
}

(app_web) {
	@face_installed file {
		root {args[0]}
		try_files /index.html
	}
	handle @face_installed {
		root * {args[0]}
		try_files {path} /index.html
		file_server
	}
	handle {
		respond "This application is installed on this host, but its interface is not: nothing is served from {args[0]}. The package's face did not arrive on this host, so there is nothing to show at the root of this name. No other application's interface is served in its place." 503
	}
}

(edge_absage) {
	handle {
		respond "This Holistic instance has no root application answering to the name {host}. A root application — the landscape dashboard (${SETUP_ROOT_APP}) or another application on equal footing — is reached under a hostname this host declares for it, and no application on this host claims this one. No other application's interface is served in its place." 404
	}
}

import ${apps}/*.caddy

http://:${port} {
	import edge_absage
}
EDGE
}

# ── can this process WRITE there, in THIS mount namespace? ───────────────────────────────────────────
# A systemd unit with ProtectSystem=strict mounts the whole hierarchy read-only apart from its
# ReadWritePaths, and that mount namespace is inherited by EVERY child the unit starts — a child that
# sudo raised to root included. Against a read-only mount point, being root does not help: the write
# fails with EROFS no matter who attempts it. So "may I write there" is a question about the NAMESPACE,
# not about rights, and it has to be MEASURED rather than derived from the fact that we are root.
#
# access(2) — which `test -w` calls — answers exactly that question: for root it passes the permission
# bits but still returns EROFS on a read-only mount. That is what makes a plain `-w` the honest probe,
# and it is the probe the approved wrapper renewal has used since 2026-08-06.
#
# The path asked about need not exist yet (a first-time setup writes into directories it also creates),
# so the probe walks up to the nearest EXISTING ancestor: creating /etc/holistic/permissions.d requires
# /etc to be writable, and that is precisely what is asked. Returns 0 when writable, 1 when not.
setup_path_writable() {
  local p="${1:-}" parent
  [ -n "$p" ] || return 1
  while [ ! -e "$p" ]; do
    parent="$(dirname -- "$p")"
    [ "$parent" = "$p" ] && break
    p="$parent"
  done
  [ -w "$p" ]
}

# The directory the ACCOUNT DATABASE lives in. `useradd`/`groupadd` do not merely rewrite /etc/passwd,
# /etc/shadow and /etc/group — they first create their lock files (/etc/passwd.lock …) BESIDE them, so
# the directory itself must be writable. A read-only /etc is exactly what makes an otherwise privileged
# `useradd` print "cannot lock /etc/passwd; try again later." Named here so the one probe above can be
# aimed at it, and overridable only as a direct-invocation test seam like every other path here.
# It is read by the wrappers that SOURCE this library (devlab-install decides its hand-out with it), not
# inside it — shellcheck cannot see across that seam.
# shellcheck disable=SC2034
SETUP_ACCOUNT_DB_DIR="${DEVLAB_ACCOUNT_DB_DIR:-/etc}"

# ── system-account creation: serialised, and resilient to a momentarily-held account database ────────
# Creating the service account writes /etc/passwd (and /etc/group), and the tool that does it —
# `useradd` — takes a short EXCLUSIVE lock on those files while it writes. When two first-time setups
# run on the SAME host at once (two services delivered in one wave), the second's `useradd` can arrive
# while the first still holds that lock; it then refuses with
#   "useradd: cannot lock /etc/passwd; try again later."
# and says "try again later" precisely because the condition is momentary — nothing is wrong with the
# account being created. A setup that reads this refusal as a verdict throws a finished implementation
# away over a scheduling coincidence. Two guards, sharing ONE path (setup_account_cmd), so every caller
# that creates an account behaves identically — no second, similar sibling:
#
#   SERIALISE      A host-wide advisory lock (flock on SETUP_ACCOUNT_LOCK) is held for the whole create,
#                  so two Holistic setups take their turns instead of racing. The second WAITS.
#   RETRY THE LOCK The account database can also be held by a NON-Holistic operation the flock knows
#                  nothing about. So a create that fails with the DATABASE-LOCK signature — and ONLY
#                  that signature — is retried until SETUP_ACCOUNT_LOCK_WAIT seconds have passed; only
#                  then is it a failure, and a loud one. Any OTHER failure (an invalid name, an
#                  exhausted UID range) is a factual rejection repetition cannot cure: it fails at once.
#
# What "momentary" MEANS is defined HERE, once — no caller re-guesses it per site. `useradd`, `groupadd`
# and `usermod` all name a busy database with these two phrases and nothing else does: "cannot lock" is
# emitted only when acquiring the lock fails, and "try again later" is the tool's own advice to retry.
SETUP_ACCOUNT_LOCK_SIGNATURE='cannot lock|try again later'
# A refusal repetition CANNOT cure, judged over the WHOLE output and BEFORE the lock signature. An
# unprivileged `useradd` prints TWO lines — "useradd: Permission denied." AND "useradd: cannot lock
# /etc/passwd; try again later." — and only the first is the real cause; the second merely matches the
# momentary-lock signature above. Reading that second line as a busy database would wait out the whole
# deadline for a right that can never appear, then report "the database stayed locked" — a false cause
# hiding the true one printed one line above. So whenever the output carries a rights refusal ANYWHERE,
# the create fails AT ONCE, and its message names the line that carries the abort, not the last line.
SETUP_ACCOUNT_DENIED_SIGNATURE='permission denied|operation not permitted|must be (root|superuser)'
# Tunables (overridable for tests; the defaults bound a real host's momentary contention):
: "${SETUP_ACCOUNT_LOCK:=/run/lock/holistic-account.lock}"  # the host-wide account-creation lock file
: "${SETUP_ACCOUNT_LOCK_WAIT:=30}"                          # seconds a momentary lock may persist
: "${SETUP_ACCOUNT_LOCK_STEP:=1}"                           # seconds between retry attempts

# setup_account_cmd <describe> <cmd> [args…] — the ONE choke point every account create passes through.
# It holds the host-wide lock (waiting for a busy peer up to the deadline) and runs <cmd>, retrying it
# ONLY while it fails because the account database is momentarily locked. Returns 0 on success, the
# command's own non-zero status on a factual failure, or 75 (EX_TEMPFAIL) when the deadline is reached
# with a peer still holding the lock. It never hangs: both the wait for the peer and the retry are
# bounded. Runs in a subshell so the private lock fd is released the instant the create is done.
setup_account_cmd() {
  local describe="$1"; shift
  mkdir -p -- "$(dirname -- "$SETUP_ACCOUNT_LOCK")" 2>/dev/null || true
  (
    # Acquire the host-wide account lock on fd 9; wait up to the deadline for a busy peer, then report
    # THAT as its own bounded failure rather than hanging. (No flock ⇒ degrade to retry-only below.)
    # The fd-9 open is wrapped in a brace group so its stderr suppression is scoped to the open alone —
    # `exec 9>file 2>/dev/null` would redirect the WHOLE subshell's stderr and swallow every later message.
    if command -v flock >/dev/null 2>&1 && { exec 9>"$SETUP_ACCOUNT_LOCK"; } 2>/dev/null; then
      if ! flock -w "$SETUP_ACCOUNT_LOCK_WAIT" 9; then
        echo "devlab: another setup on this host held the account lock ($SETUP_ACCOUNT_LOCK) for more than ${SETUP_ACCOUNT_LOCK_WAIT}s while creating the account for '$describe' — stopping rather than hanging" >&2
        exit 75
      fi
    fi
    setup_account_retry "$describe" "$@"
  )
}

# setup_account_retry <describe> <cmd> [args…] — run <cmd>, retrying ONLY on the database-lock signature,
# bounded by SETUP_ACCOUNT_LOCK_WAIT. Assumes the caller already serialised (setup_account_cmd). The
# command's stderr is captured so the signature can be judged and, on a factual failure, surfaced intact.
setup_account_retry() {
  local describe="$1"; shift
  local elapsed=0 step="$SETUP_ACCOUNT_LOCK_STEP" err rc denied
  [ "$step" -ge 1 ] 2>/dev/null || step=1
  while :; do
    err="$("$@" 2>&1 1>/dev/null)"; rc=$?
    [ "$rc" -eq 0 ] && return 0
    # Judge the WHOLE output for an unrecoverable rights refusal FIRST — before the lock signature — and
    # fail immediately with the very line that carries it, never after waiting out the lock deadline for
    # a right that cannot appear. This is the line "one above" the busy message that was being overlooked.
    denied="$(printf '%s\n' "$err" | grep -Ei "$SETUP_ACCOUNT_DENIED_SIGNATURE" | head -n1)"
    if [ -n "$denied" ]; then
      echo "devlab: creating the account for '$describe' was refused for lack of rights — ${denied}" >&2
      return "$rc"
    fi
    if printf '%s' "$err" | grep -Eqi "$SETUP_ACCOUNT_LOCK_SIGNATURE"; then
      if [ "$elapsed" -ge "$SETUP_ACCOUNT_LOCK_WAIT" ]; then
        echo "devlab: the account database stayed locked for more than ${SETUP_ACCOUNT_LOCK_WAIT}s while creating the account for '$describe' — last message: ${err}" >&2
        return "$rc"
      fi
      sleep "$SETUP_ACCOUNT_LOCK_STEP" 2>/dev/null || true
      elapsed=$(( elapsed + step ))
      continue
    fi
    # A factual rejection repetition cannot cure — fail now, loudly, with the tool's own words.
    [ -n "$err" ] && printf '%s\n' "$err" >&2
    return "$rc"
  done
}

# setup_ensure_account <repo> — the service's own identity: a nologin SYSTEM account whose home is
# /var/lib/<repo>, created only when absent (idempotent). This is what `User=<repo>` in the unit runs
# as; devlab-install and devlab-deploy-recv create it identically at first-time setup. The create runs
# through setup_account_cmd, so a momentarily-locked account database makes it WAIT, not fail.
setup_ensure_account() {
  local repo="$1"
  # DIRECT-INVOCATION TEST SEAM (the same one setup_ensure_secrets carries; sudo's env_reset and sshd's
  # forced command both strip it, so it can never be set in operation): creating an operating-system
  # account is the one step of an install that no unprivileged process can perform even against a
  # fixture host, so under the seam the identity step is skipped and SAID to be skipped. It lets a whole
  # delivery be driven end to end off a production host — which is how "the face survives the run" is
  # measured on a host rather than read off a report.
  if [ "${DEVLAB_RECV_TEST:-0}" = 1 ]; then
    echo "devlab: (test seam) service account '$repo' not created — unprivileged run against a fixture host" >&2
    return 0
  fi
  setup_account_cmd "$repo" setup_ensure_account_locked "$repo" || return 1
  install -d -o "$repo" -g "$repo" -m 0755 "/var/lib/$repo"
}

# setup_ensure_account_locked <repo> — the create itself, run UNDER the host-wide lock. The existence
# re-check lives here (not in the caller) so two setups for the same repo cannot both decide it is
# absent and then collide — the loser of the lock finds it present and returns.
#
# On a BARE host a same-named GROUP may already exist without the matching user — the landscape's own
# prerequisites create a `holistic` group before any service is installed. `useradd` defaults (Debian
# USERGROUPS_ENAB) to creating a per-user group of the same name, which then FAILS ("group holistic
# exists") and aborts the whole first-time setup. So when a group under the name is already present, the
# account is created bound to THAT group (-g <repo>) instead of trying to mint a second one; when no such
# group exists the default per-user group is created as before.
setup_ensure_account_locked() {
  local repo="$1"
  getent passwd "$repo" >/dev/null 2>&1 && return 0
  if getent group "$repo" >/dev/null 2>&1; then
    useradd --system --shell /usr/sbin/nologin --home-dir "/var/lib/$repo" -g "$repo" "$repo"
  else
    useradd --system --shell /usr/sbin/nologin --home-dir "/var/lib/$repo" "$repo"
  fi
}

# setup_fragment_path <repo> [<systemctl>] — echoes the FragmentPath systemd already knows for
# <repo>.service (whitespace-trimmed), empty when systemd knows no unit. The QUESTION is asked of
# systemd, not of a directory, so a vendor unit under /lib or /usr/lib — which a path test in
# /etc/systemd/system would not see — is visible and can be refused instead of silently shadowed.
setup_fragment_path() {
  local repo="$1" systemctl="${2:-systemctl}"
  "$systemctl" show -p FragmentPath --value "${repo}.service" 2>/dev/null | tr -d '[:space:]'
}

# setup_unit_declares_user <unit-file> <repo> — a DELIVERED unit is trusted only if it runs as its
# own service account: it must carry exactly `User=<repo>`. A unit that starts something else, or runs
# as root, or omits User= altogether, fails this and is refused (returns non-zero). devlab-install
# never needs this — it AUTHORS the line — but the production receiver, which INSTALLS a unit built
# elsewhere, must verify it before trusting it.
setup_unit_declares_user() {
  local file="$1" repo="$2"
  grep -Eq "^[[:space:]]*User[[:space:]]*=[[:space:]]*${repo}[[:space:]]*$" -- "$file"
}

# setup_delivered_unit_name <setup-dir> <repo> — the unit name the DELIVERED setup product carries, so
# the installer READS the unit's name from the package instead of ERRECHNET-ing it from the repository
# name. A service whose unit legitimately differs from its repository (a dashboard whose unit is
# `<repo>-dashboard.service`) ships that unit in setup/, and this is where its real name is recovered.
# The rule, in order:
#   * no setup/ dir, or no *.service in it        → echo nothing → the caller falls back to <repo> (the
#                                                    unchanged derivation for a package that ships no unit);
#   * setup/<repo>.service present                → <repo> (the conventional name a uniform service's
#                                                    generated product carries — unambiguous, wins outright);
#   * exactly one other *.service                 → that file's stem (the divergent, delivered name);
#   * more than one and none named <repo>.service → nothing (ambiguous: not a guess, the caller falls back).
# It echoes the bare unit NAME (no `.service` suffix, the form systemctl and the unit-file path both take).
setup_delivered_unit_name() {
  local dir="$1" repo="$2" f count=0 only="" base
  [ -d "$dir" ] || return 0
  if [ -f "$dir/$repo.service" ]; then printf '%s' "$repo"; return 0; fi
  for f in "$dir"/*.service; do
    [ -e "$f" ] || continue
    count=$((count + 1)); only="$f"
  done
  [ "$count" -eq 1 ] || return 0
  base="$(basename -- "$only")"
  printf '%s' "${base%.service}"
}

# setup_valid_unit_name <unit-name> — the SHAPE gate a DELIVERED unit name must pass before it reaches a
# systemctl argument or a unit-file path. A unit name may legitimately differ from the repository name
# (it need not equal it), but it is still a name the installer will hand to systemd and interpolate into
# /etc/systemd/system/<name>.service, so it must be a safe token: the holistic service grammar widened
# only to admit the longer, dashed compound names a delivered unit carries (<repo>-dashboard), never a
# path separator, a traversal, or a systemd-reserved prefix. The deeper safety — that the unit is OURS
# and runs as our own account — stays with the foreign-unit refusal (setup_unit_state) and the User=
# check (setup_unit_declares_user); this is only the lexical gate. Prints the reason and returns 1 on a
# bad name, like setup_valid_repo_name, so each caller keeps its own exit-code contract.
setup_valid_unit_name() {
  local name="$1" r
  [[ "$name" =~ ^[a-z][a-z0-9-]{2,60}$ ]] || { echo "invalid delivered unit name '$name' (want ^[a-z][a-z0-9-]{2,60}\$)" >&2; return 1; }
  case "$name" in
    *--* | *-) echo "invalid delivered unit name '$name' (no doubled and no trailing dash)" >&2; return 1 ;;
    systemd-*) echo "delivered unit name '$name' lies in systemd's own namespace" >&2; return 1 ;;
  esac
  for r in $SETUP_RESERVED_REPOS; do
    if [ "$name" = "$r" ]; then
      echo "delivered unit name '$name' is reserved (operating-system, package or landscape identity)" >&2
      return 1
    fi
  done
  return 0
}

# setup_unit_state <unit-name> <own-unit-file> [<systemctl>] — the SHARED first-time-vs-update-vs-foreign
# decision, asked of SYSTEMD (setup_fragment_path), not of a directory, so a vendor unit under /lib or
# /usr/lib is visible and refused rather than silently shadowed. It echoes exactly one of:
#   first-time     systemd knows NO unit under this name AND our own file is absent → install the setup
#   update         the unit is OURS: its FragmentPath is <own-unit-file>, or systemd knows none but our
#                  file already exists → replace the program and restart
#   foreign:<frag> a unit under this name exists that this receiver did not install → the caller refuses
# The uniform service branch and the self (devlab) branch make the IDENTICAL decision through this one
# function; only the unit NAME and the own-file PATH differ (a layout difference, never a logic one), so
# the branches can never drift apart on what "already set up" means.
setup_unit_state() {
  local unit_name="$1" own_file="$2" systemctl="${3:-systemctl}" frag
  frag="$(setup_fragment_path "$unit_name" "$systemctl")"
  case "$frag" in
    "$own_file") echo update ;;
    "") if [ -e "$own_file" ]; then echo update; else echo "first-time"; fi ;;
    *) echo "foreign:$frag" ;;
  esac
}

# setup_prepare_route <artifact-dir> <repo> <port> <conf-dir> <delivered-route> <staged-out> — THE ONE
# switch that decides HOW a delivery is reached from outside, and the only place a route of any kind comes
# into being. On success it prints — as its ENTIRE standard output — the absolute path the route must be
# installed at, and writes the route's text into <staged-out>. On failure it prints the reason and returns
# non-zero; nothing is written where the edge could see it.
#
# WHY IT IS SHARED. The dev installer (devlab-install) and the production receiver (devlab-deploy-recv)
# both put routes on a host. Two receivers with two edge paths is two edges: the property that decides the
# shape of a route would be judged twice and could be judged differently, which is the class of fault this
# whole change removes. So the judgement lives HERE and is called from both; neither writes a rule of its
# own, and the caller's remaining job is unchanged — hand the staged text to setup_install_route, which
# validates the ASSEMBLED edge and unwinds on failure exactly as before.
#
# <port> is the loopback port THE UNIT BINDS (setup_unit_listen_port), not a port anybody computed: the
# route must reach the daemon that is actually there.
#
# THE THREE ROLES:
#   service                 a naked `/api/services/<repo>/*` fragment on the services shelf, reached under
#                           the dashboard's name. Unchanged in every respect — including that a DELIVERED
#                           route file, when the package ships one, is installed verbatim as before.
#   application, dashboard  a whole site block on the apps shelf, on the hostname THIS HOST declares for
#                           it. Two things must hold and both are refused BY NAME when they do not:
#                             * the host declares a name for it (setup_edge_host) — a package cannot name
#                               itself; without a name the delivery fails instead of taking over the edge;
#                             * the package declares where its face is served from (web.root), inside its
#                               own territory — a root application without a face has nothing at the root
#                               of its name.
#                           `dashboard` additionally carries the uniform services' shelf, and there can be
#                           only ONE such application per instance: a second would silently take the whole
#                           landscape's services with it, so it is refused while the first still stands.
setup_prepare_route() {
  local art="$1" repo="$2" port="$3" conf="$4" deliv="$5" out="$6"
  local role dest apps host www site edge_port reason other othername
  role="$(setup_read_edge_role "$art")" || return 1
  case "$role" in
    service)
      dest="$(setup_edge_services_dir "$conf")/${repo}.caddy"
      if [ -n "$deliv" ] && [ -f "$deliv" ]; then
        cat -- "$deliv" > "$out" || { echo "could not stage the delivered route of '$repo'" >&2; return 1; }
      else
        setup_route_text "$repo" "$port" > "$out" || return 1
      fi
      ;;
    application|dashboard)
      apps="$(setup_edge_apps_dir "$conf")"
      dest="$apps/${repo}.caddy"
      if ! host="$(setup_edge_host "$repo")"; then
        echo "'$repo' declares itself a root application (edge.role=$role), but this host names no hostname for it in $SETUP_EDGE_HOSTS_DIR/$repo — a root application is reached under a name, and the name is the HOST's to give, never the package's; declare it when setting the host up: devlab-install-recv --edge-host $repo=<name>" >&2
        return 1
      fi
      if ! site="$(setup_edge_address)"; then
        echo "'$repo' is a root application, but this environment's edge address is not declared in $SETUP_EDGE_ADDRESS_FILE — its site block cannot name a port; restate it with devlab-install-recv --provision --edge-address <host:port>" >&2
        return 1
      fi
      case "$site" in *://*) site="${site#*://}" ;; esac
      edge_port="${site##*:}"
      www="$(setup_read_web_root "$art")"
      if [ -z "$www" ]; then
        echo "'$repo' declares itself a root application (edge.role=$role), but its package declares no serve root (no web.root) — a root application answers the root of its own hostname with its own face; there is nothing to serve there" >&2
        return 1
      fi
      if ! reason="$(setup_valid_web_root "$www" "$repo" 2>&1)"; then
        echo "$reason" >&2; return 1
      fi
      if [ "$role" = dashboard ]; then
        # ONE dashboard per instance. Without this refusal the uniform services' shelf would simply follow
        # whichever root application was delivered last, and every service would change name silently.
        for other in "$apps"/*.caddy; do
          [ -e "$other" ] || continue
          [ "$other" = "$dest" ] && continue
          grep -qE '^[[:space:]]*import[[:space:]]+holistic_service_routes[[:space:]]*$' -- "$other" 2>/dev/null || continue
          othername="$(basename -- "$other" .caddy)"
          echo "'$othername' is already the dashboard of this instance (it carries the uniform services' routes at $other) — '$repo' cannot be a second one; an instance has exactly one dashboard, and the services hang under it" >&2
          return 1
        done
      fi
      setup_app_route_text "$repo" "$host" "$edge_port" "$port" "$www" "$([ "$role" = dashboard ] && echo 1 || echo 0)" > "$out" || return 1
      ;;
    *)
      echo "unknown edge role '$role' for '$repo' (known: $SETUP_EDGE_ROLES)" >&2
      return 1
      ;;
  esac
  printf '%s' "$dest"
}

# setup_install_route <deliv-route> <route-file> <caddy_bin> <caddy_main> <systemctl> [<also-remove>...] —
# install a DELIVERED edge route into the SHARED conf.d directory, then PROVE the assembled edge still
# validates and reloads. The directory serves every OTHER service of the host, so an unparseable file of
# ours breaks the whole edge at the next reload: OUR file — and every <also-remove> path (the unit just
# written, so a failed route step does not leave a half setup a later run reads as an update) — is taken
# BACK OUT when validation or reload fails, leaving the edge on its last good configuration. On failure
# it echoes the reason and returns: 4 caddy validate/reload failed, 5 the shared config cannot be
# validated at all. On success it returns 0. The caller maps the code to its own die/exit. It is SHARED
# so the uniform service branch and the self branch install a route the identical, self-unwinding way.
setup_install_route() {
  local deliv="$1" route_file="$2" bin="$3" main="$4" systemctl="$5"; shift 5
  local extra=("$@") validate_out own=(-o root -g root)
  [ "${DEVLAB_RECV_TEST:-0}" = 1 ] && own=()
  # The shelf the route belongs on (services/ or apps/) is created when absent: a host provisioned before
  # the shelves existed still receives its first route without a separate hand step.
  install -d "${own[@]}" -m 0755 "$(dirname -- "$route_file")"
  install "${own[@]}" -m 0644 "$deliv" "$route_file"
  if command -v "$bin" >/dev/null 2>&1 && [ -f "$main" ]; then
    if ! validate_out="$("$bin" validate --config "$main" 2>&1)"; then
      rm -f "$route_file" "${extra[@]}"
      # MEASURED: caddy reports a parse fault in an IMPORTED file against the MAIN file's line number
      # ("unexpected EOF, at /etc/caddy/Caddyfile:<n>"), so its message sends the reader to the wrong file.
      # The file we just laid down is therefore named here, by us, beside caddy's own words.
      echo "the edge configuration does not validate with the route just written ($route_file — caddy reports the line of the main file $main, not of the imported one) — route and unit removed, edge untouched: $(printf '%s' "$validate_out" | tail -n 3)" >&2
      return 4
    fi
  else
    rm -f "$route_file" "${extra[@]}"
    echo "cannot validate the shared edge configuration ($bin / $main) — refusing to write into a config directory serving every other service" >&2
    return 5
  fi
  if ! "$systemctl" reload caddy; then
    rm -f "$route_file" "${extra[@]}"
    echo "caddy reload failed — route and unit removed so the edge keeps its last good configuration" >&2
    return 4
  fi
  return 0
}

# setup_install_rights <deliv-rights> <perms-dir> <repo> — install a DELIVERED rights manifest (a
# validated FILE COPY, never executed) into the shared permissions directory and create the hp_* system
# groups it declares. SHARED so the rights half of a first-time setup is identical for a uniform service
# and for the self repo.
setup_install_rights() {
  local deliv="$1" perms_dir="$2" repo="$3" g
  install -d -m 0755 "$perms_dir"
  install -m 0644 "$deliv" "$perms_dir/$repo.json"
  grep -oE '"group"[[:space:]]*:[[:space:]]*"hp_[a-z0-9_]+"' "$deliv" | grep -oE 'hp_[a-z0-9_]+' | sort -u |
    while read -r g; do groupadd -f "$g"; done
}

# ── instance secrets: minted ON the host, never transported ────────────────────────────────────────
# A delivered unit names the secrets its service reads (Environment=HOLISTIC_SECRET_FILE=/etc/holistic/
# jwt-secret …). A blank host has none of them, so a service that installs cleanly still dies at start
# ("no JWT secret …") and loops. The setup of a host therefore MINTS its instance secrets, HERE, from
# what the services actually demand — derived from the unit, never a hand-kept list that goes stale.
#
# THE BOUNDARY: a secret belongs to ONE environment. It is generated on the host that runs the service
# and it never leaves — not by hand, not by the chain, in no direction. Two hosts sharing a secret are
# one environment, not two. So these functions MINT (from the kernel CSPRNG) and NAME; they never read
# a secret off another host and never emit one.

# The shared landscape group that guards instance secrets. A minted secret is root-owned and readable
# by this group (mode 0640); each service account joins it, so every service reads the ONE shared
# jwt-secret without the secret being world-readable. `holistic` is a landscape identity (already in
# SETUP_RESERVED_REPOS — no service may take the name). Overridable ONLY as a direct test seam.
SETUP_SECRET_GROUP="${DEVLAB_SECRET_GROUP:-holistic}"
# The directory the landscape keeps its instance secrets in. Instance-neutral default; the env override
# is the direct-invocation test seam, mirroring the wrappers' path seams.
SETUP_HOLISTIC_DIR="${DEVLAB_HOLISTIC_DIR:-/etc/holistic}"

# setup_unit_secret_files <unit-file> — DERIVE the /etc/holistic/<file> secret paths a unit demands,
# read from the unit itself (its Environment=/EnvironmentFile= values and any other reference). This is
# the SINGLE SOURCE of "which secrets": add a secret to a service's unit and the host mints it; the set
# never goes stale because it is read from the unit, not maintained here. The shared state directories
# (permissions.d, config.d) are host state, not secrets, and are excluded. One path per line, sorted
# and de-duplicated.
setup_unit_secret_files() {
  local file="$1"
  [ -r "$file" ] || return 0
  grep -hoE "${SETUP_HOLISTIC_DIR}/[A-Za-z0-9._-]+" -- "$file" 2>/dev/null \
    | grep -vE "/(permissions\.d|config\.d)$" \
    | sort -u || true
}

# setup_secret_is_generatable <path-or-name> — a secret is GENERATABLE (a random landscape token this
# host can mint on its own) when its name ends in `-secret`: the landscape convention for an INTERNALLY
# shared secret (jwt-secret, notify-secret, <svc>-internal-secret). Anything else referenced under
# /etc/holistic is a credential to a FOREIGN service (e.g. an `.env` of external access keys) that comes
# from OUTSIDE and cannot be minted here — it is NAMED as missing, never silently skipped. This is a
# RULE, not a list, so it stays correct as services add secrets.
setup_secret_is_generatable() {
  case "${1##*/}" in *-secret) return 0 ;; *) return 1 ;; esac
}

# setup_generate_secret_value — a fresh high-entropy token minted from THIS host's kernel CSPRNG. It is
# never derived from anything transported in and never leaves the host.
setup_generate_secret_value() {
  head -c 48 /dev/urandom | base64 | tr -d '\n='
}

# setup_ensure_secrets <repo> <unit-file> — make this host's instance secrets EXIST before <repo> is
# started, derived from what its unit demands (setup_unit_secret_files). It runs on the host that will
# run the service and mints every generatable secret HERE, so no secret is ever transported. For each
# referenced /etc/holistic secret:
#   * already present      → left exactly as is (idempotent; a host's own secret is NEVER overwritten,
#                            so re-running never rotates a live secret and never adopts a foreign one).
#   * generatable, absent  → minted from the CSPRNG, root:<group> 0640, and the group is created and
#                            <repo> joined so the service can read the shared secret.
#   * external, absent     → NAMED (a "MISSING-SECRET: <name>" line plus a human sentence) as an outside
#                            credential this host cannot mint — not swallowed. Kein stummes Ausbleiben.
# It returns 0 even when an external secret is missing: a missing OUTSIDE credential is a NAMED
# condition, not a setup failure — the host is still built and the service still starts (it may run
# degraded until the operator provides the credential). Idempotent, so it also self-heals a host that
# was set up before this fix and has a unit but no secret.
setup_ensure_secrets() {
  local repo="$1" unit="$2" path name seam=0
  [ "${DEVLAB_RECV_TEST:-0}" = 1 ] && seam=1
  local own=(-o root -g "$SETUP_SECRET_GROUP"); [ "$seam" = 1 ] && own=()

  mkdir -p "$SETUP_HOLISTIC_DIR"
  if [ "$seam" != 1 ]; then
    groupadd -f "$SETUP_SECRET_GROUP" >/dev/null 2>&1 || true
    # the service account joins the secret group so it can READ the shared secret (root:group 0640)
    if getent passwd "$repo" >/dev/null 2>&1 \
       && ! id -nG "$repo" 2>/dev/null | tr ' ' '\n' | grep -qx "$SETUP_SECRET_GROUP"; then
      gpasswd -a "$repo" "$SETUP_SECRET_GROUP" >/dev/null 2>&1 \
        || usermod -aG "$SETUP_SECRET_GROUP" "$repo" >/dev/null 2>&1 || true
    fi
  fi

  while IFS= read -r path; do
    [ -n "$path" ] || continue
    name="${path##*/}"
    if [ -e "$path" ]; then
      echo "devlab: instance secret '$name' already present on this host — left as is (a host's own secret is never overwritten)" >&2
      continue
    fi
    if setup_secret_is_generatable "$path"; then
      local tmp; tmp="$(mktemp)"
      ( umask 077; setup_generate_secret_value > "$tmp" )
      install "${own[@]}" -m 0640 -- "$tmp" "$path"
      rm -f -- "$tmp"
      echo "devlab: minted instance secret '$name' on this host (root:$SETUP_SECRET_GROUP 0640) — it never leaves this host" >&2
    else
      echo "MISSING-SECRET: $name"
      echo "devlab: instance secret '$name' comes from OUTSIDE (a foreign-service credential) and cannot be minted here — provide '$path' (root:$SETUP_SECRET_GROUP 0640) and restart '$repo'; until then the parts of '$repo' that need '$name' will not work" >&2
    fi
  done <<EOF
$(setup_unit_secret_files "$unit")
EOF
  return 0
}

# setup_plan_secrets <unit-file> — the --check counterpart of setup_ensure_secrets: it emits a PLAN
# line per secret the unit demands (mint-if-absent for generatable, name-if-absent for external),
# WITHOUT any effect, so a dry run proves what the real setup would do.
setup_plan_secrets() {
  local unit="$1" path name
  while IFS= read -r path; do
    [ -n "$path" ] || continue
    name="${path##*/}"
    if setup_secret_is_generatable "$path"; then
      echo "PLAN: mint instance secret '$name' on this host if absent (root:$SETUP_SECRET_GROUP 0640; never transported)"
    else
      echo "PLAN: instance secret '$name' comes from OUTSIDE — NAMED as missing if absent, never minted here"
    fi
  done <<EOF
$(setup_unit_secret_files "$unit")
EOF
}

# setup_unit_listen_port <unit-file> — the loopback port the unit's ExecStart binds, or empty. Two forms
# occur across the build kinds: the go-daemon template's `--listen 127.0.0.1:<port>` (a colon, read first
# because a loopback bind is unambiguous) and a python-app's `--port <port>` / `--port=<port>` (uvicorn).
# The honest running gate dials this port to prove the service STAYS up; a unit whose port cannot be read
# is proven by unit-activity alone. Keep in step with the Go twin deploy.DeliveredUnitPort — a dev gate
# that dialed a computed port while the delivered unit bound another booked a live service as failed.
setup_unit_listen_port() {
  local file="$1" port
  [ -r "$file" ] || return 0
  port="$(grep -oE '127\.0\.0\.1:[0-9]+' -- "$file" 2>/dev/null | grep -oE '[0-9]+$' | head -n1 || true)"
  [ -n "$port" ] || port="$(grep -oE -e '--port[=[:space:]]+[0-9]+' -- "$file" 2>/dev/null | grep -oE '[0-9]+$' | head -n1 || true)"
  printf '%s' "$port"
}

# ── A SERVE ROOT IS READABLE BY ITS ROLE, NOT BY THE ACCOUNT THAT BUILT IT ────────────────────────
# A web face is copied into a serve root with `rsync -a`, which carries the SOURCE's owner and mode.
# The source is the artifact built on the workbench under the unprivileged build account, so with a
# group-only umask the delivered tree arrives drwxr-x--- / uid-of-the-builder — and the edge webserver,
# a DIFFERENT account entirely, then answers 404 over a page that is present. A serve root is not a
# private directory: it is public by role, because the browser fetches every byte of it through the
# edge. So the readability comes from the SERVING side, never from whoever happened to build.
#
# These two functions are the ONE place that rule lives, shared by every serve-root copy — the dev
# installer's self web and dashboard serve roots (devlab-install) and the production receiver's web
# root (devlab-deploy-recv) — so no site re-implements it and none can drift. They are the symmetric
# twin of the `chmod -R a+rX /opt/<repo>` the program install applies so the service account can read
# and run the interpreter it did not build.

# setup_serve_root_readable <serve-root> — DERIVE the permissions from the public role: directories
# traversable, files readable for every reader (a+rX), regardless of who built or under which umask.
# World-read (rather than adding the webserver to a group) needs no knowledge of the webserver's
# account — instance configuration this template does not and should not carry — and makes the tree
# reachable for ANY edge identity.
setup_serve_root_readable() {  # <serve-root>
  chmod -R a+rX "$1"
}

# setup_serve_root_check <serve-root> — PROVE the delivered serve root is actually reachable by the
# edge (Kein stummes Ausbleiben): a delivery whose result the webserver cannot read is NOT complete and
# says so, never a silent green. The webserver's account is instance configuration this template does
# not carry, so we prove the ROLE property that holds for EVERY account: an unprivileged reader can read
# the start page. Running as root (production, via sudo) we DROP to `nobody` — the nologin account on
# every host — and read index.html: if `nobody` can read it, the edge can too, whatever its account.
# Without root (the direct-invocation test seam, where no privilege drop is possible) we check the same
# property on the mode bits: the other-read bit must be set. It echoes the named reason to stderr and
# RETURNS non-zero (it does not die), so each caller decides what to report first.
setup_serve_root_check() {  # <serve-root> → 0 if the edge can read the start page, else 1 (reason on stderr)
  local index="$1/index.html"
  [ -f "$index" ] || { echo "serve root $1 has no index.html after delivery — the browser has no start page to fetch (delivery incomplete)" >&2; return 1; }
  if [ "$(id -u)" = 0 ] && command -v runuser >/dev/null 2>&1 && getent passwd nobody >/dev/null 2>&1; then
    runuser -u nobody -- test -r "$index" && return 0
    echo "the delivered start page $index is NOT readable by an unprivileged reader ('nobody') — the webserver would answer 404 over a present page; the serve root's permissions do not match its public role (delivery incomplete)" >&2
    return 1
  fi
  local mode; mode="$(stat -c %a "$index" 2>/dev/null || echo 0)"
  [ "$(( 8#${mode:-0} & 4 ))" = 4 ] && return 0
  echo "the delivered start page $index lacks other-read (mode $mode) — the webserver could not read it (404 over a present page; delivery incomplete)" >&2
  return 1
}

# ── THE DELIVERED FACE: a package that carries a web bundle gets it INSTALLED ─────────────────────
# A service's face travels in the artifact as `web/` (the built SPA). It used to be installed for
# exactly ONE repository — the self repo, in a branch of its own — so every other service's face rode
# along to the host and was dropped there: the package carried a start page, the host never received
# one, and the delivery still reported success (measured on production 2026-08-09: the holistic
# dashboard's bundle sat complete in the staging directory while /opt/holistic/www did not exist).
#
# WHERE the face belongs is said by the DELIVERY, in `web.root` beside `build.kind` — never derived by
# the receiver from the repository name, which is the same defect the unit name, the unit port and the
# build kind were each taken out of before. The installer reads that declaration, judges it, and copies
# there. It judges it because a package must not be able to aim a root-run `rsync --delete` wherever it
# likes: the declared root must lie in the SERVICE'S OWN territory (its /opt/<repo> tree or its
# /var/lib/<repo> state tree), so a delivery can place its face inside itself and nowhere else.

# setup_read_web_root <artifact-dir> — echo the serve root a package DECLARES (the trimmed first line of
# <artifact-dir>/web.root), or nothing when absent. Never validated here: the caller runs
# setup_valid_web_root, so an absent declaration and a bad one get their own named refusals.
setup_read_web_root() {  # <artifact-dir>
  local dir="$1"
  [ -r "$dir/web.root" ] || return 0
  head -n1 -- "$dir/web.root" | tr -d '[:space:]'
}

# ── THE EDGE ROLE: how a package is reached from outside, said by the package ─────────────────────
# Whether a delivery is reached at `/api/services/<id>/*` under the dashboard's name or owns a hostname
# and the whole `/api/*` space beneath it is a PROPERTY OF THE PACKAGE. It used to be derived from the
# repository NAME — `if [ "$repo" = devlab ]` in two places in devlab-exec — which is why exactly one
# member of the landscape could ever have it, and why the second one that grew into the same role
# (holistic, the dashboard) collided with the first instead of standing beside it.
#
# The package states it in the ONE file a repo states delivery deviations in (holistic-service.json,
# field edge.role); the build DERIVES this stamp from that declaration and travels it with the artifact,
# exactly as build.kind and web.root travel. The installer READS the stamp. It is never executed and it
# grants nothing: the stamp says which SHAPE of route to write, while the NAME that route answers to
# comes from the host alone (setup_edge_host). A package that declares itself a root application on a
# host that declares no name for it does not take the instance over — its delivery dies BENANNT.
SETUP_EDGE_ROLES="service application dashboard"

# setup_valid_edge_role <role> — 0 for exactly the three words the closed list names, 1 for anything
# else. There is no fourth role and none is inferred.
setup_valid_edge_role() {
  case "${1:-}" in service|application|dashboard) return 0 ;; *) return 1 ;; esac
}

# setup_read_edge_role <artifact-dir> — echo the edge role a package DECLARES (the trimmed first line of
# <artifact-dir>/edge.role). An ABSENT file is `service`: the unprivileged default, the shape every
# uniform service has, and never the other way round — a package that says nothing must not be able to
# fall into the role that owns a whole hostname. A file that says anything OTHER than the three known
# words is a NAMED failure (reason on stderr, nothing on stdout, non-zero), never a fallback: a value we
# do not understand is not evidence of a service.
setup_read_edge_role() {  # <artifact-dir>
  local dir="$1" role
  [ -r "$dir/edge.role" ] || { printf 'service'; return 0; }
  role="$(head -n1 -- "$dir/edge.role" | tr -d '[:space:]')"
  if ! setup_valid_edge_role "$role"; then
    echo "the package declares the edge role '$role', which is not one of: $SETUP_EDGE_ROLES — refusing to guess how this delivery is reached from outside" >&2
    return 1
  fi
  printf '%s' "$role"
}

# ── reading the REPOSITORY's declaration at build time ────────────────────────────────────────────
# The artifact stamps above are DERIVED, at build time, from the one file a repository states delivery
# deviations in: holistic-service.json in its root. The build reads it here; the backend reads the same
# file in Go (deploy.Detect / edgeRoleOf). Two readers of one file is deliberate and is the same twin
# arrangement the unit's listen port already has (setup_unit_listen_port / DeliveredUnitPort): the build
# wrapper runs without the daemon, and the daemon judges without the wrapper. A test pins them to the
# same answer so they cannot drift.
SETUP_DECL_FILE="holistic-service.json"

# setup_decl_field <file> <object> <field> — the string value of <object>.<field>, or nothing. Small by
# intent: the declaration is a short, hand-written file of named scalars, and this reads exactly that.
# It never alters a value (a serve root containing spaces survives), it takes the FIRST occurrence, and
# an absent file, object or field yields empty with status 0 — the caller decides what an absence means.
setup_decl_field() {
  local file="$1" obj="$2" fld="$3" blob
  [ -r "$file" ] || return 0
  blob="$(tr '\n\r' '  ' < "$file" | grep -oE "\"$obj\"[[:space:]]*:[[:space:]]*\{[^{}]*\}" | head -n1)" || return 0
  [ -n "$blob" ] || return 0
  printf '%s' "$blob" | grep -oE "\"$fld\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" | head -n1 \
    | sed -E "s/^\"$fld\"[[:space:]]*:[[:space:]]*\"//; s/\"$//" || return 0
}

# setup_decl_edge_role <repo-dir> — the edge role the REPOSITORY declares (holistic-service.json,
# edge.role). No declaration means `service`, the unprivileged default. A word outside the closed list
# is a NAMED failure, never a fallback — the same rule the artifact stamp is read by.
setup_decl_edge_role() {  # <repo-dir>
  local dir="$1" role
  role="$(setup_decl_field "$dir/$SETUP_DECL_FILE" edge role)"
  [ -n "$role" ] || { printf 'service'; return 0; }
  if ! setup_valid_edge_role "$role"; then
    echo "$SETUP_DECL_FILE declares edge.role '$role', which is not one of: $SETUP_EDGE_ROLES" >&2
    return 1
  fi
  printf '%s' "$role"
}

# setup_decl_serve_root <repo-dir> <repo> — where this package's own face is served from on the target
# host: what the repository declares (edge.serveRoot), else the landscape's convention /opt/<repo>/www.
# A declared value must be ABSOLUTE; anything else is a named failure rather than a path the installer
# would have to make sense of. This REPLACES the name comparison that used to decide the same thing
# (`if [ "$repo_name" = devlab ]` in devlab-exec) — a package says where its face belongs, and the
# installer re-validates that it lies in the package's own territory before copying (setup_valid_web_root).
setup_decl_serve_root() {  # <repo-dir> <repo>
  local dir="$1" repo="$2" root
  root="$(setup_decl_field "$dir/$SETUP_DECL_FILE" edge serveRoot)"
  if [ -z "$root" ]; then
    setup_service_www "$repo"
    return 0
  fi
  case "$root" in
    /*) : ;;
    *) echo "$SETUP_DECL_FILE declares edge.serveRoot '$root', which is not an absolute path — a face is installed at an absolute path on the target host" >&2; return 1 ;;
  esac
  printf '%s' "$root"
}

# setup_valid_web_root <root> <repo> — 0 when <root> is a serve root this service may own: an absolute
# path, free of relative and empty segments, strictly INSIDE the service's own /opt/<repo> or
# /var/lib/<repo> tree (never the tree itself, never another service's, never a system directory).
# Prints the reason to stderr and returns 1 otherwise.
setup_valid_web_root() {  # <root> <repo>
  local root="$1" repo="$2"
  [ -n "$root" ] || { echo "no serve root declared" >&2; return 1; }
  case "$root" in
    /*) : ;;
    *) echo "declared serve root '$root' is not absolute — a face is installed at an absolute path on the target host" >&2; return 1 ;;
  esac
  case "$root" in
    *//* | */. | */./* | */.. | */../*)
      echo "declared serve root '$root' contains an empty or relative segment — refusing to resolve it" >&2; return 1 ;;
  esac
  case "$root" in
    "$SETUP_SERVICE_ROOT/$repo"/?* | "/var/lib/$repo"/?*) return 0 ;;
  esac
  echo "declared serve root '$root' lies outside the territory of service '$repo' (expected below $SETUP_SERVICE_ROOT/$repo or /var/lib/$repo) — a delivery installs its face inside itself, never elsewhere on the host" >&2
  return 1
}

# setup_install_web <artifact-dir> <repo> [<plan-only>] — install the delivered face. THE ONE path both
# the dev installer and the production receiver take, so a face arrives on a workbench and on a
# production host by the identical rule and neither can grow a second one.
#
# IT DOES NOT STATE SUCCESS. The verdict belongs to the END of the run and is spoken there
# (setup_confirm_web): a face that is copied here and gone by the time the run finishes was not
# installed, whatever this step saw. What this step does state is a REFUSAL, at the point it refuses:
#   MERCURY-WEB: failed                with the named reason on stderr; the function returns 1 and the
#                                      caller fails the install with ITS OWN exit code
# The three faults it refuses are all incomplete packages, never silent skips: a face without a
# declaration, a declaration without a face, and a declaration outside the service's own territory. A
# service that ships a face which never reaches its host is an INCOMPLETE DELIVERY — the very state this
# reporting exists to end — so it is never a warning beside a green result. It refuses BEFORE it copies,
# and it is called BEFORE the program half, so an incomplete package changes as little as possible on the
# host before it fails. That ordering is about failing early — it is NOT what keeps the face alive; that
# is the program copy's own boundary (setup_install_program), which holds whenever and wherever it runs.
#
# After the copy the tree is made readable BY ITS PUBLIC ROLE and that readability is PROVEN
# (setup_serve_root_readable / setup_serve_root_check): a face the webserver cannot read is a 404 over a
# present page, which is not a delivery either. <plan-only>=1 judges everything and copies nothing (the
# --check dry run), so a plan can never read clean where the real run would fail.
setup_install_web() {  # <artifact-dir> <repo> [<plan-only>]
  local art="$1" repo="$2" plan="${3:-0}" root have_web=0 reason
  [ -d "$art/web" ] && have_web=1
  root="$(setup_read_web_root "$art")"
  if [ "$have_web" = 0 ] && [ -z "$root" ]; then
    return 0
  fi
  if [ -z "$root" ]; then
    echo "MERCURY-WEB: failed"
    echo "the delivery of '$repo' carries a web face (web/) but declares no serve root (no web.root in the package) — where a face belongs is said by the delivery, never derived from the repository name; refusing to guess a destination" >&2
    return 1
  fi
  if [ "$have_web" = 0 ]; then
    echo "MERCURY-WEB: failed"
    echo "the delivery of '$repo' declares the serve root '$root' but carries no web/ — the face was announced and did not travel; that is an incomplete delivery, not a service without a face" >&2
    return 1
  fi
  if ! reason="$(setup_valid_web_root "$root" "$repo" 2>&1)"; then
    echo "MERCURY-WEB: failed"; echo "$reason" >&2; return 1
  fi
  if [ "$plan" = 1 ]; then
    echo "PLAN: install the delivered face $art/web -> the serve root the package declares ($root), make it readable by its public role, and PROVE the webserver can read its start page — else fail the delivery BENANNT"
    return 0
  fi
  if ! mkdir -p -- "$root"; then
    echo "MERCURY-WEB: failed"; echo "could not create the serve root '$root' for '$repo'" >&2; return 1
  fi
  # WHAT IS BEING REPLACED IS SAID OUT LOUD. The copy is a `--delete` copy: whatever stood at this serve
  # root is gone afterwards. On a workbench that ALSO builds the shared dashboard from an operator
  # checkout, the root application's serve root has two writers — this delivery and that build — and the
  # last one wins. That is a real interaction, so it is named in the log of every run that overwrites an
  # existing tree, rather than left for somebody to discover as a face that silently changed back.
  if [ -f "$root/index.html" ]; then
    echo "note: the serve root '$root' already carries a start page (last written $(date -r "$root/index.html" '+%Y-%m-%d %H:%M:%S' 2>/dev/null || echo 'unknown')); the delivered face of '$repo' replaces it" >&2
  fi
  if ! rsync -a --delete "$art/web"/ "$root"/; then
    echo "MERCURY-WEB: failed"; echo "could not copy the delivered face of '$repo' into '$root'" >&2; return 1
  fi
  setup_serve_root_readable "$root"
  if ! reason="$(setup_serve_root_check "$root" 2>&1)"; then
    echo "MERCURY-WEB: failed"
    echo "the delivered face of '$repo' did not become servable at '$root': $reason" >&2
    return 1
  fi
}

# setup_confirm_web <artifact-dir> <repo> [<plan-only>] — THE VERDICT ON THE FACE, spoken at the END of
# the run. This is the ONE line a reader (and the delivery chain) takes the outcome from:
#   MERCURY-WEB: none                  the package ships no face and declares none
#   MERCURY-WEB: installed | <root>    the face is on this host AT THE END of the run, proven readable
#   MERCURY-WEB: planned   | <root>    <plan-only>=1: everything judged, nothing copied
#   MERCURY-WEB: failed                (from setup_install_web, at the point it refuses the package)
# Exactly one such line is written per run: either the refusal where the package was refused, or this
# verdict at the end.
#
# WHY IT IS SPOKEN HERE AND NOT AT THE COPY. "Installed" used to be reported the moment the bytes were
# copied — and the very next step of the same run deleted them again: the program half of a python-app
# copied its payload over the whole service directory with `rsync --delete`, which removed the face that
# had just been laid beside it. The host then had no start page, the instance root answered 503, and the
# delivery was booked as a success (measured on production 2026-08-09: `MERCURY-WEB: installed |
# /opt/holistic/www` while /opt/holistic held nothing but pybase and venv). A verdict about a moment is
# not a verdict about the delivery. What is not there when the run ends was not installed — so the same
# proof the copy already ran (setup_serve_root_check: the start page exists AND an unprivileged reader
# can read it) is REPEATED here, over the END state, and only that repetition may say "installed".
#
# It re-reads the package's own declaration rather than being handed a path, so it judges the same fact
# by the same rule as the copy did and cannot be told a different serve root than the one that was used.
setup_confirm_web() {  # <artifact-dir> <repo> [<plan-only>]
  local art="$1" repo="$2" plan="${3:-0}" root have_web=0 reason
  [ -d "$art/web" ] && have_web=1
  root="$(setup_read_web_root "$art")"
  if [ "$have_web" = 0 ] && [ -z "$root" ]; then
    echo "MERCURY-WEB: none"; return 0
  fi
  if [ "$plan" = 1 ]; then
    echo "MERCURY-WEB: planned | $root"; return 0
  fi
  if ! reason="$(setup_serve_root_check "$root" 2>&1)"; then
    echo "MERCURY-WEB: failed"
    echo "the delivered face of '$repo' is NOT at its serve root '$root' now that the run has finished: $reason" >&2
    echo "it was installed earlier in this run and did not survive it — something else in the same run removed it. A face that is gone when the run ends was not installed, so this delivery is incomplete and fails." >&2
    return 1
  fi
  echo "MERCURY-WEB: installed | $root"
}

# ── THE PROGRAM: A COPY THAT CLEANS UP AFTER ITSELF, AND AFTER NOBODY ELSE ────────────────────────
# A service directory (/opt/<repo>) is not the program's private property: the program lives in it, and
# so does the service's own face, at the serve root the package declares (/opt/<repo>/www by the serve
# convention). The python-app program install copied its payload there with `rsync -a --delete`, which
# removes everything on the receiving side that the payload does not carry — and the payload carries no
# face. So the delivery installed the interface and deleted it again in the same run, and reported
# success both times (measured on production 2026-08-09).
#
# THE RULE, and it is ONE rule for every build kind: the program's copy touches only what belongs to the
# PROGRAM. What another half of the same service owns inside that directory is kept out of the copy
# entirely — not deleted from and not written into — by name, derived from that half's own declaration
# and never from a fixed list of directory names. The delete itself is NOT weakened: everything the
# previous payload left and the new one no longer carries still goes, so a shrinking program still
# cleans up after itself.
#
# The alternative — installing the program first and the face after it — was rejected: it leaves the
# boundary violated and merely arranges for the damage to be repaired afterwards, so any run, any host
# and any future caller that installs only the program destroys the face again. A boundary that must be
# remembered at every call site is a boundary that will be crossed again.

# setup_program_keepouts <artifact-dir> <repo> <dest> — the paths INSIDE the program's destination that
# belong to ANOTHER half of the same service, one per line, relative to <dest>. Today exactly one half
# can live there: the service's own face, at the serve root its package declares. A declaration that is
# not admissible is not honoured here either — the face half refuses the package by name before the
# program is ever copied (setup_install_web), and a keepout derived from a rejected declaration would
# quietly let a package name a directory it may not own.
setup_program_keepouts() {  # <artifact-dir> <repo> <dest>
  local art="$1" repo="$2" dest="$3" root rel
  root="$(setup_read_web_root "$art")"
  [ -n "$root" ] || return 0
  setup_valid_web_root "$root" "$repo" >/dev/null 2>&1 || return 0
  case "$root/" in
    "$dest"/*) rel="${root#"$dest"/}"; [ -n "$rel" ] && printf '%s\n' "$rel" ;;
  esac
  return 0
}

# setup_install_program <artifact-dir> <repo> <kind> [<plan-only>] — install the prebuilt program per its
# DECLARED build kind. THE ONE program install both the dev installer and the production receiver take,
# so the two hosts cannot drift on what a program install may remove. It is a pure copy of already-built
# bytes; neither host ever builds. <plan-only>=1 states the plan and copies nothing.
#
#   go-daemon   the single prebuilt <repo>d, installed into the program's own directory /opt/<repo>/bin.
#   python-app  the prebuilt payload tree (a --copies virtualenv + the bundled interpreter + the app),
#               copied verbatim to /opt/<repo> — the prefix the venv's shebangs and the delivered unit's
#               ExecStart were built against, which is why the payload is not moved into a subdirectory
#               of its own: that path is named in the SERVICE's repository, not in this one.
#               --safe-links drops any symlink escaping the payload, so a crafted link cannot make root
#               expose a foreign file under the service tree; --delete removes what a previous payload
#               left behind, BOUNDED by the keepouts above; a+rX then makes the tree readable and
#               traversable for the unit's own service account (User=<repo>, not root), which did not
#               build it — the same public-by-role rule the serve root follows.
setup_install_program() {  # <artifact-dir> <repo> <kind> [<plan-only>]
  local art="$1" repo="$2" kind="$3" plan="${4:-0}" dest bin payload rel keep=() kept=""
  dest="$SETUP_SERVICE_ROOT/$repo"
  # Ownership: root owns what it installs. The DIRECT-INVOCATION test seam drops the flags so a whole
  # install can be driven unprivileged against a fixture host (sudo's env_reset and sshd's forced
  # command both strip it, so it can never be set in operation).
  local own=(-o root -g root); [ "${DEVLAB_RECV_TEST:-0}" = 1 ] && own=()
  while IFS= read -r rel; do
    [ -n "$rel" ] || continue
    # The program's copy LEAVES THAT DIRECTORY ALONE, in both directions: rsync neither deletes what
    # stands there (an excluded path is exempt from --delete — that is what saves the face) nor writes
    # anything of its own into it (a payload that happened to carry files at that path would otherwise
    # overwrite the face right after it was installed, which is the same fault from the other side).
    keep+=("--filter=- /$rel/***")
    kept="${kept:+$kept, }$dest/$rel"
  done < <(setup_program_keepouts "$art" "$repo" "$dest")
  case "$kind" in
    go-daemon)
      bin="$art/${repo}d"
      if [ "$plan" = 1 ]; then
        echo "PLAN: install binary $bin -> $dest/bin/${repo}d (the program's own directory; nothing outside it is removed)"
        return 0
      fi
      install -d "${own[@]}" -m 0755 "$dest/bin"
      install "${own[@]}" -m 0755 "$bin" "$dest/bin/${repo}d"
      ;;
    python-app)
      payload="$art/payload"
      if [ "$plan" = 1 ]; then
        echo "PLAN: copy prebuilt payload $payload/ -> $dest/ (rsync --delete --safe-links + chmod a+rX; this host never builds)${kept:+ — the delete stops at what another half of '$repo' owns: $kept}"
        return 0
      fi
      install -d "${own[@]}" -m 0755 "$dest"
      rsync -a --delete --safe-links ${keep[@]+"${keep[@]}"} "$payload/" "$dest/" || return 1
      chmod -R a+rX "$dest"
      ;;
    *)
      echo "setup_install_program: unknown build kind '$kind' (known: $SETUP_BUILD_KINDS)" >&2
      return 1
      ;;
  esac
}
