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

# setup_self_route_text <port> — THE edge route template for the SELF service (devlab). devlab is the
# one landscape member whose UI is the dashboard served at the site root and whose backend answers the
# WHOLE `/api/*` prefix (not `/api/services/<repo>/*` like every uniform service — the standalone
# DevLab app calls /api/user, /api/repos, …). So its route is a naked `handle /api/*` block, dropped
# into the SAME shared route directory and imported into the edge's site block exactly like a uniform
# route; a more specific `/api/services/<other>/*` block still wins for another service, so this
# catch-all coexists with them. This is a NAMED layout exception for the self repo, not a second route
# system: it reuses the identical container (setup_edge_caddyfile_text) and install path
# (setup_install_route) — only the path matcher differs, because devlab's contribution genuinely does.
setup_self_route_text() {
  local port="$1"
  cat <<ROUTE
# Generated by devlab-install. Routes the whole DevLab API (/api/*) to the devlab daemon.
handle /api/* {
	reverse_proxy 127.0.0.1:${port}
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

# setup_edge_caddyfile_text <conf_dir> <www_dir> <site> — THE host edge shell: the ONE site block the
# per-service routes REQUIRE. Every route from setup_route_text is a naked `handle` block that is valid
# only inside a site block, so the shell imports the whole route directory INSIDE one and adds a static
# fallback for the dashboard. This is the SAME shape a grown holistic host carries (a site block whose
# body imports conf.d and ends in a file_server), reduced to its instance-neutral core: the conf.d and
# web paths supplied by the caller, and — decisively — the SITE ADDRESS supplied by the caller too, from
# the ONE declaration (setup_edge_address). There is NO baked-in default: an address invented here is
# exactly the guess that makes the edge answer somewhere the routing layer does not forward. It lives
# HERE, beside the route template, so the container and the thing it must contain can never drift into
# two incompatible descriptions of the same edge.
setup_edge_caddyfile_text() {
  local conf_dir="$1" www_dir="$2" site="${3:-}"
  if ! setup_valid_edge_address "$site"; then
    echo "setup_edge_caddyfile_text: refusing to build an edge without a declared site address (got '${site}') — the edge address is read from the ONE declaration (setup_edge_address), never guessed" >&2
    return 1
  fi
  # The edge is the PLAIN-HTTP face behind the routing layer (sxgate), which owns hostnames and TLS. So
  # the declared socket is rendered as an explicit `http://` site: without a scheme, Caddy treats a
  # host-bearing address (10.10.0.1:8080) as needing automatic HTTPS and tries to bind :80 for the
  # HTTP→HTTPS redirect, which is both wrong here and fails on a host that does not own :80. The stored
  # declaration stays the bare socket (what the routing layer forwards to); only the Caddy label carries
  # the scheme. An address that already names a scheme is left as the operator wrote it.
  local label="$site"
  case "$site" in *://*) : ;; *) label="http://$site" ;; esac
  cat <<EDGE
# Managed by devlab-install-recv — the Holistic edge shell. The per-service routes the receiver drops
# into ${conf_dir} are naked \`handle\` blocks; a naked directive is valid ONLY inside a site block, so
# they are imported INSIDE one here. Never add a bare directive or a second site block beside this one:
# Caddy refuses that as an ambiguous site definition and then NO delivered route validates. Regenerated
# on --provision; do not edit by hand.
${label} {
	import ${conf_dir}/*.caddy
	handle {
		root * ${www_dir}
		file_server
	}
}
EDGE
}

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
  local extra=("$@") validate_out
  install -o root -g root -m 0644 "$deliv" "$route_file"
  if command -v "$bin" >/dev/null 2>&1 && [ -f "$main" ]; then
    if ! validate_out="$("$bin" validate --config "$main" 2>&1)"; then
      rm -f "$route_file" "${extra[@]}"
      echo "the edge configuration does not validate with the delivered route — route and unit removed, edge untouched: $(printf '%s' "$validate_out" | tail -n 3)" >&2
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
