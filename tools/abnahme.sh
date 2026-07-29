#!/usr/bin/env bash
# tools/abnahme.sh — the executable half of the acceptance matrix (Baustein Q).
#
# Every "Code-Suche" cell of the acceptance matrix is ONE NAMED CHECK here, labelled with its
# matrix id (K-1 … B-45), so a failure names the exact line that is not passed. Checks prove the
# ABSENCE of a retired construct, the UNIQUENESS of a single path, or the PRESENCE of a required
# marker. The list-shaped criteria (route/caller reconciliation, MCP parity, vocabulary,
# symmetry) are automated TESTS instead — they live in backend/it and src/*.test.ts and run with
# --tests.
#
# Design notes
#   * The file set comes from git (tracked + untracked, .gitignore respected), so a file a
#     Baustein added minutes ago is audited and node_modules/dist never are.
#   * Code checks IGNORE comment-only lines: the matrix asks whether a construct EXISTS, and
#     prose that documents its removal ("there is no rollout") must not read as a violation.
#     Checks that must see prose too use the *_raw variants.
#   * deploy/migration/ is excluded from code checks by design: those documents describe the
#     cutover and therefore NAME the retired artefacts they remove.
#   * This script carries no instance literals of its own — the "no instance domain" check works
#     off an allow-list of generic hosts, never off a concrete one.
#
# Usage: tools/abnahme.sh [--tests] [--verbose]
#   --tests    additionally run the acceptance TESTS (go test ./it/..., node --test)
#   --verbose  print every matching line of a failed check (default: up to 10)

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 2

WITH_TESTS=0
VERBOSE=0
for arg in "$@"; do
  case "$arg" in
    --tests) WITH_TESTS=1 ;;
    --verbose) VERBOSE=1 ;;
    -h | --help)
      sed -n '2,25p' "${BASH_SOURCE[0]}"
      exit 0
      ;;
    *)
      echo "abnahme: unknown argument: $arg" >&2
      exit 2
      ;;
  esac
done

if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "abnahme: not a git work tree — the audit derives its file set from git" >&2
  exit 2
fi

# ── file sets ────────────────────────────────────────────────────────────────────────────────

# Tracked + untracked-but-not-ignored, minus binaries, generated lock files — and minus THIS
# script, whose patterns would otherwise match themselves.
mapfile -t ALL_FILES < <(
  git ls-files --cached --others --exclude-standard |
    grep -vE '\.(woff2|png|jpe?g|gif|ico|pdf|zip|gz)$' |
    grep -vE '^(package-lock\.json|backend/go\.sum|tools/abnahme\.sh)$' |
    sort -u
)

# sel <path-regex> [<exclude-regex>] — the subset of ALL_FILES matching (and not excluding).
sel() {
  local include="$1" exclude="${2:-}"
  local f
  for f in "${ALL_FILES[@]}"; do
    [[ $f =~ $include ]] || continue
    [[ -n $exclude && $f =~ $exclude ]] && continue
    printf '%s\n' "$f"
  done
}

MIGRATION='^deploy/migration/'
mapfile -t F_ALL < <(sel '.' "$MIGRATION")
mapfile -t F_CODE < <(sel '.' "(\.md$)|$MIGRATION|^contract/fixtures/|^\.sxgate/")
mapfile -t F_GO < <(sel '^backend/.*\.go$')
mapfile -t F_GO_SRC < <(sel '^backend/.*\.go$' '(_test\.go$)|/testdata/')
# The Go files that make up the SHIPPED daemon: the integration suite may import a package without
# the daemon wiring it, which is precisely what the orphan check must still see.
mapfile -t F_GO_WIRED < <(sel '^backend/.*\.go$' '^backend/it/')
mapfile -t F_TS < <(sel '^src/.*\.tsx?$')
mapfile -t F_TS_SRC < <(sel '^src/.*\.tsx?$' '\.test\.tsx?$')
mapfile -t F_SH < <(sel '^(deploy/|service$|tools/)' '(\.md$)|(\.conf$)|(\.json$)')
# Implementation code only. An acceptance TEST names forbidden constructs on purpose (to prove
# their absence), so an absence grep that read the tests would report the proof as the offence.
mapfile -t F_IMPL < <(sel '.' '(\.md$)|'"$MIGRATION"'|^contract/|^\.sxgate/|(_test\.go$)|(\.test\.tsx?$)|^backend/it/|/testdata/')

