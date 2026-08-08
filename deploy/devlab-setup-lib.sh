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
#   python-app  — a prebuilt, relocatable payload tree (a --copies virtualenv plus the app) that the
#                 installer copies VERBATIM to /opt/<repo>; its unit's ExecStart runs an interpreter out
#                 of that tree (…/venv/bin/…). It carries no <repo>d and never will. Because only the
#                 unit knows how to start it, a python-app MUST ship its own setup/<unit>.service — the
#                 unit is the source of truth, not a name convention.
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
# established exit-code contract while sharing the one rule-set. For a name that is reserved BUT already
# belongs to the service being delivered (a renewal), the caller uses setup_owns_reserved_name to lift the
# refusal — this function alone always refuses a reserved name, because on its own it cannot see the host.
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

# setup_edge_caddyfile_text <conf_dir> <www_dir> [<site>] — THE host edge shell: the ONE site block
# the per-service routes REQUIRE. Every route from setup_route_text is a naked `handle` block that is
# valid only inside a site block, so the shell imports the whole route directory INSIDE one and adds a
# static fallback for the dashboard. This is the SAME shape a grown holistic host carries (a site block
# whose body imports conf.d and ends in a file_server), reduced to its instance-neutral core: no
# hostname (the site is a bare port so no domain is baked in — Keine Instanz-Spezifika), the conf.d and
# web paths supplied by the caller. It lives HERE, beside the route template, so the container and the
# thing it must contain can never drift into two incompatible descriptions of the same edge.
setup_edge_caddyfile_text() {
  local conf_dir="$1" www_dir="$2" site="${3:-:80}"
  cat <<EDGE
# Managed by devlab-install-recv — the Holistic edge shell. The per-service routes the receiver drops
# into ${conf_dir} are naked \`handle\` blocks; a naked directive is valid ONLY inside a site block, so
# they are imported INSIDE one here. Never add a bare directive or a second site block beside this one:
# Caddy refuses that as an ambiguous site definition and then NO delivered route validates. Regenerated
# on --provision; do not edit by hand.
${site} {
	import ${conf_dir}/*.caddy
	handle {
		root * ${www_dir}
		file_server
	}
}
EDGE
}

# setup_ensure_account <repo> — the service's own identity: a nologin SYSTEM account whose home is
# /var/lib/<repo>, created only when absent (idempotent). This is what `User=<repo>` in the unit runs
# as; devlab-install and devlab-deploy-recv create it identically at first-time setup.
setup_ensure_account() {
  local repo="$1"
  getent passwd "$repo" >/dev/null 2>&1 \
    || useradd --system --shell /usr/sbin/nologin --home-dir "/var/lib/$repo" "$repo"
  install -d -o "$repo" -g "$repo" -m 0755 "/var/lib/$repo"
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

# setup_unit_listen_port <unit-file> — the loopback port the unit's ExecStart binds
# (--listen 127.0.0.1:<port>), or empty. The honest running gate dials this port to prove the service
# STAYS up; a unit whose port cannot be read is proven by unit-activity alone.
setup_unit_listen_port() {
  local file="$1"
  [ -r "$file" ] || return 0
  grep -oE '127\.0\.0\.1:[0-9]+' -- "$file" 2>/dev/null | grep -oE '[0-9]+$' | head -n1 || true
}
