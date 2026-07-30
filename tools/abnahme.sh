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
# The WORKING-STATE path K-1 speaks of: the packages that hold a managed repository's working copy —
# the fold-in state machine and the per-user worktrees. Scoped as a SET, not as a list of names, so a
# new file in either package is audited the day it appears.
mapfile -t F_WORKSTATE < <(sel '^backend/internal/(workbench|workspace)/.*\.go$' '(_test\.go$)|/testdata/')
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

# ── the git-call matcher (K-1) ───────────────────────────────────────────────────────────────
#
# This code writes git as an ARGUMENT LIST, never as one shell string: `b.gitUser(ctx, "reset",
# "--hard", targetSHA)`. A pattern that expects contiguous shell text (`reset[[:space:]]+--hard`)
# therefore matches NOTHING here — which is how the two K-1 checks came to pass on a code base in
# which the construct they forbid could not be seen at all.
#
# So the source is NORMALISED first and matched afterwards: quotes and commas become spaces, and a
# continued argument list is joined onto ONE logical line (gofmt breaks a long call after a comma).
# A single pattern per forbidden construct then covers BOTH spellings — the shell string and the
# argument list. The reported line number is where the logical line begins.
git_calls() {
  local f
  for f in "$@"; do
    [[ -f $f && -r $f ]] || continue
    awk -v F="$f" '
      function emit(n, s) { gsub(/["'"'"',]/, " ", s); print F ":" n ":" s }
      {
        line = $0
        sub(/[[:space:]]+$/, "", line)
        if (buf == "") { start = FNR; buf = line } else { buf = buf " " line }
        # A trailing comma, backslash or open parenthesis means the argument list continues. The
        # window is bounded, so a long composite literal cannot swallow half the file.
        if (buf ~ /[,\\(]$/ && FNR - start < 5) next
        emit(start, buf)
        buf = ""
      }
      END { if (buf != "") emit(start, buf) }
    ' "$f"
  done
}

# The two constructs K-1 forbids, in normalised spelling. A reset is judged by its TARGET: onto a
# resolved commit or HEAD it is the deliberate, actor-bound discard the architecture allows (behind
# the UI's confirmation, with the previous tip sheltered); onto a REMOTE ref it throws away work the
# remote never saw, which is the construction fault itself.
FORCE_CHECKOUT='checkout([[:space:]]+-[a-zA-Z-]+)*[[:space:]]+-B([[:space:]]|$)'
RESET_TO_REMOTE='reset([[:space:]]+-[a-zA-Z-]+)*[[:space:]]+--hard([[:space:]]+-[a-zA-Z-]+)*[[:space:]]+[^[:space:]]*(origin|remote|upstream)'

# git_hits <pattern> <file...> — normalised git calls matching the pattern, comment lines dropped.
git_hits() {
  local pattern="$1"
  shift
  [[ $# -eq 0 ]] && return 0
  git_calls "$@" | drop_comment_lines | grep -E -- "$pattern" || true
}

# git_absent <id> <description> <pattern> <file...>
git_absent() {
  local id="$1" desc="$2" pattern="$3"
  shift 3
  gate_files "$id" "$desc" "$@" || return 0
  local out
  out="$(git_hits "$pattern" "$@")"
  if [[ -z $out ]]; then pass "$id" "$desc"; else fail "$id" "$desc" "$out"; fi
}

# ── the identity-default scanner (B-45c) ─────────────────────────────────────────────────────
#
# Reads "path<TAB>line<TAB>literal" for every configuration lookup that names an IDENTITY and carries
# a baked-in fallback. It is a function, not an inline program, so the instrument section can run it
# against samples with a known answer — an absence check nobody has ever seen fire is a check whose
# silence proves nothing.
identity_scan() {
  [[ $# -eq 0 ]] && return 0
  awk '
    # key is the WHOLE configuration key: an identity word anywhere inside an upper-case name.
    function identity(k) { return k ~ /^[A-Z0-9_]*(OWNER|ORG|ACCOUNT|USER|EMAIL|TENANT)[A-Z0-9_]*$/ }
    # A fallback that is DERIVED (a substitution, another variable) names no instance — only a
    # baked-in literal does. Loopback stand-ins are technical constants, not identities.
    function neutral(v) {
      return v == "" || v == "localhost" || v == "127.0.0.1" || v == "::1" ||
             v ~ /[$`]/ || v ~ /^[A-Z0-9]*_[A-Z0-9_]*$/
    }
    # A key that names WHERE the identity is read from (…_FILE, …_PATH, …_DIR) is not the identity:
    # its value is a LOCATION. An organisation is never an absolute path, so a path default under
    # such a key stays admissible — while the same key with a name-shaped default is still reported.
    function location(k, v) { return k ~ /_(FILE|PATH|DIR)$/ && v ~ /^\// }
    function report(k, v) {
      if (neutral(v) || location(k, v)) return
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
        if (identity(k)) report(k, v)
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
          report(k, frag)
        }
        armed--
      }
    }
  ' "$@"
}

# ── the token-exposure matchers (B-13 / B-43) ────────────────────────────────────────────────
#
# One secret, two languages — and each criterion had been written for only ONE of them. "No token on
# a command line" searched shell files, so the Go path that spliced the credential into a remote URL
# was invisible to it; "no token in a log line" searched Go files, so a shell that echoed the
# variable was invisible to that one. The asymmetry was mutual, so both criteria are measured over
# BOTH languages now, and the instrument section proves each half can see its own leak.
#
# The patterns stay PER LANGUAGE because the same letters mean opposite things in the two: the shell
# reference `"$DEVLAB_GH_TOKEN"` inside a Go string is the inline credential HELPER — the tokenless
# construction that keeps the secret OFF argv — while in a shell script it is the value itself.
#
# What puts a token on a command line is the CALL, not the letters. The credential travels in the
# environment by design, so an env assignment (`"DEVLAB_GH_TOKEN="+token`), the Authorization header
# an HTTP client sets, and the redaction of a value that leaked are admissible neighbours the matcher
# must leave alone. It sees the three spellings that reach argv: an exec call carrying the value, an
# argument slice carrying it, and a credential spliced into a URL.
ARGV_TOKEN_GO='exec\.Command(Context)?\([^)]*[Tt]oken\b|append\((args|full|argv|cmdArgs)[[:space:]]*,[^)]*[Tt]oken\b|https?://[^"]*"[[:space:]]*\+[[:space:]]*[A-Za-z_.]*[Tt]oken\b'
ARGV_TOKEN_SH='\$DEVLAB_GH_TOKEN|--token|-t[[:space:]]+"?\$[A-Z_]*TOKEN'
LOG_TOKEN_GO='log\.[A-Za-z]+\([^)]*[Tt]oken|Printf\([^)]*[Tt]oken[^)]*\)'
LOG_TOKEN_SH='(echo|printf|logger)[^|;&]*\$[A-Za-z_]*TOKEN'

# two_absent <id> <desc> <pattern-a> <files-a…> :: <pattern-b> <files-b…> — ONE criterion measured
# over two languages with the pattern each needs. A criterion that spans both may not become two
# similar sibling checks: it would then be possible to pass one half and call the line green.
two_absent() {
  local id="$1" desc="$2"
  shift 2
  local -a a=() b=()
  local seen=0
  local arg
  for arg in "$@"; do
    if [[ $arg == "::" ]]; then
      seen=1
      continue
    fi
    if [[ $seen -eq 0 ]]; then a+=("$arg"); else b+=("$arg"); fi
  done
  if [[ $seen -eq 0 || ${#a[@]} -lt 2 || ${#b[@]} -lt 2 ]]; then
    fail "$id" "$desc" "the check was given only one half — it would measure one language and report both"
    return
  fi
  local pa="${a[0]}" pb="${b[0]}"
  gate_files "$id" "$desc" "${a[@]:1}" || return 0
  gate_files "$id" "$desc" "${b[@]:1}" || return 0
  local out
  out="$(
    hits "$pa" "${a[@]:1}"
    hits "$pb" "${b[@]:1}"
  )"
  if [[ -z $out ]]; then pass "$id" "$desc"; else fail "$id" "$desc" "$out"; fi
}

# ── the env_keep allow-list (the pinned wrapper's boundary) ───────────────────────────────────
#
# devlab-exec confines every path to <state root>/workspaces/<user> and DERIVES that root from
# DEVLAB_STATE_DIR / DEVLAB_WORKSPACES with the template's defaults. Those overrides exist for direct
# invocation in tests and are harmless in production for exactly ONE reason: sudo's env_reset strips
# them. A service that could set them would pick the boundary it is measured against — that is, hand
# itself every user's files. The invariant was written down as PROSE in both files, so adding both
# names to the env_keep line passed every check in this script.
#
# It is an ALLOW-LIST, not a deny-list of the two known names: "NOTHING ELSE MAY JOIN THIS LIST" is
# what the sudoers file itself says, and a deny-list would have to be extended for every seam the
# install wrapper grows. Prints one line per name that is on the list without being permitted.
ENV_KEEP_ALLOWED='DEVLAB_GH_TOKEN GIT_TERMINAL_PROMPT'
env_keep_strays() {
  local f="$1" line names n
  [[ -f $f && -r $f ]] || return 0
  # sudoers continues a directive with a trailing backslash, so the file is folded before the
  # directives are read; a commented example is prose, not a privilege.
  while IFS= read -r line; do
    names="$(sed -E 's/.*env_keep[[:space:]]*\+?=[[:space:]]*//; s/"//g' <<<"$line")"
    for n in $names; do
      [[ " $ENV_KEEP_ALLOWED " == *" $n "* ]] && continue
      printf '%s\n' "$n"
    done
  done < <(sed -E ':a; /\\$/ { N; s/\\\n[[:space:]]*//; ba }' "$f" | grep -E '^[[:space:]]*[^#]*env_keep')
}

# ── the dead-shipped-function scan (REQ-040h) ─────────────────────────────────────────────────
#
# REQ-040c/d forbid a dead MODULE and an orphaned PACKAGE; the same rule holds one level down. An
# unexported function that occurs exactly once in the shipped sources — its own declaration — is
# reached from nowhere: whatever calls it is a test, so the shipped binary carries code whose only
# purpose is to be asserted about. That is how a hand-rolled Basic-auth encoder survived the move to
# the credential helper, kept alive by the very test that proved the credential no longer goes on argv.
#
# Unexported PLAIN functions only. A method may satisfy an interface and be called through it, and an
# exported identifier may be used from another package — neither is decidable from one grep, and a
# check that guesses is worse than no check. The scan reports the LEAF of a dead chain: a helper whose
# only caller was the dead function surfaces on the next run, once the leaf is gone.
dead_shipped_funcs() {
  [[ $# -eq 0 ]] && return 0
  local decls uses
  decls="$(grep -hoE '^func [a-z][A-Za-z0-9_]*\(' "$@" 2>/dev/null | sed -E 's/^func //; s/\($//' | sort -u)"
  [[ -n $decls ]] || return 0
  uses="$(grep -hoE '\b[A-Za-z_][A-Za-z0-9_]*\b' "$@" 2>/dev/null | sort | uniq -c)"
  awk 'NR == FNR { c[$2] = $1; next } (c[$0] + 0) <= 1 { print }' <(printf '%s\n' "$uses") <(printf '%s\n' "$decls")
}

# ── the contract-state fingerprint (contract-a) ───────────────────────────────────────────────
#
# A protocol entry must describe the change that is actually IN the tree, and naming the path proved
# only that SOME change to it was described once. A second, undescribed change to a file an older
# entry already named therefore passed unseen — appending an exported type to the frozen wire
# contract was enough. So an entry carries the file's CONTENT fingerprint and the check compares it
# against the file as it stands: describing a state is what records it, and a further change changes
# the fingerprint.
#
# The fingerprint is git's own blob hash of the working-tree file, shortened — the identity git
# itself uses, so it is recomputed with `git hash-object <path>` and needs no second tool.
content_fingerprint() {
  git hash-object -- "$1" 2>/dev/null | cut -c1-12
}

# contract_state_faults <body> <path…> — one fault line per path whose CURRENT state the protocol
# body does not carry. Kept apart from the loop that collects the changed paths so the instrument
# section can run it against a body whose answer is known.
contract_state_faults() {
  local body="$1"
  shift
  local p fp
  for p in "$@"; do
    if ! grep -qF -- "$p" <<<"$body"; then
      printf '%s\n' "$p changed and no entry in the protocol names it (BAUPLAN §0.2: file, reason, diff hint)"
      continue
    fi
    fp="$(content_fingerprint "$p")"
    if [[ -z $fp ]]; then
      printf '%s\n' "$p could not be fingerprinted — the protocol cannot be bound to a state nobody can read"
      continue
    fi
    grep -qF -- "$p@$fp" <<<"$body" ||
      printf '%s\n' "$p is named in the protocol, but no entry carries its CURRENT state \`$p@$fp\` — the protocol describes an older version of the file, so this change is undescribed"
  done
}

# ── the audit's own instrument check ─────────────────────────────────────────────────────────
#
# Every criterion below reads one of these sets. An empty set makes each of its checks pass on
# nothing, so the sets are asserted BEFORE the first criterion is judged: the audit states what it
# measured, or it fails.
section "instrument"

set_sizes=""
for name in F_EVERY F_ALL F_GO F_GO_SRC F_GO_WIRED F_WORKSTATE F_TS F_TS_SRC F_SH F_IMPL; do
  declare -n ref="$name"
  [[ ${#ref[@]} -eq 0 ]] && set_sizes+="$name is EMPTY — every check reading it would pass on nothing"$'\n'
  unset -n ref
done
if [[ -z $set_sizes ]]; then
  pass "harness" "every audited file set is non-empty (${#F_EVERY[@]} files, ${#F_GO[@]} Go, ${#F_TS[@]} TS)"
else
  fail "harness" "every audited file set is non-empty" "${set_sizes%$'\n'}"
fi

# The K-1 matcher's own SENSITIVITY, measured before any criterion reads it. An absence check is
# worth exactly as much as its ability to see the construct it forbids, and this one must see a git
# call written as an argument list — the only spelling this code uses. So the matcher is run against
# samples whose answer is known: four that MUST be caught (both constructs, in both spellings) and
# five neighbours that must NOT be (a reset onto HEAD or onto a resolved sha, a plain `-b` checkout,
# a tool-table row that merely names "checkout", and a comment that documents the removal).
sample_dir="$(mktemp -d)"
trap 'rm -rf "$sample_dir"' EXIT
cat >"$sample_dir/caught.go" <<'SAMPLE'
func a() { b.gitUser(ctx, "checkout", "-f", "-B", branch) }
func b() { b.gitUser(ctx, "reset", "--hard", "origin/"+def) }
func c() {
	b.gitUser(ctx, "reset",
		"--hard", remoteRef)
}
SAMPLE
cat >"$sample_dir/caught.sh" <<'SAMPLE'
git checkout -f -B "$branch"
git reset --hard origin/main
SAMPLE
cat >"$sample_dir/clean.go" <<'SAMPLE'
func a() { b.gitUser(ctx, "reset", "--hard", "HEAD") }
func b() { b.gitUser(ctx, "reset", "--hard", targetSHA) }
func c() { mustGit(t, wt, "checkout", "--quiet", "-b", Branch) }
func d() { rows = append(rows, tool{Op: "checkout", Method: http.MethodPost, Path: "/api/repos/{id}/checkout"}) }
// NEVER checkout -f -B, NEVER reset --hard origin/<branch> (REQ-023.1)
SAMPLE
matcher_faults=""
caught="$(git_hits "$FORCE_CHECKOUT|$RESET_TO_REMOTE" "$sample_dir/caught.go" "$sample_dir/caught.sh" | grep -c . || true)"
[[ $caught -eq 5 ]] || matcher_faults+="the matcher caught $caught of the 5 forbidden sample calls — it cannot see what K-1 forbids"$'\n'
missed="$(git_hits "$FORCE_CHECKOUT|$RESET_TO_REMOTE" "$sample_dir/clean.go" || true)"
[[ -z $missed ]] || matcher_faults+="the matcher flagged an admissible neighbour:"$'\n'"$missed"$'\n'
rm -rf "$sample_dir"
trap - EXIT
if [[ -z $matcher_faults ]]; then
  pass "harness-git" "the K-1 matcher sees a git call in both spellings (shell string AND argument list)"
else
  fail "harness-git" "the K-1 matcher sees a git call in both spellings (shell string AND argument list)" "${matcher_faults%$'\n'}"
fi

# The same measurement for the identity scanner behind B-45c: it must SEE a baked-in organisation in
# both spellings, and must not read a technical constant as one. Two samples, known answer.
id_dir="$(mktemp -d)"
trap 'rm -rf "$id_dir"' EXIT
cat >"$id_dir/caught.sh" <<'SAMPLE'
OWNER="${DEVLAB_GH_OWNER:-an-org}"
MAIL="${DEVLAB_ADMIN_EMAIL:-ops@an-org.example}"
SAMPLE
cat >"$id_dir/caught.go" <<'SAMPLE'
func owner() string {
	if o := os.Getenv("DEVLAB_GH_ORG"); o != "" {
		return o
	}
	return "an-org"
}
SAMPLE
cat >"$id_dir/clean.sh" <<'SAMPLE'
OWNER_FILE="${DEVLAB_GH_OWNER_FILE:-/etc/devlab/gh-owner}"
OWNER="${DEVLAB_GH_OWNER:-}"
PORT="${DEVLAB_PORT:-8080}"
HOST="${DEVLAB_HOST:-localhost}"
RUNNER="${DEVLAB_RUNS_USER:-$DEVLAB_USER}"
SAMPLE
cat >"$id_dir/clean.go" <<'SAMPLE'
func topic() string {
	if t := os.Getenv("DEVLAB_GH_TOPIC"); t != "" {
		return t
	}
	return "holistic"
}
SAMPLE
id_faults=""
id_caught="$(identity_scan "$id_dir/caught.sh" "$id_dir/caught.go" 2>/dev/null | grep -c . || true)"
[[ $id_caught -eq 3 ]] || id_faults+="the identity scanner found $id_caught of the 3 baked-in identities — B-45c would pass on a check that sees nothing"$'\n'
id_missed="$(identity_scan "$id_dir/clean.sh" "$id_dir/clean.go" 2>/dev/null || true)"
[[ -z $id_missed ]] || id_faults+="the identity scanner read a technical constant as an identity:"$'\n'"$id_missed"$'\n'
rm -rf "$id_dir"
trap - EXIT
if [[ -z $id_faults ]]; then
  pass "harness-id" "the identity scanner sees a baked-in organisation, and only that"
else
  fail "harness-id" "the identity scanner sees a baked-in organisation, and only that" "${id_faults%$'\n'}"
fi

# The token matchers, both halves, known answer. The caught samples are the leaks that were actually
# possible: the credential spliced into a clone URL (Go), a token handed over as a flag (Go and
# shell), and a variable echoed into a log (shell). The clean samples are the constructions this code
# uses ON PURPOSE — the value in the ENVIRONMENT, the inline credential helper that only NAMES the
# variable, the Authorization header, and the redaction of a value that leaked — and a matcher that
# flagged one of them would be turned off within a week.
tok_dir="$(mktemp -d)"
trap 'rm -rf "$tok_dir"' EXIT
cat >"$tok_dir/caught.go" <<'SAMPLE'
func a(ctx context.Context, token, dir string) {
	cmd := exec.CommandContext(ctx, "git", "clone", "https://x-access-token:"+token+"@github.com/o/r.git", dir)
}
func b(args []string, token string) []string { return append(args, "--token", token) }
func c(token string) string { return "https://x-access-token:" + token + "@github.com/o/r.git" }
SAMPLE
cat >"$tok_dir/caught.sh" <<'SAMPLE'
git clone "https://x-access-token:$DEVLAB_GH_TOKEN@github.com/o/r.git" "$dir"
echo "using $DEVLAB_GH_TOKEN" >&2
SAMPLE
cat >"$tok_dir/clean.go" <<'SAMPLE'
func a(cmd *exec.Cmd, token string) { cmd.Env = append(os.Environ(), "DEVLAB_GH_TOKEN="+token) }
func b() []string {
	return []string{"-c", `credential.helper=!f() { printf 'password=%s\n' "$DEVLAB_GH_TOKEN"; }; f`}
}
func c(req *http.Request, token string) { req.Header.Set("Authorization", "Bearer "+token) }
func d(msg, token string) string { return strings.ReplaceAll(msg, token, "***") }
func e(ctx context.Context, args []string) *exec.Cmd { return exec.CommandContext(ctx, "git", args...) }
SAMPLE
cat >"$tok_dir/clean.sh" <<'SAMPLE'
Defaults!/usr/local/sbin/devlab-exec env_keep += "DEVLAB_GH_TOKEN GIT_TERMINAL_PROMPT"
exec git -c "safe.directory=*" "$@"
echo "cloning $repo" >&2
SAMPLE
tok_faults=""
tok_caught="$( {
  hits "$ARGV_TOKEN_GO" "$tok_dir/caught.go"
  hits "$ARGV_TOKEN_SH" "$tok_dir/caught.sh"
  hits "$LOG_TOKEN_SH" "$tok_dir/caught.sh"
} | grep -c . || true)"
[[ $tok_caught -ge 5 ]] || tok_faults+="the token matchers caught $tok_caught of the 5 exposures (3 Go, 2 shell) — B-13/B-43 would pass on checks that cannot see their own leak"$'\n'
tok_missed="$( {
  hits "$ARGV_TOKEN_GO" "$tok_dir/clean.go"
  hits "$LOG_TOKEN_GO" "$tok_dir/clean.go"
  hits "$ARGV_TOKEN_SH" "$tok_dir/clean.sh"
  hits "$LOG_TOKEN_SH" "$tok_dir/clean.sh"
} || true)"
[[ -z $tok_missed ]] || tok_faults+="a token matcher flagged an admissible construction:"$'\n'"$tok_missed"$'\n'
rm -rf "$tok_dir"
trap - EXIT
if [[ -z $tok_faults ]]; then
  pass "harness-token" "the token matchers see a leak in BOTH languages, and spare the env/header/redaction paths"
else
  fail "harness-token" "the token matchers see a leak in BOTH languages, and spare the env/header/redaction paths" "${tok_faults%$'\n'}"
fi

# The env_keep allow-list, known answer: the two boundary variables the wrapper's header forbids ARE
# seen when they join the list, and the shipped list itself reads clean.
keep_dir="$(mktemp -d)"
trap 'rm -rf "$keep_dir"' EXIT
cat >"$keep_dir/caught.sudoers" <<'SAMPLE'
# DEVLAB_STATE_DIR must never be kept — this comment is prose, not a privilege.
Defaults!/usr/local/sbin/devlab-exec env_keep += "DEVLAB_GH_TOKEN GIT_TERMINAL_PROMPT DEVLAB_STATE_DIR DEVLAB_WORKSPACES"
SAMPLE
cat >"$keep_dir/clean.sudoers" <<'SAMPLE'
# The boundary variables DEVLAB_STATE_DIR and DEVLAB_WORKSPACES are deliberately absent.
Defaults!/usr/local/sbin/devlab-exec env_keep += "DEVLAB_GH_TOKEN GIT_TERMINAL_PROMPT"
SAMPLE
keep_faults=""
keep_caught="$(env_keep_strays "$keep_dir/caught.sudoers" | sort -u | tr '\n' ' ')"
[[ $keep_caught == *DEVLAB_STATE_DIR* && $keep_caught == *DEVLAB_WORKSPACES* ]] ||
  keep_faults+="the env_keep scan saw [$keep_caught] where both boundary variables were added — the invariant would stay prose"$'\n'
keep_missed="$(env_keep_strays "$keep_dir/clean.sudoers")"
[[ -z $keep_missed ]] || keep_faults+="the env_keep scan flagged a permitted name: $keep_missed"$'\n'
rm -rf "$keep_dir"
trap - EXIT
if [[ -z $keep_faults ]]; then
  pass "harness-keep" "the env_keep scan sees a boundary variable joining the list, and only that"
else
  fail "harness-keep" "the env_keep scan sees a boundary variable joining the list, and only that" "${keep_faults%$'\n'}"
fi

# The dead-function scan, known answer: an unexported function only a test names is FOUND, while a
# called one, an exported one and a method are left alone.
dead_dir="$(mktemp -d)"
trap 'rm -rf "$dead_dir"' EXIT
cat >"$dead_dir/sample.go" <<'SAMPLE'
func basicAuth(token string) string { return base64Std("x-access-token:" + token) }
func base64Std(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
func Exported() string { return caller() }
func (s *Store) helper() string { return "" }
func caller() string { return used() }
func used() string { return "used" }
SAMPLE
dead_faults=""
dead_found="$(dead_shipped_funcs "$dead_dir/sample.go" | sort | tr '\n' ' ')"
[[ $dead_found == *basicAuth* ]] || dead_faults+="the dead-function scan did not see basicAuth, whose only caller is a test — it found [$dead_found]"$'\n'
for alive in base64Std used caller Exported helper; do
  [[ $dead_found == *"$alive"* ]] && dead_faults+="the dead-function scan reported $alive, which is reached: [$dead_found]"$'\n'
done
rm -rf "$dead_dir"
trap - EXIT
if [[ -z $dead_faults ]]; then
  pass "harness-dead" "the dead-function scan sees a shipped function only a test reaches, and only that"
else
  fail "harness-dead" "the dead-function scan sees a shipped function only a test reaches, and only that" "${dead_faults%$'\n'}"
fi

# The contract-state matcher, known answer, measured against a REAL frozen file: a body that names the
# path but carries a stale fingerprint must be reported (that is the exact hole — the path was named
# by an older entry), a body that carries the current one must not, and an unnamed path must be.
state_faults=""
state_probe='deploy/migration/80-vertragsaenderungen.md'
state_fp="$(content_fingerprint "$state_probe")"
if [[ -z $state_fp ]]; then
  state_faults+="no fingerprint could be taken of $state_probe — the contract check would compare against nothing"$'\n'
else
  [[ -n $(contract_state_faults "described once: $state_probe@0000deadbeef" "$state_probe") ]] ||
    state_faults+="a stale fingerprint passed — a second, undescribed change to an already-named file would slip through"$'\n'
  [[ -z $(contract_state_faults "described now: $state_probe@$state_fp" "$state_probe") ]] ||
    state_faults+="the current fingerprint was rejected — every legitimate entry would fail"$'\n'
  [[ -n $(contract_state_faults "names nothing at all" "$state_probe") ]] ||
    state_faults+="an unnamed path passed — the original criterion would be lost"$'\n'
fi
if [[ -z $state_faults ]]; then
  pass "harness-contract" "the contract-state matcher binds an entry to the file as it stands (a stale fingerprint fails)"
else
  fail "harness-contract" "the contract-state matcher binds an entry to the file as it stands" "${state_faults%$'\n'}"
fi

# ── §1 the six construction faults ───────────────────────────────────────────────────────────

section "§1  construction faults K-1 … K-6"

# A force-checkout has no admissible use anywhere in the daemon, so this one is judged over the whole
# shipped source: the old `EnsureDevBranch` re-pointed the working branch at the remote on every run
# and dropped eighteen strands of committed work in a day.
git_absent "K-1a" "no force-checkout of the workbench branch (work is folded in, never re-pointed)" \
  "$FORCE_CHECKOUT" "${F_GO_SRC[@]}" "${F_SH[@]}"

# K-1b is scoped the way the criterion is scoped — the WORKING-STATE path: the packages that hold a
# managed repository's working copy, plus every shipped script (none of them has business resetting a
# working copy). Outside that path K-1c decides.
git_absent "K-1b" "no reset --hard onto a remote ref in the working-state path (fold in, never re-point)" \
  "$RESET_TO_REMOTE" "${F_WORKSTATE[@]}" "${F_SH[@]}"

# K-1c pins the ONE reset onto a remote that the tree legitimately contains: the constitution MIRROR.
# That clone holds no local work by construction — every write commits and pushes, and a push that
# loses the race is retried on the refreshed state — so refreshing it IS a reset to the remote.
# Pinning it does double duty: the exception cannot spread to a second file, and the known occurrence
# is the matcher's live WITNESS. If the normalisation above ever stopped seeing an argument-list git
# call, this hit would disappear and the check fails — instead of K-1a/K-1b passing on an empty
# result, which is exactly the way they used to pass.
mirror_ok='^backend/internal/axiomrepo/'
reset_hits="$(git_hits "$RESET_TO_REMOTE" "${F_GO_SRC[@]}" "${F_SH[@]}")"
mirror_witness="$(awk -F: -v ok="$mirror_ok" '$1 ~ ok' <<<"$reset_hits" | grep -c . || true)"
mirror_stray="$(awk -F: -v ok="$mirror_ok" '$1 !~ ok' <<<"$reset_hits" | grep . || true)"
if [[ $mirror_witness -eq 0 ]]; then
  fail "K-1c" "the ONLY reset onto a remote is the constitution mirror (and the matcher proves it sees one)" \
    "the mirror's own reset onto origin/<branch> was NOT found — the matcher no longer sees an argument-list git call, so K-1a/K-1b would be passing on nothing"
elif [[ -n $mirror_stray ]]; then
  fail "K-1c" "the ONLY reset onto a remote is the constitution mirror (and the matcher proves it sees one)" \
    "$mirror_stray"
else
  pass "K-1c" "the ONLY reset onto a remote is the constitution mirror (and the matcher proves it sees one)"
fi

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

two_absent "B-13" "no token ever reaches a log line (Go AND shell)" \
  "$LOG_TOKEN_GO" "${F_GO_SRC[@]}" :: "$LOG_TOKEN_SH" "${F_SH[@]}"

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

two_absent "B-43" "no token on a command line (env passthrough only, Go AND shell)" \
  "$ARGV_TOKEN_GO" "${F_GO_SRC[@]}" :: "$ARGV_TOKEN_SH" "${F_SH[@]}"

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
identity_defaults="$(identity_scan "${F_IMPL[@]}" 2>/dev/null || true)"
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

# REQ-040.5, one level below the package: no dead FUNCTION either. An unexported function the shipped
# sources mention exactly once is called from nowhere — the caller is a test, so the binary carries
# code that exists to be asserted about. A test may prove a construct ABSENT; it may not keep the
# construct alive to do so.
mapfile -t F_GO_TESTS < <(printf '%s\n' "${F_GO[@]}" | grep -E '_test\.go$')
dead_funcs=""
while IFS= read -r fn; do
  [[ -z $fn ]] && continue
  where="$(grep -lE "^func $fn\(" "${F_GO_SRC[@]}" 2>/dev/null | head -n1)"
  by=""
  [[ ${#F_GO_TESTS[@]} -gt 0 ]] &&
    by="$(grep -lE "\b$fn\b" "${F_GO_TESTS[@]}" 2>/dev/null | head -n2 | tr '\n' ' ')"
  dead_funcs+="$fn (${where:-?}) is reached from no shipped code${by:+ — only from $by}"$'\n'
done < <(dead_shipped_funcs "${F_GO_SRC[@]}")
if [[ -z $dead_funcs ]]; then
  pass "REQ-040h" "no dead function in the shipped backend (a test may prove absence, not sustain it)"
else
  fail "REQ-040h" "no dead function in the shipped backend (a test may prove absence, not sustain it)" "${dead_funcs%$'\n'}"
fi

# The pinned per-user wrapper derives the boundary it confines to (<state root>/workspaces/<user>)
# from DEVLAB_STATE_DIR / DEVLAB_WORKSPACES. sudo's env_reset is what makes those seams harmless, so
# the env_keep allow-list IS the boundary: a service that could set them would choose the boundary it
# is measured against. Both files say so in prose — this is the check that makes the sentence binding.
sudoers_files=()
for f in "${F_EVERY[@]}"; do
  [[ $f == deploy/*.sudoers ]] && sudoers_files+=("$f")
done
keep_out=""
if gate_files "sudo-a" "no boundary variable survives sudo (env_keep is an allow-list)" "${sudoers_files[@]}"; then
  saw_keep=0
  for f in "${sudoers_files[@]}"; do
    grep -qE '^[[:space:]]*[^#]*env_keep' "$f" && saw_keep=1
    while IFS= read -r stray; do
      [[ -z $stray ]] && continue
      keep_out+="$f keeps $stray across sudo — only [$ENV_KEEP_ALLOWED] may survive env_reset"$'\n'
    done < <(env_keep_strays "$f")
  done
  if [[ $saw_keep -eq 0 ]]; then
    keep_out+="no env_keep directive exists in any sudoers template — the check would pass on nothing"$'\n'
  fi
  if [[ -z $keep_out ]]; then
    pass "sudo-a" "no boundary variable survives sudo (env_keep is an allow-list)"
  else
    fail "sudo-a" "no boundary variable survives sudo (env_keep is an allow-list)" "${keep_out%$'\n'}"
  fi
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

# ── contract discipline (BAUPLAN §0.2 / §4) ──────────────────────────────────────────────────
#
# The contract files are frozen after Welle 0, and every later change to one of them must be recorded
# in deploy/migration/80-vertragsaenderungen.md with its file, its reason and a diff hint. The rule
# was written into that document and then broken five times in ONE commit — a new mutating route, the
# wire types, the data seam, the MCP tool table and the parity artefact all moved unrecorded — so it
# is measured here instead of trusted.
#
# The frozen list AND the baseline commit are read out of the protocol's own header, so there is one
# home for both and no second list to drift. An entry counts when the protocol's BODY (everything
# below the header's rule) names the changed path AND the file's current content fingerprint — the
# header names every frozen path by definition, which is why the search starts beneath it, and the
# fingerprint is what binds the entry to the change that is actually in the tree (contract_state_faults:
# naming the path alone let a second, undescribed change to an already-named file pass unseen).
section "contract discipline"

protocol='deploy/migration/80-vertragsaenderungen.md'
contract_faults=""
baseline=""
frozen_list=()
if [[ ! -f $protocol ]]; then
  contract_faults="$protocol does not exist — the contract-change protocol cannot be read"$'\n'
else
  baseline="$(sed -nE 's/.*Welle 0 = `([0-9a-f]{7,40})`.*/\1/p' "$protocol" | head -n1)"
  mapfile -t frozen_list < <(
    awk '/Frozen were:/ {f = 1} f { if ($0 == "") exit; print }' "$protocol" |
      grep -oE '`[^`]+`' | tr -d '`' | sort -u
  )
  if [[ -z $baseline ]] || ! git cat-file -e "${baseline}^{commit}" 2>/dev/null; then
    contract_faults+="the protocol names no readable Welle-0 baseline commit — nothing to compare the contract against"$'\n'
  fi
  if [[ ${#frozen_list[@]} -lt 12 ]]; then
    contract_faults+="the protocol's frozen list parsed to ${#frozen_list[@]} paths — BAUPLAN §0.2 names 13, so the list is unreadable or was shortened"$'\n'
  fi
fi

if [[ -z $contract_faults ]]; then
  # A trailing /* names the directory; a path that no longer exists cannot be audited at all.
  targets=()
  for p in "${frozen_list[@]}"; do
    p="${p%/\*}"
    if [[ ! -e $p ]]; then
      contract_faults+="the frozen list names $p, which does not exist — a renamed contract file is an unauditable one"$'\n'
      continue
    fi
    targets+=("$p")
  done
  body="$(awk 'f; /^---$/ {f = 1}' "$protocol")"
  # Tracked changes since the baseline AND new files inside a frozen directory: adding a file to the
  # model or to the fixtures changes the contract exactly as editing one does.
  mapfile -t changed_frozen < <(
    {
      git diff --name-only "$baseline" -- "${targets[@]}" 2>/dev/null
      git ls-files --others --exclude-standard -- "${targets[@]}" 2>/dev/null
    } | sort -u | grep .
  )
  if [[ ${#changed_frozen[@]} -gt 0 ]]; then
    while IFS= read -r fault; do
      [[ -z $fault ]] && continue
      contract_faults+="${fault/changed and/changed since $baseline and}"$'\n'
    done < <(contract_state_faults "$body" "${changed_frozen[@]}")
  fi
fi
if [[ -z $contract_faults ]]; then
  pass "contract-a" "every frozen contract file changed since Welle 0 is recorded in the protocol"
else
  fail "contract-a" "every frozen contract file changed since Welle 0 is recorded in the protocol" "${contract_faults%$'\n'}"
fi

# The live topics are frozen as CONSTANTS inside a file whose broker was meant to be filled, so the
# constant set is what is compared — renaming or re-valuing a topic breaks every subscriber on the
# other side of the wire and needs the same protocol entry as any other contract change.
topic_faults=""
topic_now="$(sed -nE 's/^[[:space:]]*(Topic[A-Za-z]+)[[:space:]]+Topic[[:space:]]*=[[:space:]]*"([^"]+)".*/\1=\2/p' \
  backend/internal/live/live.go 2>/dev/null | sort)"
topic_base=""
if [[ -n $baseline ]]; then
  topic_base="$(git show "$baseline:backend/internal/live/live.go" 2>/dev/null |
    sed -nE 's/^[[:space:]]*(Topic[A-Za-z]+)[[:space:]]+Topic[[:space:]]*=[[:space:]]*"([^"]+)".*/\1=\2/p' | sort)"
fi
if [[ $(grep -c . <<<"$topic_now") -lt 5 ]]; then
  topic_faults="the live topic constants could not be read from backend/internal/live/live.go — the comparison would pass on nothing"
elif [[ $topic_now != "$topic_base" ]]; then
  topic_faults="$(diff <(printf '%s\n' "$topic_base") <(printf '%s\n' "$topic_now") || true)"
  grep -qF -- 'backend/internal/live/live.go' <<<"$(awk 'f; /^---$/ {f = 1}' "$protocol")" && topic_faults=""
fi
if [[ -z $topic_faults ]]; then
  pass "contract-b" "the live topic constants are the ones Welle 0 fixed (or the change is recorded)"
else
  fail "contract-b" "the live topic constants are the ones Welle 0 fixed (or the change is recorded)" "$topic_faults"
fi

# ── the delivery's own statements about itself ───────────────────────────────────────────────
#
# deploy/migration/20-sichtpruefung.md carries the STATE of every acceptance line and the size of
# every inspection section. Both are claims about this repository, and a claim in the delivery is
# worth no more than a claim in the code: it gets measured. The document once cited a check id that
# does not exist (`REQ-012`) and named section sizes that were three items short — a reader would
# have inspected less than the matrix demands and called the line passed.
inspection='deploy/migration/20-sichtpruefung.md'
doc_faults=""
if [[ ! -f $inspection ]]; then
  doc_faults="$inspection does not exist — the inspection half of the acceptance cannot be audited"$'\n'
else
  # Every check id the document cites in "Grep green (…)" must be an id this script actually runs.
  script_ids="$(
    grep -oE '^[[:space:]]*(pass|fail|absent|absent_raw|two_absent|present|present_anywhere|unique_in|count_files|no_file|git_absent|gate_files|note)[[:space:]]+"[^"]+"' \
      "${BASH_SOURCE[0]}" | grep -oE '"[^"]+"' | tr -d '"' | sort -u
  )"
  if [[ $(grep -c . <<<"$script_ids") -lt 20 ]]; then
    doc_faults+="the script's own check ids could not be read — the comparison would pass on nothing"$'\n'
  else
    cited="$(grep -oE 'Grep green \([^)]*\)' "$inspection" | sed -E 's/Grep green \(|\)//g' | tr ',' '\n' | tr -d ' ' | sort -u | grep .)"
    [[ -n $cited ]] || doc_faults+="the document cites no check id at all — its \"Grep green\" claims name nothing"$'\n'
    while IFS= read -r id; do
      [[ -z $id ]] && continue
      grep -qxF -- "$id" <<<"$script_ids" ||
        doc_faults+="$inspection cites check $id, which this script does not run — a status resting on a check nobody performs"$'\n'
    done <<<"$cited"
  fi

  # Every section size the result table claims must be the number of items that section carries.
  declare -A doc_items=()
  sec=""
  while IFS= read -r line; do
    if [[ $line =~ ^##[[:space:]]+([0-9]+)\. ]]; then
      sec="${BASH_REMATCH[1]}"
      doc_items[$sec]=0
      continue
    fi
    [[ -n $sec && $line == "- [ ]"* ]] && doc_items[$sec]=$((doc_items[$sec] + 1))
  done <"$inspection"
  if [[ ${#doc_items[@]} -lt 5 ]]; then
    doc_faults+="the document's inspection sections could not be read — the size comparison would pass on nothing"$'\n'
  fi
  while IFS= read -r row; do
    [[ $row =~ ^\|[[:space:]]*([0-9]+)[[:space:]][^|]*\|[[:space:]]*([0-9]+)[[:space:]]*\| ]] || continue
    s="${BASH_REMATCH[1]}"
    claimed="${BASH_REMATCH[2]}"
    actual="${doc_items[$s]:-}"
    if [[ -z $actual ]]; then
      doc_faults+="the result table counts a section $s that the document does not contain"$'\n'
    elif [[ $claimed != "$actual" ]]; then
      doc_faults+="the result table claims $claimed items for section $s, which carries $actual — the inspection would be reported complete after doing less"$'\n'
    fi
  done <"$inspection"
fi
if [[ -z $doc_faults ]]; then
  pass "doc-a" "the inspection document cites only checks that exist, and counts its own items correctly"
else
  fail "doc-a" "the inspection document cites only checks that exist, and counts its own items correctly" "${doc_faults%$'\n'}"
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