# ── check plumbing ───────────────────────────────────────────────────────────────────────────

PASS=0
FAILED=()
NOTES=()

green() { printf '\033[32m%s\033[0m' "$1"; }
red() { printf '\033[31m%s\033[0m' "$1"; }
dim() { printf '\033[2m%s\033[0m' "$1"; }
if [[ ! -t 1 ]]; then
  green() { printf '%s' "$1"; }
  red() { printf '%s' "$1"; }
  dim() { printf '%s' "$1"; }
fi

pass() {
  PASS=$((PASS + 1))
  printf '%s  %-9s %s\n' "$(green PASS)" "$1" "$2"
}

fail() {
  FAILED+=("$1 — $2")
  printf '%s  %-9s %s\n' "$(red FAIL)" "$1" "$2"
  if [[ -n ${3:-} ]]; then
    local limit=10
    [[ $VERBOSE -eq 1 ]] && limit=1000
    while IFS= read -r line; do printf '        %s\n' "$(dim "$line")"; done <<<"$(head -n "$limit" <<<"$3")"
  fi
}

# hits <pattern> <file...> — matching "path:line:text", comment-only lines removed.
hits() {
  local pattern="$1"
  shift
  [[ $# -eq 0 ]] && return 0
  grep -nIE -- "$pattern" "$@" 2>/dev/null |
    grep -vE ':[0-9]+:[[:space:]]*(//|#|\*|<!--|--|;)' || true
}

# hits_raw <pattern> <file...> — matching lines INCLUDING comments.
hits_raw() {
  local pattern="$1"
  shift
  [[ $# -eq 0 ]] && return 0
  grep -nIE -- "$pattern" "$@" 2>/dev/null || true
}

# absent <id> <description> <pattern> <file...>
absent() {
  local id="$1" desc="$2" pattern="$3"
  shift 3
  local out
  out="$(hits "$pattern" "$@")"
  if [[ -z $out ]]; then pass "$id" "$desc"; else fail "$id" "$desc" "$out"; fi
}

# absent_raw <id> <description> <pattern> <file...> — comments count as violations too.
absent_raw() {
  local id="$1" desc="$2" pattern="$3"
  shift 3
  local out
  out="$(hits_raw "$pattern" "$@")"
  if [[ -z $out ]]; then pass "$id" "$desc"; else fail "$id" "$desc" "$out"; fi
}

# present <id> <description> <pattern> <file...>
present() {
  local id="$1" desc="$2" pattern="$3"
  shift 3
  local out
  out="$(hits_raw "$pattern" "$@")"
  if [[ -n $out ]]; then pass "$id" "$desc"; else fail "$id" "$desc" "expected at least one occurrence of /$pattern/"; fi
}

# unique_in <id> <description> <pattern> <expected-path-regex> <file...>
# Passes when every match sits in a file matching <expected-path-regex> and there is ≥ 1.
unique_in() {
  local id="$1" desc="$2" pattern="$3" allowed="$4"
  shift 4
  local out stray
  out="$(hits "$pattern" "$@")"
  if [[ -z $out ]]; then
    fail "$id" "$desc" "expected /$pattern/ in $allowed, found nothing at all"
    return
  fi
  stray="$(awk -F: -v ok="$allowed" '$1 !~ ok' <<<"$out")"
  if [[ -z $stray ]]; then pass "$id" "$desc"; else fail "$id" "$desc" "$stray"; fi
}

# count_files <id> <description> <expected-count> <pattern> <file...>
count_files() {
  local id="$1" desc="$2" want="$3" pattern="$4"
  shift 4
  local out got
  out="$(hits "$pattern" "$@")"
  got="$(cut -d: -f1 <<<"$out" | sort -u | grep -c . || true)"
  if [[ "$got" == "$want" ]]; then
    pass "$id" "$desc"
  else
    fail "$id" "$desc (expected $want file(s), found $got)" "$out"
  fi
}

# no_file <id> <description> <path...>
no_file() {
  local id="$1" desc="$2"
  shift 2
  local found=() p
  for p in "$@"; do [[ -e $p ]] && found+=("$p"); done
  if [[ ${#found[@]} -eq 0 ]]; then pass "$id" "$desc"; else fail "$id" "$desc" "$(printf '%s exists\n' "${found[@]}")"; fi
}

# note <id> <description> <pattern> <file...> — informational, never a gate.
note() {
  local id="$1" desc="$2" pattern="$3"
  shift 3
  local out
  out="$(hits_raw "$pattern" "$@")"
  [[ -z $out ]] && return 0
  NOTES+=("$id — $desc")
  while IFS= read -r line; do NOTES+=("        $line"); done <<<"$(head -n 20 <<<"$out")"
}

section() { printf '\n-- %s %s\n' "$1" "$(printf '%.0s-' $(seq 1 $((70 - ${#1}))))"; }

# ── §1 the six construction faults ───────────────────────────────────────────────────────────

section "§1  construction faults K-1 … K-6"

absent "K-1a" "no force-checkout of the workbench branch (work is folded in, never re-pointed)" \
  'checkout[[:space:]]+(-[a-zA-Z]+[[:space:]]+)*-B' "${F_GO_SRC[@]}" "${F_SH[@]}"

absent "K-1b" "no reset --hard onto origin anywhere in the working-state path" \
  'reset[[:space:]].*--hard.*(origin|remote)|--hard[[:space:]]+origin' "${F_GO_SRC[@]}" "${F_SH[@]}"

absent "K-2" "no inline restart in the deploy path (handover only, via the root wrapper)" \
  'systemd-run|systemctl[[:space:]]+(restart|start)' "${F_GO_SRC[@]}"

unique_in "K-6a" "pull-request creation has exactly ONE caller" \
  'github\.CreatePullRequest\(' '^backend/internal/deliver/github\.go$' \
  "${F_GO_SRC[@]}"

absent "K-6b" "no constitution-rollout code (no endpoint, no scheduler, no tracking pool)" \
  '[Rr]ollout[A-Za-z]*\(|rollout[A-Za-z]*[[:space:]]*(:?=|\{)|/rollout|handlers_mercury_rollout' \
  "${F_IMPL[@]}"

no_file "K-6c" "no rollout source files" \
  backend/internal/mercury/rollout.go backend/internal/api/handlers_mercury_rollout.go \
  backend/internal/api/rollout_auto.go

# ── §2 requirements ──────────────────────────────────────────────────────────────────────────

section "§2  requirements REQ-001 … REQ-044"

no_file "REQ-001a" "no legacy constitution data path in the repository" axioms

absent "REQ-001b" "no second constitution store (the axioms repository is the only one)" \
  'DEVLAB_MERCURY_AXIOMS|mercury/axioms\.json|axiomsFile' "${F_IMPL[@]}"

absent "REQ-002" "no rollout endpoints, schedules, tracking records or tests" \
  'rollout' "${F_TS_SRC[@]}"

unique_in "REQ-003a" "exactly ONE composition path writes a prompt snapshot" \
  'PromptSnapshot[[:space:]]*=[^=]' '^backend/internal/runs/plan\.go$' "${F_GO_SRC[@]}"

absent "REQ-003b" "no recompose badge or button (\"outdated\" is unreachable)" \
  '[Rr]ecompose|[Nn]eu komponieren|[Ss]tale[Pp]rompt' "${F_TS_SRC[@]}"

count_files "REQ-004" "exactly ONE planning path folds axioms into runs" \
  1 'func UpsertPlannedRun\(|func ComposeInto\(' "${F_GO_SRC[@]}"

count_files "REQ-010" "exactly ONE maintenance place for the run tunables (model/effort/budget)" \
  1 'EFFORT_LADDER|EFFORTS[[:space:]]*[:=]|effortLadder' "${F_TS_SRC[@]}"

present "REQ-012" "both calendars read through the ONE calendar access point" \
  'mercuryRunCalendar' src/views/mercury/calendar/MercuryCalendar.tsx src/views/GlobalCalendarView.tsx

absent "REQ-013" "no singular \"the active run\" assumption (active work is a list)" \
  '\b(activeRun|currentRun|theActiveRun|ActiveRun|activeExecutionID)\b' "${F_IMPL[@]}"

count_files "REQ-015a" "exactly ONE pause concept" \
  1 'type PauseReason' "${F_GO_SRC[@]}"

absent "REQ-015b" "no second, persisted pause mechanic beside the ONE pause" \
  'json:"suspended|json:"pausedUntil|pausedUntil[[:space:]]*[:=]|PausedUntil[[:space:]]+[A-Za-z*]' \
  "${F_IMPL[@]}"

absent "REQ-017" "no cost cap and no remaining-budget display anywhere" \
  '[Mm]axCost|costCap|CostCap|costLimit|CostLimit|budgetUSD|BudgetUSD|remainingCost|costRemaining' \
  "${F_IMPL[@]}"

present "REQ-022a" "the working branch is recorded as never becoming a pull request" \
  'never itself turned into a pull request' "${F_GO_SRC[@]}"

absent "REQ-022b" "the production path never ships the working branch" \
  'mercury-dev' backend/internal/deploy/prod.go deploy/devlab-deploy-recv

absent "REQ-027a" "no operating mode (no mode env, no mode branch)" \
  'RUNS_MODE|runsMode|RunsMode|\bmodePR\b|ModePR' "${F_IMPL[@]}"

absent "REQ-027b" "no dev-deploy switch, not even switched off" \
  'DEV_DEPLOY|devDeployEnabled|DevDeployEnabled|skipDeploy|SkipDeploy|deployEnabled' "${F_IMPL[@]}"

absent "REQ-027c" "\"skipped because of a setting\" is never produced anew" \
  'skipped because of a setting|übersprungen wegen' "${F_GO_SRC[@]}" "${F_TS_SRC[@]}"

absent "REQ-028" "no per-repository deploy script mechanism" \
  "deploy\.d[/\"' ]|deploy\.d\$" "${F_IMPL[@]}"

unique_in "REQ-034a" "exactly ONE live stream is opened" \
  'new EventSource' '^src/data/httpSource\.ts$' "${F_TS_SRC[@]}"

unique_in "REQ-034b" "no remaining polls — the only interval is the documented null-stream fallback" \
  'setInterval|setRepeat[[:space:]]*\?\?' '^src/lib/live\.ts$' "${F_TS_SRC[@]}"

unique_in "REQ-039a" "exactly ONE restart marker name, defined in one place" \
  'restart\.json' '^backend/internal/statepath/statepath\.go$' "${F_GO_SRC[@]}"

absent "REQ-039b" "the twin-marker trap of the old system does not exist" \
  'run-active|runs-active|runActiveMarker' "${F_IMPL[@]}"

unique_in "REQ-040a" "the data seam is the only place that names API paths" \
  "['\"\`]/api/" '^src/data/' "${F_TS_SRC[@]}"

absent "REQ-040b" "no second spelling of the wire vocabulary (one vocabulary everywhere)" \
  '"not_implemented"|"notApplicable"|"not_applicable"|"notExecuted"|"not_executed"|"NOT_APPLICABLE"|"implementedUndelivered"' \
  "${F_IMPL[@]}"

# One vocabulary spans UI, API, store, log AND report (REQ-040.4): a synonym for a term the wire
# contract already fixes is a violation wherever it appears, report items included.
absent "REQ-040f" "no synonym for the ONE pause / step vocabulary (paused, skipped, blocked)" \
  '\b(Suspended|suspended)\b|\bSkipped\b[[:space:]]+bool|json:"skipped' "${F_IMPL[@]}"

absent "REQ-042" "no mail dispatch path of its own (the mail service is the only one)" \
  'net/smtp|smtp\.(Dial|SendMail)|sendmail' "${F_GO_SRC[@]}"

absent "REQ-044" "no stored port state (ports are read from routes + bound sockets)" \
  '(^|[^a-z-])ports\.json|PortsFile|portStore|PortStore|savedPorts|portAllocationStore' "${F_IMPL[@]}"

# ── §3 inventory of the old system ───────────────────────────────────────────────────────────

section "§3  inventory B-01 … B-45"

absent "B-02" "no direct model-provider call anywhere in DevLab" \
  'api\.anthropic\.com|api\.openai\.com|generativelanguage|ANTHROPIC_API_KEY|OPENAI_API_KEY|x-api-key' \
  "${F_IMPL[@]}"

unique_in "B-11" "every file pool writes through the ONE atomic write path" \
  '[:+]?=[[:space:]]*[A-Za-z_.]+[[:space:]]*\+[[:space:]]*"\.tmp"|CreateTemp' \
  '^backend/internal/fsatomic/' "${F_GO_SRC[@]}"

absent "B-13" "no token ever reaches a log line" \
  'log\.[A-Za-z]+\([^)]*[Tt]oken|Printf\([^)]*[Tt]oken[^)]*\)' "${F_GO_SRC[@]}"

absent "B-16" "no decomposition remnant" \
  '[Dd]ecompose' "${F_IMPL[@]}"

absent "B-18" "no branch-preview routes and no preview wrapper" \
  '/api/preview|handlers_preview|devlab-preview|internal/preview|previewRoutes' "${F_IMPL[@]}"

absent "B-20" "no old scheduler and no activity marker pool" \
  'active_marker|activeMarker|ActiveMarker|strandedRuns|stranded_' "${F_IMPL[@]}"

count_files "B-21" "exactly ONE transliterating slugifier in the backend" \
  1 '"ä", *"ae"|"ä":[[:space:]]*"ae"' "${F_GO_SRC[@]}"

no_file "B-22" "the retired working-tree plumbing and its reset relatives are gone" \
  backend/internal/workspace/plumb.go backend/internal/workspace/plumb_test.go \
  backend/internal/workspace/mergeref_test.go backend/internal/workspace/devbranch_test.go \
  backend/internal/workspace/reset_test.go

absent "B-35" "the client derives no stages — it renders the server stage array" \
  'mercuryPipeline|isReportExecution|deriveStages|stageFromName|inferStage|guessStage' "${F_TS_SRC[@]}"

no_file "B-41a" "the dead helper modules are gone" src/lib/mercury.ts src/lib/axioms.ts

absent "B-43" "no token on a command line (env passthrough only)" \
  '\$DEVLAB_GH_TOKEN|--token|-t[[:space:]]+"?\$[A-Z_]*TOKEN' "${F_SH[@]}"

absent "B-44" "no PID busy marker and no idle-restart helper" \
  'restart-idle|pidfile|PIDFile|\.pid"' "${F_IMPL[@]}"

absent "B-45" "no machine- or user-specific paths in the repository" \
  '/home/[A-Za-z0-9._-]+/|/Users/[A-Za-z0-9._-]+/' "${F_ALL[@]}"

# ── cross-cutting audits (no single matrix cell, but named by several) ───────────────────────

section "cross-cutting audits"

# B-45 / universality: any absolute URL host outside this generic allow-list names a concrete
# instance. The allow-list holds only vendor-neutral hosts — this file names no instance itself.
# A host without a dot is a placeholder (loopback name, unix-socket stand-in), never an instance
# domain. Everything dotted must be a vendor-neutral or documentation host.
host_allow='^([^.]+|127\.0\.0\.1|github\.com|api\.github\.com|www\.w3\.org|([a-z0-9-]+\.)*example(\.(com|org|net|invalid))?|([a-z0-9-]+\.)*invalid)$'
stray_hosts="$(
  grep -ohIE 'https?://[A-Za-z0-9._~-]+' "${F_ALL[@]}" 2>/dev/null |
    sed -E 's#https?://##' | sort -u | grep -vE "$host_allow" || true
)"
if [[ -z $stray_hosts ]]; then
  pass "B-45b" "no concrete instance domain in the repository"
else
  fail "B-45b" "no concrete instance domain in the repository" "$stray_hosts"
fi

# REQ-007.2 / B-41: every upload point offers the clipboard as an equal input path.
upload_files="$(grep -lIE 'uploadVision\(|mercuryUploadAttachment\(' "${F_TS_SRC[@]}" 2>/dev/null | grep -v '^src/data/' || true)"
missing_paste=""
while IFS= read -r f; do
  [[ -z $f ]] && continue
  grep -qE 'usePasteFiles|onPaste|filesFromClipboard' "$f" || missing_paste+="$f has no clipboard path"$'\n'
done <<<"$upload_files"
if [[ -z $missing_paste ]]; then
  pass "B-41b" "every upload point accepts the clipboard as an equal input path"
else
  fail "B-41b" "every upload point accepts the clipboard as an equal input path" "$missing_paste"
fi

# REQ-040.5: no dead inventory — every module under src/ has at least one importer.
dead_modules=""
for f in "${F_TS_SRC[@]}"; do
  case "$f" in
    src/main.tsx | src/App.tsx | src/types.ts | src/styles.css) continue ;;
    src/theme/*) continue ;;
  esac
  base="$(basename "$f")"
  stem="${base%.tsx}"
  stem="${stem%.ts}"
  # An index module is imported by its DIRECTORY name ("@/data"), everything else by its stem.
  if [[ $stem == index ]]; then
    stem="$(basename "$(dirname "$f")")"
  fi
  if ! grep -qIE "from '[^']*[/']${stem}(\.tsx?)?'|import\('[^']*${stem}'" \
    "${F_TS[@]}" 2>/dev/null; then
    dead_modules+="$f has no importer"$'\n'
  fi
done
if [[ -z $dead_modules ]]; then
  pass "REQ-040c" "no dead module in the frontend inventory"
else
  fail "REQ-040c" "no dead module in the frontend inventory" "$dead_modules"
fi

# REQ-040.5: no dead inventory — every backend package is imported (cmd and the test-only
# integration package excepted).
dead_pkgs=""
while IFS= read -r pkgdir; do
  pkg="devlab/backend/${pkgdir#backend/}"
  case "$pkgdir" in
    backend/cmd/* | backend/it) continue ;;
  esac
  if ! grep -qIE "\"$pkg\"" "${F_GO_WIRED[@]}" 2>/dev/null; then
    dead_pkgs+="$pkgdir is imported by nobody"$'\n'
  fi
done < <(printf '%s\n' "${F_GO[@]}" | xargs -r -n1 dirname | sort -u)
if [[ -z $dead_pkgs ]]; then
  pass "REQ-040d" "no orphaned backend package"
else
  fail "REQ-040d" "no orphaned backend package" "$dead_pkgs"
fi

# B-03: every MUTATING route must be bound to a guard that enforces the CSRF double submit
# (guardWrite or guardCSRF). A mutating route on the plain read guard is reachable cross-site with
# the caller's cookies.
csrf_holes="$(
  grep -nE 'mux\.HandleFunc\("(POST|PUT|DELETE) [^"]+",[[:space:]]*s\.guard\(' \
    backend/internal/api/api.go 2>/dev/null || true
)"
if [[ -z $csrf_holes ]]; then
  pass "B-03" "every mutating route is bound to a CSRF-enforcing guard"
else
  fail "B-03" "every mutating route is bound to a CSRF-enforcing guard" "$csrf_holes"
fi

# K-2 / REQ-039.1 / REQ-013: the boot sequence is only real if the daemon CONSTRUCTS it. An
# unwired daemon makes every downstream criterion unreachable in production, so the calls the
# documented boot order needs are checked one by one.
boot_gaps=""
while IFS='|' read -r call why; do
  [[ -z $call ]] && continue
  grep -qF -- "$call" backend/cmd/devlabd/main.go 2>/dev/null ||
    boot_gaps+="cmd/devlabd/main.go never calls ${call} — ${why}"$'\n'
done <<'BOOT'
execstate.Open|boot step 2 opens the execution documents
MarkInterruptedAtBoot|boot step 3 exorcises ghosts before the first answer (REQ-039.1)
sched.New|boot steps 4/7/8 need the scheduler
executor.Execute|the chain motor is the execution function the scheduler drives
SetExecution|the API layer answers slots and active work out of the machinery
SetBroker|the ONE live stream needs the broker (REQ-034)
ServeReadySocket|the restart wrapper's only gate (A2-7)
scheduler.Start|resume enqueueing plus the due ticker (boot steps 7/8)
DrainAndPersist|SIGTERM must persist running executions as interrupted (K-2)
BOOT
if [[ -z $boot_gaps ]]; then
  pass "K-2b" "the daemon constructs the documented boot sequence"
else
  fail "K-2b" "the daemon constructs the documented boot sequence" "$boot_gaps"
fi

# REQ-040.5: no build artefact in the inventory. A compiled binary that is neither ignored nor
# tracked lands in the repository the moment somebody commits everything.
stray_binaries=""
while IFS= read -r f; do
  [[ -z $f ]] && continue
  [[ -f $f ]] || continue
  if file -b --mime-type "$f" 2>/dev/null | grep -q 'application/x-\(executable\|sharedlib\|pie-executable\)'; then
    stray_binaries+="$f is a compiled artefact that git neither tracks nor ignores"$'\n'
  fi
done < <(git ls-files --others --exclude-standard)
if [[ -z $stray_binaries ]]; then
  pass "REQ-040g" "no build artefact loose in the inventory"
else
  fail "REQ-040g" "no build artefact loose in the inventory" "$stray_binaries"
fi

# The acceptance TESTS that carry the list-shaped criteria must exist — a deleted list is a
# silently unproven matrix line.
missing_suites=""
for f in backend/it/surface_test.go backend/it/symmetry_test.go backend/it/vocabulary_test.go \
  src/parity.test.ts src/reload.test.ts; do
  [[ -f $f ]] || missing_suites+="$f is missing"$'\n'
done
if [[ -z $missing_suites ]]; then
  pass "REQ-040e" "the list-shaped acceptance suites are present (routes, symmetry, vocabulary, parity, reload)"
else
  fail "REQ-040e" "the list-shaped acceptance suites are present" "$missing_suites"
fi

# ── informational: prose that mentions a retired construct ──────────────────────────────────

note "info-1" "documentation mentions the default state directory (review by hand)" \
  '/var/lib/devlab' "${F_GO[@]}" "${F_TS[@]}"

# REQ-040.4 + the implementation-language rule: a user-facing message must be English. These are
# ported strings that reach the surface through the error detail — a translation task, not a
# structural defect, so they are reported rather than gated.
note "info-2" "user-facing messages still in German (implementation language is English)" \
  'Errorf\("[^"]*(ungültig|Wochentag|darf keine|ist nicht|braucht|unbekannt|fehlt|nutze)' "${F_GO_SRC[@]}"

# ── optional: run the acceptance tests ──────────────────────────────────────────────────────

if [[ $WITH_TESTS -eq 1 ]]; then
  section "acceptance tests"
  if (cd backend && go test ./it/... >/tmp/abnahme-go.log 2>&1); then
    pass "tests-go" "backend/it integration + list suites"
  else
    fail "tests-go" "backend/it integration + list suites" "$(tail -n 40 /tmp/abnahme-go.log)"
  fi
  if node --test --experimental-strip-types src/parity.test.ts src/reload.test.ts \
    >/tmp/abnahme-node.log 2>&1; then
    pass "tests-ts" "MCP parity + reload state preservation"
  else
    fail "tests-ts" "MCP parity + reload state preservation" "$(tail -n 40 /tmp/abnahme-node.log)"
  fi
fi

# ── report ───────────────────────────────────────────────────────────────────────────────────

if [[ ${#NOTES[@]} -gt 0 ]]; then
  section "informational (not a gate)"
  printf '%s\n' "${NOTES[@]}"
fi

section "result"
printf '%d passed, %d failed\n' "$PASS" "${#FAILED[@]}"
if [[ ${#FAILED[@]} -gt 0 ]]; then
  printf '\nopen acceptance lines:\n'
  printf '  %s\n' "${FAILED[@]}"
  exit 1
fi
exit 0
