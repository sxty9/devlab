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
#     The comment syntax is decided PER FILE (drop_comment_lines): // and /* … */ for the C-family
#     sources, # for shell/unit/sudoers, and NOTHING for Markdown, JSON and HTML — a Markdown
#     bullet, heading or `--flag` is content, and a filter that swallowed it would make every
#     absence check over documentation blind.
#   * deploy/migration/ is excluded from code checks by design: those documents describe the
#     cutover and therefore NAME the retired artefacts they remove. The universality checks
#     (B-45/B-45b) see it anyway: documenting a cutover never justifies a user's home path.
#   * A check that NAMES its files fails when one of them is missing (gate_files): a renamed file
#     would otherwise make grep answer "no match" and the criterion pass on nothing at all.
#   * This script carries no instance literals of its own — the "no instance domain" check works
#     off an allow-list of generic hosts, and the "no instance identity" check DERIVES the
#     offending literal from the code it audits.
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
# F_EVERY is the WHOLE audited inventory, migration documents included: the universality criteria
# (no instance path, no instance domain) hold for every artefact in the repository without exception.
mapfile -t F_EVERY < <(sel '.')
mapfile -t F_ALL < <(sel '.' "$MIGRATION")
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

# drop_comment_lines reads grep's "path:line:text" on stdin and drops the matches that are
# COMMENT-ONLY in the comment syntax OF THAT FILE. Binding the syntax to the file is what keeps the
# filter from lying: the old single filter dropped every line starting with `#`, `*`, `--` or
# `<!--`, which in Markdown is a heading, a bullet, a CLI flag and an HTML comment — so every
# absence check over documentation silently matched nothing.
drop_comment_lines() {
  awk '
    {
      i = index($0, ":")
      if (i == 0) { print; next }
      path = substr($0, 1, i - 1)
      rest = substr($0, i + 1)
      j = index(rest, ":")
      if (j == 0) { print; next }
      text = substr(rest, j + 1)
      sub(/^[[:space:]]+/, "", text)
      if (path ~ /\.(go|ts|tsx|js|jsx|mjs|cjs|css)$/) {
        if (text ~ /^\/\//) next                  # line comment
        if (text ~ /^\/\*/) next                  # block comment opener
        if (text ~ /^\*([[:space:]]|\/|$)/) next  # block comment continuation ("* …", "*/")
      } else if (path ~ /\.(md|markdown|json|html|htm|mod|sum)$/) {
        # No comment-only line here: the whole line is content.
      } else {
        if (text ~ /^#/) next                     # shell, unit, sudoers, conf
      }
      print
    }
  '
}

# hits <pattern> <file...> — matching "path:line:text", comment-only lines removed. /dev/null keeps
# grep in multi-file mode so the path prefix is present even for a single named file.
hits() {
  local pattern="$1"
  shift
  [[ $# -eq 0 ]] && return 0
  grep -nIE -- "$pattern" "$@" /dev/null 2>/dev/null | drop_comment_lines || true
}

# hits_raw <pattern> <file...> — matching lines INCLUDING comments.
hits_raw() {
  local pattern="$1"
  shift
  [[ $# -eq 0 ]] && return 0
  grep -nIE -- "$pattern" "$@" /dev/null 2>/dev/null || true
}

# gate_files <id> <description> <file...> — refuses a check whose file set cannot carry it: no file
# at all, or a named file that does not exist (renamed, moved, deleted). Both would make grep answer
# "nothing" and the criterion pass on air. Returns 1 after FAILing.
gate_files() {
  local id="$1" desc="$2"
  shift 2
  if [[ $# -eq 0 ]]; then
    fail "$id" "$desc" "the check names no file at all — it would pass on an empty inventory"
    return 1
  fi
  local p missing=""
  for p in "$@"; do
    [[ -f $p && -r $p ]] && continue
    missing+="$p does not exist or is unreadable — renamed? the check can prove nothing about it"$'\n'
  done
  if [[ -n $missing ]]; then
    fail "$id" "$desc" "${missing%$'\n'}"
    return 1
  fi
  return 0
}

# absent <id> <description> <pattern> <file...>
absent() {
  local id="$1" desc="$2" pattern="$3"
  shift 3
  gate_files "$id" "$desc" "$@" || return 0
  local out
  out="$(hits "$pattern" "$@")"
  if [[ -z $out ]]; then pass "$id" "$desc"; else fail "$id" "$desc" "$out"; fi
}

# absent_raw <id> <description> <pattern> <file...> — comments count as violations too.
absent_raw() {
  local id="$1" desc="$2" pattern="$3"
  shift 3
  gate_files "$id" "$desc" "$@" || return 0
  local out
  out="$(hits_raw "$pattern" "$@")"
  if [[ -z $out ]]; then pass "$id" "$desc"; else fail "$id" "$desc" "$out"; fi
}

# present <id> <description> <pattern> <file...> — EVERY named file must carry the pattern; a
# union would let one file cover for another that does not contain it at all.
present() {
  local id="$1" desc="$2" pattern="$3"
  shift 3
  gate_files "$id" "$desc" "$@" || return 0
  local f without=""
  for f in "$@"; do
    grep -qIE -- "$pattern" "$f" 2>/dev/null || without+="$f carries no occurrence of /$pattern/"$'\n'
  done
  if [[ -z $without ]]; then pass "$id" "$desc"; else fail "$id" "$desc" "${without%$'\n'}"; fi
}

# present_anywhere <id> <description> <pattern> <file...> — the pattern exists SOMEWHERE in the
# named set (used where the criterion is "the rule is written down", not "every file states it").
present_anywhere() {
  local id="$1" desc="$2" pattern="$3"
  shift 3
  gate_files "$id" "$desc" "$@" || return 0
  local out
  out="$(hits_raw "$pattern" "$@")"
  if [[ -n $out ]]; then pass "$id" "$desc"; else fail "$id" "$desc" "expected at least one occurrence of /$pattern/"; fi
}

# unique_in <id> <description> <pattern> <expected-path-regex> <file...>
# Passes when every match sits in a file matching <expected-path-regex> and there is ≥ 1.
unique_in() {
  local id="$1" desc="$2" pattern="$3" allowed="$4"
  shift 4
  gate_files "$id" "$desc" "$@" || return 0
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
  gate_files "$id" "$desc" "$@" || return 0
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

# ── the audit's own instrument check ─────────────────────────────────────────────────────────
#
# Every criterion below reads one of these sets. An empty set makes each of its checks pass on
# nothing, so the sets are asserted BEFORE the first criterion is judged: the audit states what it
# measured, or it fails.
section "instrument"

set_sizes=""
for name in F_EVERY F_ALL F_GO F_GO_SRC F_GO_WIRED F_TS F_TS_SRC F_SH F_IMPL; do
  declare -n ref="$name"
  [[ ${#ref[@]} -eq 0 ]] && set_sizes+="$name is EMPTY — every check reading it would pass on nothing"$'\n'
  unset -n ref
done
if [[ -z $set_sizes ]]; then
  pass "harness" "every audited file set is non-empty (${#F_EVERY[@]} files, ${#F_GO[@]} Go, ${#F_TS[@]} TS)"
else
  fail "harness" "every audited file set is non-empty" "${set_sizes%$'\n'}"
fi

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

# REQ-012 is a THREE-part statement about the calendar, and each part is checked at the file that
# has to satisfy it — a union over both files would let the fetching view cover for the rendering
# one, which is exactly how "no second data path" went unmeasured.
present "REQ-012a" "the calendar surface reads through the ONE calendar access point" \
  'source\.mercuryRunCalendar\(' src/views/GlobalCalendarView.tsx

count_files "REQ-012d" "exactly ONE calendar endpoint is addressed (no second calendar data path)" \
  1 '/api/mercury/runs/calendar' "${F_TS_SRC[@]}"

absent "REQ-012b" "the calendar view owns no data path of its own (it renders what it is handed)" \
  'source\.[a-zA-Z]+\(|fetch\(|EventSource' src/views/mercury/calendar/MercuryCalendar.tsx

present "REQ-012c" "a past occurrence opens the SAME detail the history opens (no second detail path)" \
  'ExecutionDetail' src/views/mercury/calendar/MercuryCalendar.tsx src/views/mercury/exec/ExecutionsView.tsx

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

present_anywhere "REQ-022a" "the working branch is recorded as never becoming a pull request" \
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
  '/home/[A-Za-z0-9._-]+/|/Users/[A-Za-z0-9._-]+/' "${F_EVERY[@]}"

# ── cross-cutting audits (no single matrix cell, but named by several) ───────────────────────

section "cross-cutting audits"

# B-45 / universality: any absolute URL host outside this generic allow-list names a concrete
# instance. The allow-list holds only vendor-neutral hosts — this file names no instance itself.
# A host without a dot is a placeholder (loopback name, unix-socket stand-in), never an instance
# domain. Everything dotted must be a vendor-neutral or documentation host.
host_allow='^([^.]+|127\.0\.0\.1|github\.com|api\.github\.com|www\.w3\.org|([a-z0-9-]+\.)*example(\.(com|org|net|invalid))?|([a-z0-9-]+\.)*invalid)$'
stray_hosts="$(
  grep -ohIE 'https?://[A-Za-z0-9._~-]+' "${F_EVERY[@]}" 2>/dev/null |
    sed -E 's#https?://##' | sort -u | grep -vE "$host_allow" || true
)"
if [[ -z $stray_hosts ]]; then
  pass "B-45b" "no concrete instance domain in the repository"
else
  fail "B-45b" "no concrete instance domain in the repository" "$stray_hosts"
fi

# B-45 / universality: an instance's ORGANISATION or ACCOUNT name is an instance literal exactly
# like its domain and its home directory — "Instanz-Spezifika leben ausschließlich in der
# Laufzeit-Konfiguration". A name cannot be recognised by its spelling, so the check reads the SHAPE
# that produces one: an identity-shaped configuration key (owner, org, account, user, email, tenant)
# whose lookup carries a baked-in non-empty fallback. Such a fallback IS the instance. Only a
# loopback stand-in is exempt — that is a technical constant, not an identity.
#
# Stage two hunts every further occurrence of the literal the code itself revealed (prose included),
# so fixing the fallback while leaving the name in a comment does not pass. The check therefore names
# no instance of its own.
identity_defaults="$(
  awk '
    # key is the WHOLE configuration key: an identity word anywhere inside an upper-case name.
    function identity(k) { return k ~ /^[A-Z0-9_]*(OWNER|ORG|ACCOUNT|USER|EMAIL|TENANT)[A-Z0-9_]*$/ }
    # A fallback that is DERIVED (a substitution, another variable) names no instance — only a
    # baked-in literal does. Loopback stand-ins are technical constants, not identities.
    function neutral(v) {
      return v == "" || v == "localhost" || v == "127.0.0.1" || v == "::1" ||
             v ~ /[$`]/ || v ~ /^[A-Z0-9]*_[A-Z0-9_]*$/
    }
    function report(v) {
      if (neutral(v)) return
      print FILENAME "\t" FNR "\t" v
    }
    FNR == 1 { armed = 0 }
    {
      line = $0
      sub(/[[:space:]]*\/\/.*$/, "", line)   # C-family line comment
      sub(/^[[:space:]]*#.*$/, "", line)     # shell comment
      if (line == "") { if (armed > 0) armed--; next }

      # Shell: ${KEY:-default} carries key and fallback on one line.
      rest = line
      while (match(rest, /\$\{[A-Z0-9_]+:-[^}]*\}/)) {
        frag = substr(rest, RSTART, RLENGTH)
        rest = substr(rest, RSTART + RLENGTH)
        k = frag; sub(/^\$\{/, "", k); sub(/:-.*$/, "", k)
        v = frag; sub(/^[^-]*:-/, "", v); sub(/\}$/, "", v)
        if (identity(k)) report(v)
      }

      # Go / TypeScript: the lookup arms a short window; the fallback literal follows it.
      hit = 0
      if (match(line, /Getenv\("[A-Z0-9_]+"\)/) || match(line, /process\.env\.[A-Z0-9_]+/)) {
        k = substr(line, RSTART, RLENGTH)
        gsub(/^Getenv\("|"\)$|^process\.env\./, "", k)
        if (identity(k)) { armed = 6; hit = 1 }
        else { armed = 0 }
      }

      if (armed > 0) {
        # A bare fallback: `return "x"`, `?? "x"`, `|| "x"`, `= "x"` — and on the lookup line
        # itself only after the lookup, so the key inside Getenv("…") is never read as a value.
        scan = line
        if (hit) scan = substr(line, RSTART + RLENGTH)
        while (match(scan, /(return|\?\?|\|\||=)[[:space:]]*("[^"]*"|'"'"'[^'"'"']*'"'"')/)) {
          frag = substr(scan, RSTART, RLENGTH)
          scan = substr(scan, RSTART + RLENGTH)
          sub(/^[^"'"'"']*["'"'"']/, "", frag)
          sub(/["'"'"']$/, "", frag)
          report(frag)
        }
        armed--
      }
    }
  ' "${F_IMPL[@]}" 2>/dev/null || true
)"
# Stage two: every FURTHER occurrence of the literal the code revealed, prose included.
identity_out=""
while IFS=$'\t' read -r file line lit; do
  [[ -z ${lit:-} ]] && continue
  identity_out+="$file:$line: identity default \"$lit\" is baked in — it belongs in the runtime configuration"$'\n'
  [[ ${#lit} -lt 4 ]] && continue
  identity_out+="$(grep -nIEw -- "$(sed -E 's/[][\.*^$(){}?+|/]/\\&/g' <<<"$lit")" "${F_EVERY[@]}" 2>/dev/null || true)"$'\n'
done <<<"$identity_defaults"
identity_out="$(grep . <<<"$identity_out" | sort -u || true)"
if [[ -z $identity_out ]]; then
  pass "B-45c" "no instance organisation or account literal (identity comes from the runtime configuration)"
else
  fail "B-45c" "no instance organisation or account literal (identity comes from the runtime configuration)" "$identity_out"
fi

# B-35: the client renders the server's stage array and derives NO stage. A stage NAME in client
# code is the step-name heuristic the criterion retires — the client would be deciding what a stage
# means instead of reading the state the server stated. The forbidden set is DERIVED from the wire
# contract (the Go stage constants), so it cannot drift when a stage is renamed; the type mirror in
# src/types.ts is the one place allowed to spell them.
stage_names="$(
  sed -nE 's/^[[:space:]]*Stage[A-Za-z]+[[:space:]]+Stage[[:space:]]*=[[:space:]]*"([^"]+)".*/\1/p' \
    backend/internal/model/model.go 2>/dev/null | sort -u
)"
stage_literals=""
while IFS= read -r st; do
  [[ -z ${st:-} ]] && continue
  hit="$(grep -nIE "['\"]${st}['\"]" "${F_TS_SRC[@]}" 2>/dev/null | grep -vE '^src/types\.ts:' || true)"
  [[ -n $hit ]] && stage_literals+="$hit"$'\n'
done <<<"$stage_names"
if [[ $(grep -c . <<<"$stage_names") -lt 5 ]]; then
  fail "B-35b" "no stage-name literal in the client" \
    "the stage contract could not be read from backend/internal/model/model.go — the forbidden set would be unknown"
elif [[ -z $stage_literals ]]; then
  pass "B-35b" "no stage-name literal in the client (the server's stage array is rendered, not interpreted)"
else
  fail "B-35b" "no stage-name literal in the client (the server's stage array is rendered, not interpreted)" \
    "$(sort -u <<<"$stage_literals" | grep .)"
fi

# REQ-030.5 / brand integrity: Tailwind's /opacity modifier only works on a token whose value is a
# colour CHANNEL (rgb(var(--x) / <alpha-value>)). On a token that is a plain var() Tailwind DROPS
# the whole declaration without a word — the mark then renders NOTHING, which is exactly how a
# state badge silently disappears. The forbidden token set is DERIVED from the theme preset, so it
# cannot drift: every colour written as v('…') instead of rgb('…'), in the spelling Tailwind gives
# it (a nested member reads <group>-<member>, DEFAULT reads as the group alone).
var_tokens="$(
  awk '
    /colors: \{/      { inside = 1; next }
    /borderRadius: \{/ { inside = 0 }
    !inside { next }
    {
      line = $0
      opens = (line ~ /:[[:space:]]*\{/)
      closes = (line ~ /\}/)
      grp = cur
      if (opens) {
        name = line
        sub(/:[[:space:]]*\{.*$/, "", name)
        gsub(/[^A-Za-z0-9_-]/, "", name)
        grp = name
        if (!closes) { cur = name; next }
      } else if (closes && line !~ /v\(/) {
        cur = ""
        next
      }
      rest = line
      while (match(rest, /[A-Za-z0-9_]+:[[:space:]]*v\(/)) {
        member = substr(rest, RSTART, RLENGTH)
        sub(/:.*$/, "", member)
        if (grp == "")            print member
        else if (member == "DEFAULT") print grp
        else                      print grp "-" member
        rest = substr(rest, RSTART + RLENGTH)
      }
    }
  ' src/theme/tailwind-preset.ts 2>/dev/null | sort -u
)"
opacity_on_var=""
while read -r token; do
  [[ -z ${token:-} ]] && continue
  hit="$(grep -nIE "[a-z-]+-${token}/" "${F_TS_SRC[@]}" 2>/dev/null || true)"
  [[ -n $hit ]] && opacity_on_var+="$hit"$'\n'
done <<<"$var_tokens"
if [[ $(grep -c . <<<"$var_tokens") -lt 5 ]]; then
  fail "B-46" "no /opacity modifier on a var()-valued theme token" \
    "the theme preset could not be read — the forbidden token set would be unknown"
elif [[ -z $opacity_on_var ]]; then
  pass "B-46" "no /opacity modifier on a var()-valued theme token (Tailwind drops it silently)"
else
  fail "B-46" "no /opacity modifier on a var()-valued theme token (Tailwind drops it silently)" \
    "$(sort -u <<<"$opacity_on_var")"
fi

# REQ-007.2 / B-41: every upload point offers the clipboard as an equal input path.
upload_files="$(grep -lIE 'uploadVision\(|mercuryUploadAttachment\(' "${F_TS_SRC[@]}" 2>/dev/null | grep -v '^src/data/' || true)"
missing_paste=""
if [[ -z $upload_files ]]; then
  # No upload point found at all: either the two upload operations were renamed or the surface lost
  # them. Either way the criterion is unproven, so it must not read as satisfied.
  missing_paste="no upload point found at all — the check found nothing to verify (renamed operation?)"$'\n'
fi
while IFS= read -r f; do
  [[ -z $f ]] && continue
  grep -qE 'usePasteFiles|onPaste|filesFromClipboard' "$f" || missing_paste+="$f has no clipboard path"$'\n'
done <<<"$upload_files"
if [[ -z $missing_paste ]]; then
  pass "B-41b" "every upload point accepts the clipboard as an equal input path"
else
  fail "B-41b" "every upload point accepts the clipboard as an equal input path" "${missing_paste%$'\n'}"
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
# The route table must be READ for the check to mean anything: a renamed file would answer "no
# holes" on a table nobody looked at, so its presence and its mutating routes are asserted first.
csrf_holes=""
if [[ ! -f backend/internal/api/api.go ]]; then
  csrf_holes="backend/internal/api/api.go does not exist — the route table cannot be audited"
elif ! grep -qE 'mux\.HandleFunc\("(POST|PUT|DELETE) ' backend/internal/api/api.go; then
  csrf_holes="backend/internal/api/api.go registers no mutating route at all — the check would pass on nothing"
else
  csrf_holes="$(
    grep -nE 'mux\.HandleFunc\("(POST|PUT|DELETE) [^"]+",[[:space:]]*s\.guard\(' \
      backend/internal/api/api.go || true
  )"
fi
if [[ -z $csrf_holes ]]; then
  pass "B-03" "every mutating route is bound to a CSRF-enforcing guard"
else
  fail "B-03" "every mutating route is bound to a CSRF-enforcing guard" "$csrf_holes"
fi

# K-2 / REQ-039.1 / REQ-013: the boot sequence is only real if the daemon CONSTRUCTS it. An
# unwired daemon makes every downstream criterion unreachable in production, so the calls the
# documented boot order needs are checked one by one — against the CODE, comments stripped. The
# documented boot order sits as prose at the top of main.go and NAMES every one of these calls, so
# a comment-blind check would be satisfied by the documentation of a daemon that wires nothing.
boot_gaps=""
boot_code=""
if [[ -f backend/cmd/devlabd/main.go ]]; then
  boot_code="$(sed -E 's#//.*$##' backend/cmd/devlabd/main.go | grep -vE '^[[:space:]]*(/\*|\*)' || true)"
else
  boot_gaps="backend/cmd/devlabd/main.go does not exist — the daemon's boot cannot be audited"$'\n'
fi
while IFS='|' read -r call why; do
  [[ -z $call ]] && continue
  [[ -z $boot_code ]] && continue
  grep -qF -- "$call" <<<"$boot_code" ||
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

# REQ-040.5: no build artefact in the inventory. The WHOLE inventory is inspected, tracked files
# included — a committed binary is not a smaller problem than an unignored one, it is the larger:
# it is already in the repository. (Scanning only the untracked half is how a tracked 9-MB daemon
# stays invisible.)
stray_binaries=""
while IFS= read -r line; do
  [[ -z $line ]] && continue
  f="${line%%:*}"
  case "${line##*:}" in
    *application/x-executable* | *application/x-sharedlib* | *application/x-pie-executable* | *application/x-archive*)
      if git ls-files --error-unmatch -- "$f" >/dev/null 2>&1; then
        stray_binaries+="$f is a compiled artefact and git TRACKS it — it is committed into the repository"$'\n'
      else
        stray_binaries+="$f is a compiled artefact that git neither tracks nor ignores"$'\n'
      fi
      ;;
  esac
done < <(file --mime-type -- "${F_EVERY[@]}" 2>/dev/null)
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
