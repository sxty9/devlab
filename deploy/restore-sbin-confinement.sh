#!/usr/bin/env bash
# restore-sbin-confinement.sh — take the devlab service back OUT of /usr/local/sbin, once the durable
# path exists that no longer needs it.
#
# WHY THIS SCRIPT EXISTS. While the approved renewal of a root wrapper could not write /usr/local/sbin
# (the service runs ProtectSystem=strict, which mounts that directory read-only for it), an approved
# renewal ran into a loop the user could not win. The stop-gap was to add
#     [Service]
#     ReadWritePaths=/usr/local/sbin
# as a drop-in — which works, but gives the service standing write to its OWN root wrappers, i.e. the
# ability to grant itself root without a human's approval. The durable fix hands the ONE approved
# renewal OUT of the service's cgroup into a transient systemd unit (deploy/devlab-install,
# maybe_hand_out_renewal) — the same escape the self-repo restart uses — so the service can stay locked
# out of /usr/local/sbin. Once that fix is the INSTALLED wrapper, the drop-in is no longer needed and
# leaving it in place only weakens the boundary. This script removes it and proves the lock is back.
#
# It is SAFE by refusing to act too early: it will not remove the drop-in until the installed
# /usr/local/sbin/devlab-install actually carries the hand-out. Removing it while the old wrapper is
# still installed would re-open the loop on the next renewal. It is instance-neutral — it discovers the
# drop-in by its EFFECT (a ReadWritePaths line naming /usr/local/sbin), never by a hand-picked filename.
#
# One pass, self-checking, halts on the first failure, and the rollback is the backup it takes first.
#   sudo /usr/local/sbin/restore-sbin-confinement.sh          # or: sudo bash deploy/restore-sbin-confinement.sh
set -euo pipefail

UNIT="${DEVLAB_UNIT:-devlabd}"
DROPIN_DIR="${DEVLAB_DROPIN_DIR:-/etc/systemd/system/${UNIT}.service.d}"
INSTALLED_WRAPPER="${DEVLAB_INSTALLED_WRAPPER:-/usr/local/sbin/devlab-install}"
SYSTEMCTL="${DEVLAB_SYSTEMCTL:-systemctl}"
BACKUP_DIR="${DEVLAB_DROPIN_BACKUP_DIR:-/var/lib/devlab-wrapper-audit/sbin-dropin-backups}"

say()  { printf '%s\n' "restore-sbin-confinement: $*"; }
die()  { printf 'restore-sbin-confinement: error: %s\n' "$1" >&2; exit "${2:-1}"; }

[ "$(id -u)" -eq 0 ] || die "must run as root (it edits a systemd drop-in and restarts $UNIT)" 2

# 1) The durable path must already be the INSTALLED wrapper, or removing the drop-in re-opens the loop.
[ -r "$INSTALLED_WRAPPER" ] || die "installed wrapper $INSTALLED_WRAPPER not found — cannot confirm the durable hand-out is in place" 2
if ! grep -q 'maybe_hand_out_renewal' "$INSTALLED_WRAPPER"; then
  die "installed $INSTALLED_WRAPPER does NOT carry the hand-out (maybe_hand_out_renewal) yet — refusing to restore confinement while an approved renewal would still fail; deliver the fix first" 3
fi
say "durable hand-out present in $INSTALLED_WRAPPER — safe to restore confinement"

# 2) Find every drop-in that grants the service write to /usr/local/sbin. Discovered by EFFECT, not by a
#    fixed name, so this works whatever the stop-gap file was called.
mapfile -t OFFENDERS < <(grep -rlE '^[[:space:]]*ReadWritePaths=.*(/usr/local/sbin)([[:space:]]|$)' "$DROPIN_DIR" 2>/dev/null || true)
if [ "${#OFFENDERS[@]}" -eq 0 ]; then
  say "no drop-in grants $UNIT write to /usr/local/sbin — nothing to remove"
else
  install -d -m 0700 -- "$BACKUP_DIR"
  ts="$(date -u +%Y%m%dT%H%M%SZ)"
  for f in "${OFFENDERS[@]}"; do
    b="$BACKUP_DIR/$(basename -- "$f").${ts}.bak"
    cp -p -- "$f" "$b"
    say "backed up $f -> $b (rollback: cp '$b' '$f' && $SYSTEMCTL daemon-reload && $SYSTEMCTL restart $UNIT)"
    rm -f -- "$f"
    say "removed $f"
  done
fi

# 3) Reload + restart so the sandbox is re-evaluated (ReadWritePaths only takes effect at unit start).
"$SYSTEMCTL" daemon-reload || die "daemon-reload failed" 4
"$SYSTEMCTL" restart "$UNIT" || die "restart of $UNIT failed — restore a backup from $BACKUP_DIR and reload" 4

# 4) Prove the lock is back: /usr/local/sbin must NOT be in the effective ReadWritePaths any more.
eff="$("$SYSTEMCTL" show "$UNIT" -p ReadWritePaths --value 2>/dev/null || true)"
case " $eff " in
  *" /usr/local/sbin "*|*"/usr/local/sbin/"*)
    die "after restart $UNIT STILL has /usr/local/sbin writable ($eff) — another drop-in or the unit itself grants it; confinement NOT restored" 5 ;;
esac
say "confinement restored: $UNIT no longer has /usr/local/sbin writable (effective ReadWritePaths: ${eff:-<none>})"
say "done — an approved wrapper renewal now takes effect via the hand-out, and the service can no longer write its own root scripts unattended."
