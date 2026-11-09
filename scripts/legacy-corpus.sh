#!/usr/bin/env bash
# scripts/legacy-corpus.sh -- build a corpus of real on-disk ethos state
# written by released binaries, for the automatic-repair tests (ethos-ftey).
#
# For each released tag the script exports the source with `git archive`
# (the same approach plugin/hooks/shim_commit_test.go uses for 93c9f82 and
# v4.8.0), builds ./cmd/ethos with the toolchain that tag's go.mod names,
# runs one fixed scenario through the binary's own CLI and hooks in an
# isolated HOME and a fresh scratch git repository, and snapshots the result
# under internal/autorepair/testdata/corpus/<tag>/ with the scratch paths
# replaced by @REPO@ and @HOME@. It also writes the hand-shaped damage
# fixtures under internal/autorepair/testdata/damage/ and the README.
#
# Usage: scripts/legacy-corpus.sh [--out DIR] [--work DIR] [--versions "v4.22.1 v4.21.0"]
#                                 [--no-damage] [--no-readme]
#   --out DIR       testdata root (default internal/autorepair/testdata)
#   --work DIR      scratch root for builds and scenario runs (default $TMPDIR or <repo>/.tmp, then /legacy-corpus)
#   --versions STR  space-separated tags (default: the nine in TAGS below)
#
# Operational notes:
#   - Needs full tag history (git fetch --tags); a missing tag is reported and skipped.
#   - Every scenario command runs under `env -i` with HOME pointing into the scratch
#     root, so nothing can reach the real ~/.punt-labs or this repository's .punt-labs.
#   - Exits non-zero when any requested tag failed to build, run, or pass the
#     privacy scan; the successful tags are still written.
#   - Bash 3.2 compatible (macOS /bin/bash) -- no associative arrays or mapfile.
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$SCRIPT_DIR/.." && pwd)

# One tag per layout change (plan section 8).
TAGS="v3.11.0 v3.12.0 v4.0.0 v4.8.0 v4.13.0 v4.17.0 v4.20.4 v4.21.0 v4.22.1"

# The only secret-shaped string in the corpus. It is not a real credential: the
# body is a repeated word, and it is split here so this file is not itself a
# secret-shaped literal.
FAKE_TOKEN="ghp_""FAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKE"

OUT=$REPO_ROOT/internal/autorepair/testdata
WORK=""
VERSIONS=$TAGS
DO_DAMAGE=1
DO_README=1

die() { echo "legacy-corpus: $*" >&2; exit 1; }
note() { echo "legacy-corpus: $*" >&2; }

while [ $# -gt 0 ]; do
  case $1 in
    --out) [ $# -ge 2 ] || die "--out needs a directory"; OUT=$2; shift 2 ;;
    --work) [ $# -ge 2 ] || die "--work needs a directory"; WORK=$2; shift 2 ;;
    --versions) [ $# -ge 2 ] || die "--versions needs a list"; VERSIONS=$2; shift 2 ;;
    --no-damage) DO_DAMAGE=0; shift ;;
    --no-readme) DO_README=0; shift ;;
    -h|--help) sed -n '2,/^set -euo/p' "$0" | sed '$d'; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

if [ -z "$WORK" ]; then
  WORK=${TMPDIR:-$REPO_ROOT/.tmp}
  WORK=${WORK%/}/legacy-corpus
fi
mkdir -p "$WORK" "$OUT"
WORK=$(cd "$WORK" && pwd -P)
OUT=$(cd "$OUT" && pwd -P)
REAL_HOME=${HOME:-}
case $WORK in
  "$REAL_HOME"/.punt-labs*|"$REPO_ROOT"/.punt-labs*) die "work dir $WORK is inside a real .punt-labs tree" ;;
esac

command -v perl >/dev/null || die "perl is required"
command -v git >/dev/null || die "git is required"
command -v go >/dev/null || die "go is required"
GIT_DIR_BIN=$(dirname "$(command -v git)")
SAFEPATH=/usr/bin:/bin:$GIT_DIR_BIN

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

# purge_dir removes a directory this script created, and only under $WORK or $OUT.
purge_dir() {
  local d=$1
  [ -n "$d" ] || die "purge_dir: empty path"
  [ -e "$d" ] || return 0
  case $d in
    "$WORK"/*|"$OUT"/*) ;;
    *) die "purge_dir: refusing to remove $d (outside $WORK and $OUT)" ;;
  esac
  rm -rf "$d"
}

FAILED=""
mark_failed() { FAILED="$FAILED $1"; }

# ---------------------------------------------------------------- build

# build_tag builds the tag's binary into $WORK/bin/<tag>/ethos. It records the
# commit and the toolchain beside it. Returns 1 (after printing the error) when
# the tag cannot be built.
build_tag() {
  local tag=$1 sha src tc bin log
  bin=$WORK/bin/$tag
  mkdir -p "$bin"
  log=$bin/build.log
  if ! sha=$(git -C "$REPO_ROOT" rev-parse --verify -q "$tag^{commit}"); then
    note "$tag: tag not found (a shallow clone lacks it?)"
    return 1
  fi
  if [ -x "$bin/ethos" ] && [ "$(cat "$bin/sha" 2>/dev/null || true)" = "$sha" ]; then
    return 0
  fi
  src=$WORK/src/$tag
  purge_dir "$src"
  mkdir -p "$src"
  if ! git -C "$REPO_ROOT" archive "$tag" | tar -x -C "$src"; then
    note "$tag: git archive failed"
    return 1
  fi
  tc=$(awk '$1=="toolchain"{print $2}' "$src/go.mod")
  [ -n "$tc" ] || tc=go$(awk '$1=="go"{print $2}' "$src/go.mod")
  echo "$tc" >"$bin/toolchain"
  if ! ( cd "$src" && GOTOOLCHAIN=$tc CGO_ENABLED=0 go build -ldflags "-X main.version=${tag#v}" -o "$bin/ethos" ./cmd/ethos ) >"$log" 2>&1; then
    note "$tag: build failed with $tc:"
    sed 's/^/    /' "$log" >&2
    return 1
  fi
  ( cd "$src" && GOTOOLCHAIN=$tc go version ) >"$bin/go-version" 2>&1 || true
  echo "$sha" >"$bin/sha"
  purge_dir "$src"
  return 0
}

# ------------------------------------------------------------- scenario

# State shared by the scenario helpers (one version at a time).
W="" BIN="" REPO="" HOME_DIR="" LOG="" STEPS="" FAKE_PID="" SESSION_ID="sess-corpus-0001"

# eth runs the binary in the scratch repo with a clean environment.
eth() {
  ( cd "$REPO" && env -i HOME="$HOME_DIR" PATH="$(dirname "$BIN"):$SAFEPATH" TMPDIR="$W/tmp" \
      GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null \
      GIT_AUTHOR_NAME="Test Person" GIT_AUTHOR_EMAIL=person@example.com \
      GIT_COMMITTER_NAME="Test Person" GIT_COMMITTER_EMAIL=person@example.com \
      CLAUDE_PID="$FAKE_PID" ETHOS_SESSION="$SESSION_ID" ETHOS_QUIET_ADVICE=1 \
      "$BIN" "$@" )
}

# gitc runs git in the scratch repo with the same clean environment, so the
# installed hooks find the version's own binary first on PATH.
gitc() {
  ( cd "$REPO" && env -i HOME="$HOME_DIR" PATH="$(dirname "$BIN"):$SAFEPATH" TMPDIR="$W/tmp" \
      GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null \
      GIT_AUTHOR_NAME="Test Person" GIT_AUTHOR_EMAIL=person@example.com \
      GIT_COMMITTER_NAME="Test Person" GIT_COMMITTER_EMAIL=person@example.com \
      CLAUDE_PID="$FAKE_PID" ETHOS_SESSION="$SESSION_ID" \
      git "$@" )
}

record() { printf '%s\t%s\n' "$1" "$2" >>"$STEPS"; }

# step NAME CMD... runs a scenario command, logs its output, records ok or rc=N.
step() {
  local name=$1 rc
  shift
  { echo "### $name: $*"; } >>"$LOG"
  set +e
  "$@" >>"$LOG" 2>&1
  rc=$?
  set -e
  if [ $rc -eq 0 ]; then record "$name" ok; else record "$name" "failed rc=$rc (see scenario log)"; fi
  return 0
}

skip() { record "$1" "skipped: $2"; echo "### $1: skipped: $2" >>"$LOG"; }

# help_of PATH... prints the help text of a command path.
help_of() { eth "$@" --help 2>&1 || true; }

# has_sub PARENT SUB: does `ethos PARENT --help` list SUB as a subcommand?
has_sub() {
  local out
  # shellcheck disable=SC2086 # $1 is a command path such as "mission create": the words must split.
  if [ -z "$1" ]; then out=$(eth --help 2>&1 || true); else out=$(eth $1 --help 2>&1 || true); fi
  printf '%s\n' "$out" | grep -Eq "^  $2( |$)"
}

# has_flag PATH FLAG: does `ethos PATH --help` mention FLAG?
has_flag() {
  local out
  # shellcheck disable=SC2086 # $1 is a command path such as "ext set": the words must split.
  out=$(eth $1 --help 2>&1 || true)
  printf '%s\n' "$out" | grep -q -- "$2"
}

# hook NAME JSON feeds JSON to `ethos hook NAME` on stdin.
hook() {
  local name=$1 json=$2
  printf '%s' "$json" | eth hook "$name"
}

write_inputs() {
  mkdir -p "$W/in"
  cat >"$W/in/setup.yaml" <<EOF
name: Test Person
handle: tperson
email: person@example.com
github: tperson
solo: true
EOF
  cat >"$W/in/worker.yaml" <<EOF
name: Worker Person
handle: wperson
kind: agent
personality: principal-engineer
writing_style: concise-quantified
talents:
  - go
EOF
  cat >"$W/in/evaluator.yaml" <<EOF
name: Eval Person
handle: eperson
kind: agent
personality: principal-engineer
writing_style: concise-quantified
talents:
  - go
EOF
  # A top-level voice field: pre-v2.0 identities carried one. A version that
  # does not know the field drops it on write; that is recorded, not faked.
  cat >"$W/in/voice.yaml" <<EOF
name: Voice Person
handle: vperson
kind: human
email: voice@example.com
voice:
  provider: say
  voice: Samantha
EOF
  cat >"$W/in/team.yaml" <<EOF
name: corpus
repositories: []
members:
  - identity: tperson
    role: architect
  - identity: wperson
    role: implementer
  - identity: eperson
    role: reviewer
EOF
  # inputs.bead is the pre-DES-049 spelling of inputs.ticket.
  cat >"$W/in/contract-bead.yaml" <<EOF
leader: claude
worker: wperson
evaluator:
  handle: eperson
write_set:
  - src/
success_criteria:
  - "tests pass"
budget:
  rounds: 2
  reflection_after_each: true
inputs:
  bead: ethos-0001
context: "corpus scenario, mission created from a contract file"
EOF
  sed_free_ticket "$W/in/contract-bead.yaml" "$W/in/contract-ticket.yaml"
  cat >"$W/in/result-1.yaml" <<EOF
mission: MISSION
round: 1
author: wperson
verdict: pass
confidence: 0.9
files_changed:
  - path: src/app.go
    added: 10
    removed: 2
evidence:
  - name: make check
    status: pass
EOF
  cat >"$W/in/result-2.yaml" <<EOF
mission: MISSION
round: 2
author: wperson
verdict: pass
confidence: 0.95
files_changed:
  - path: src/app.go
    added: 3
    removed: 1
evidence:
  - name: make check
    status: pass
EOF
  cat >"$W/in/reflection-1.yaml" <<EOF
mission: MISSION
round: 1
author: claude
converging: true
signals:
  - "tests passing"
recommendation: continue
reason: "round 1 finished cleanly"
EOF
}

# sed_free_ticket copies a contract, spelling inputs.bead as inputs.ticket.
sed_free_ticket() {
  perl -pe 's/^  bead:/  ticket:/' "$1" >"$2"
}

# fill_mission copies a result template with the mission id substituted.
fill_mission() {
  perl -pe "s/^mission: MISSION/mission: $3/" "$1" >"$2"
}

# first_mission_id prints the first m-YYYY-MM-DD-NNN found in the input.
first_mission_id() {
  grep -Eo 'm-[0-9]{4}-[0-9]{2}-[0-9]{2}-[0-9]{3}' | head -n 1
}

run_scenario() {
  local tag=$1 out mid1 mid2 idfile
  write_inputs
  : >"$STEPS"

  step "git init" gitc init -q -b main
  step "seed" eth seed
  # Remember what seed deployed so the snapshot can omit it (it is a function of
  # the binary, not legacy state).
  ( cd "$HOME_DIR" && find .punt-labs/ethos -type f 2>/dev/null | LC_ALL=C sort ) >"$W/seed.files" || true
  while IFS= read -r f; do
    printf '%s\t%s\n' "$(sha256 "$HOME_DIR/$f")" "$f"
  done <"$W/seed.files" >"$W/seed.sha"

  # Older seeds deploy no personality or writing style, and an identity that names
  # one needs it on disk. Create the two the scenario uses where seed left them out.
  printf 'Direct and evidence-driven.\n' >"$W/in/personality.md"
  printf 'Short sentences. Lead with the answer.\n' >"$W/in/writing-style.md"
  if ! eth personality show principal-engineer >/dev/null 2>&1; then
    step "personality create" eth personality create principal-engineer -f "$W/in/personality.md"
  fi
  if ! eth writing-style show concise-quantified >/dev/null 2>&1; then
    step "writing-style create" eth writing-style create concise-quantified -f "$W/in/writing-style.md"
  fi

  if has_flag "setup" "--file"; then
    step "setup --file (solo)" eth setup --file "$W/in/setup.yaml"
  else
    skip "setup --file (solo)" "this version has no setup --file"
  fi
  step "identity create worker" eth identity create -f "$W/in/worker.yaml"
  step "identity create evaluator" eth identity create -f "$W/in/evaluator.yaml"
  step "identity create voice" eth identity create -f "$W/in/voice.yaml"
  idfile=$(find "$HOME_DIR/.punt-labs/ethos/identities" "$REPO/.punt-labs/ethos/identities" -name vperson.yaml 2>/dev/null | head -n 1 || true)
  if [ -n "$idfile" ] && grep -q '^voice:' "$idfile"; then
    record "voice field" "kept in the identity file"
  else
    record "voice field" "skipped: this version moves a top-level voice field into the vox extension on write, so no top-level voice: reaches disk (see damage/top-level-voice-field)"
  fi
  if has_sub "" ext; then
    step "ext set (base)" eth ext set tperson vox voice test-voice
    if has_flag "ext set" "--local"; then
      step "ext set --local (overlay)" eth ext set --local tperson vox notify y
    else
      skip "ext set --local (overlay)" "this version has no local extension overlay"
    fi
  else
    skip "ext set (base)" "this version has no ext command"
    skip "ext set --local (overlay)" "this version has no ext command"
  fi
  if has_sub "team" create || has_sub "" team; then
    step "team create" eth team create corpus -f "$W/in/team.yaml"
  else
    skip "team create" "this version has no team create"
  fi
  if has_sub "" enable; then
    step "enable" eth enable
  else
    skip "enable" "this version has no enable command (no hook install)"
  fi

  step "session-start hook" hook session-start "{\"session_id\":\"$SESSION_ID\",\"cwd\":\"$REPO\",\"hook_event_name\":\"SessionStart\",\"source\":\"startup\"}"

  # Mission 1: from a contract file, carried through the whole lifecycle.
  if has_flag "mission create" "--file"; then
    if eth mission create -f "$W/in/contract-bead.yaml" >"$W/m1.out" 2>&1; then
      mid1=$(first_mission_id <"$W/m1.out" || true)
      record "mission create (inputs.bead)" "ok"
    elif eth mission create -f "$W/in/contract-ticket.yaml" >"$W/m1.out" 2>&1; then
      mid1=$(first_mission_id <"$W/m1.out" || true)
      record "mission create (inputs.bead)" "skipped: this version rejects inputs.bead; created with inputs.ticket instead"
    else
      mid1=""
      record "mission create (inputs.bead)" "failed (see scenario log)"
    fi
    { echo "### mission create"; cat "$W/m1.out"; } >>"$LOG"
  else
    mid1=""
    skip "mission create (inputs.bead)" "this version has no mission create"
  fi
  # Mission 2: from flags, left open for the delegation.
  if has_flag "mission dispatch" "--write-set"; then
    if eth mission dispatch --worker wperson --evaluator eperson --write-set docs/ \
        --criteria "docs updated" --ticket ethos-0002 --context "corpus scenario, mission from flags" >"$W/m2.out" 2>&1; then
      mid2=$(first_mission_id <"$W/m2.out" || true)
      record "mission dispatch" ok
    else
      mid2=""
      record "mission dispatch" "failed (see scenario log)"
    fi
    { echo "### mission dispatch"; cat "$W/m2.out"; } >>"$LOG"
  else
    mid2=""
    skip "mission dispatch" "this version has no mission dispatch"
  fi

  if [ -n "$mid1" ]; then
    fill_mission "$W/in/result-1.yaml" "$W/in/result-1.filled.yaml" "$mid1"
    fill_mission "$W/in/result-2.yaml" "$W/in/result-2.filled.yaml" "$mid1"
    step "mission result r1" eth mission result "$mid1" --file "$W/in/result-1.filled.yaml"
    # The reflection's mission field exists only in newer versions; an older
    # strict decoder rejects it, so the plain form is tried first.
    fill_mission "$W/in/reflection-1.yaml" "$W/in/reflection-1.filled.yaml" "$mid1"
    perl -ne 'print unless /^mission:/' "$W/in/reflection-1.yaml" >"$W/in/reflection-1.plain.yaml"
    if eth mission reflect "$mid1" --file "$W/in/reflection-1.plain.yaml" >/dev/null 2>&1; then
      record "mission reflect r1" ok
      echo "### mission reflect r1: accepted without a mission field" >>"$LOG"
    else
      step "mission reflect r1" eth mission reflect "$mid1" --file "$W/in/reflection-1.filled.yaml"
    fi
    step "mission advance" eth mission advance "$mid1"
    step "mission result r2" eth mission result "$mid1" --file "$W/in/result-2.filled.yaml"
    step "mission close" eth mission close "$mid1"
  else
    for s in "mission result r1" "mission reflect r1" "mission advance" "mission result r2" "mission close"; do
      skip "$s" "no mission was created"
    done
  fi

  # Delegation: a PreToolUse Agent payload. Where the version can claim a
  # mission the spawn is governed; otherwise it is an ad-hoc spawn.
  if [ -n "$mid2" ] && has_sub "mission" claim; then
    step "mission claim" eth mission claim "$mid2"
  else
    skip "mission claim" "this version has no mission claim, or no mission was dispatched"
  fi
  step "delegation (pre-tool-use Agent)" hook pre-tool-use \
    "{\"session_id\":\"$SESSION_ID\",\"cwd\":\"$REPO\",\"hook_event_name\":\"PreToolUse\",\"tool_name\":\"Agent\",\"tool_input\":{\"subagent_type\":\"wperson\",\"description\":\"corpus delegation\",\"prompt\":\"work on ${mid2:-the task}\"}}"

  if has_flag "adr create" "--decision"; then
    step "adr create" eth adr create --title "Use a corpus" --decision "Build it from released binaries" \
      --context "tests for the upgrader" --author tperson
  else
    skip "adr create" "this version has no adr create"
  fi

  local i
  for i in 1 2 3; do
    step "audit-log hook $i" hook audit-log \
      "{\"session_id\":\"$SESSION_ID\",\"cwd\":\"$REPO\",\"hook_event_name\":\"PostToolUse\",\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"cat $HOME_DIR/.netrc; echo $FAKE_TOKEN $i\"},\"tool_response\":{\"stdout\":\"ok\"}}"
  done

  step "git add" gitc add -A
  step "git commit (installed hooks)" gitc -c commit.gpgsign=false commit -q -m "corpus: scenario commit"
  out=$(gitc log -1 --format=%B 2>/dev/null || true)
  printf '%s\n' "$out" >"$W/commit-msg.txt"
  gitc ls-files -z >"$W/tracked.z" 2>/dev/null || : >"$W/tracked.z"
  : "$tag"
}

# ------------------------------------------------------------- snapshot

# subs_for_run prints the tab-separated substitution table for this run, longest
# paths first. Entries starting with "re:" are regular expressions.
subs_for_run() {
  local repo_p home_p
  repo_p=$(cd "$REPO" && pwd -P)
  home_p=$(cd "$HOME_DIR" && pwd -P)
  printf '%s\t%s\n' "$repo_p" "@REPO@" "$REPO" "@REPO@" "$home_p" "@HOME@" "$HOME_DIR" "@HOME@"
  printf '%s\t%s\n' "re:(?<![0-9A-Za-z])$FAKE_PID(?![0-9A-Za-z])" "@PID@"
  local host short user
  host=$(hostname 2>/dev/null || true)
  short=$(hostname -s 2>/dev/null || true)
  user=$(id -un 2>/dev/null || true)
  [ -n "$host" ] && printf '%s\t%s\n' "$host" "@HOST@"
  [ -n "$short" ] && [ "$short" != "$host" ] && printf '%s\t%s\n' "$short" "@HOST@"
  if [ "${#user}" -ge 5 ]; then printf '%s\t%s\n' "re:\\b$user\\b" "@USER@"; fi
  return 0
}

# rewrite_tree DIR applies $SUBS to every text file under DIR.
rewrite_tree() {
  local dir=$1 f
  while IFS= read -r -d '' f; do
    if grep -Iq . "$f" 2>/dev/null; then
      CORPUS_SUBS=$SUBS perl -pi -e '
        BEGIN { for my $l (split /\n/, $ENV{CORPUS_SUBS}) { my ($f, $t) = split /\t/, $l, 2; push @s, [$f, $t]; } }
        for my $p (@s) { my ($f, $t) = @$p; if ($f =~ s/^re://) { s/$f/$t/g } else { s/\Q$f\E/$t/g } }
      ' "$f"
    fi
  done < <(find "$dir" \( -type f -o -type l \) -print0)
}

# copy_files SRC DST LISTFILE copies each relative path in LISTFILE.
copy_files() {
  local src=$1 dst=$2 list=$3 f
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    mkdir -p "$dst/$(dirname "$f")"
    cp -pP "$src/$f" "$dst/$f"
  done <"$list"
}

snapshot() {
  local tag=$1 f dest stage
  dest=$OUT/corpus/$tag
  stage=$W/stage
  purge_dir "$stage"
  mkdir -p "$stage/repo" "$stage/home" "$stage/git/hooks"

  # repo/: everything but .git and the path-hashed lock files.
  ( cd "$REPO" && find . \( -path ./.git -o -path ./.punt-labs/local/ethos/locks \) -prune -o \( -type f -o -type l \) -print | LC_ALL=C sort | perl -pe 's{^\./}{}' ) >"$W/repo.files"
  copy_files "$REPO" "$stage/repo" "$W/repo.files"

  # home/: only .punt-labs/ethos, minus files seed deployed and the scenario left alone.
  ( cd "$HOME_DIR" && find .punt-labs/ethos \( -type f -o -type l \) -print 2>/dev/null | LC_ALL=C sort ) >"$W/home.files"
  : >"$W/home.keep"
  : >"$W/seed-omitted.txt"
  while IFS= read -r f; do
    if grep -qF "$(printf '%s\t%s' "$(sha256 "$HOME_DIR/$f")" "$f")" "$W/seed.sha"; then
      echo "$f" >>"$W/seed-omitted.txt"
    else
      echo "$f" >>"$W/home.keep"
    fi
  done <"$W/home.files"
  copy_files "$HOME_DIR" "$stage/home" "$W/home.keep"

  SUBS=$(subs_for_run)
  rewrite_tree "$stage/repo"
  rewrite_tree "$stage/home"
  # A file named for the fake PID (sessions/current/<pid>) gets the placeholder.
  while IFS= read -r -d '' f; do
    mv "$f" "$(dirname "$f")/@PID@"
  done < <(find "$stage" -name "$FAKE_PID" -print0)

  # git/: the original .git cannot be committed under testdata, and its objects
  # hold the scratch paths. Re-materialize the repository from the rewritten
  # tree: the same tracked path set, the original commit message, a fixed date.
  local msg=$W/commit-msg.txt have_commit=0
  if [ -s "$W/tracked.z" ] && gitc rev-parse --verify -q HEAD >/dev/null 2>&1; then have_commit=1; fi
  if [ "$have_commit" -eq 1 ]; then
    local mat=$W/materialize
    purge_dir "$mat"
    mkdir -p "$mat"
    cp -pR "$stage/repo/." "$mat/"
    # shellcheck disable=SC2016 # single-quoted on purpose: the inner sh expands $1..$5.
    ( cd "$mat" && env -i HOME="$W/tmp" PATH="$SAFEPATH" GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null \
        GIT_AUTHOR_NAME="Test Person" GIT_AUTHOR_EMAIL=person@example.com GIT_AUTHOR_DATE="2000-01-01T00:00:00Z" \
        GIT_COMMITTER_NAME="Test Person" GIT_COMMITTER_EMAIL=person@example.com GIT_COMMITTER_DATE="2000-01-01T00:00:00Z" \
        sh -c '
          git init -q -b main &&
          xargs -0 git add -f -- <"$1" 2>/dev/null &&
          git -c commit.gpgsign=false commit -q --no-verify -F "$2" --allow-empty &&
          git bundle create "$3" --all >/dev/null 2>&1 &&
          cp .git/index "$4" &&
          cp .git/HEAD "$5"
        ' sh "$W/tracked.z" "$msg" "$stage/git/repo.bundle" "$stage/git/index" "$stage/git/HEAD" ) \
      || note "$tag: could not re-materialize the git state"
    # The git hooks the version's enable installed (rewritten like everything else).
    if [ -d "$REPO/.git/hooks" ]; then
      for f in "$REPO"/.git/hooks/*; do
        case $f in *.sample) continue ;; esac
        [ -f "$f" ] && cp -p "$f" "$stage/git/hooks/$(basename "$f")"
      done
      rewrite_tree "$stage/git/hooks"
    fi
  fi
  # The tracked path set in plain text, so a test can rebuild an index without git.
  if [ -s "$W/tracked.z" ]; then tr '\0' '\n' <"$W/tracked.z" >"$stage/git/tracked-files.txt"; fi
  cp "$W/seed-omitted.txt" "$stage/seed-omitted.txt"
  rewrite_tree "$stage/git"

  privacy_scan "$tag" "$stage" || { purge_dir "$stage"; return 1; }

  write_provenance "$tag" "$stage/PROVENANCE.txt"
  purge_dir "$dest"
  mkdir -p "$(dirname "$dest")"
  mv "$stage" "$dest"
  return 0
}

# privacy_scan DIR fails when a real home path, user name, hostname or non-example
# email survives in DIR.
privacy_scan() {
  local tag=$1 dir=$2 bad=0 hits user
  user=$(id -un 2>/dev/null || true)
  hits=$(grep -rIlE '/(Users|home)/[A-Za-z][A-Za-z0-9._-]*' "$dir" 2>/dev/null || true)
  if [ -n "$hits" ]; then note "$tag: privacy scan: absolute home path in: $hits"; bad=1; fi
  if [ -n "$REAL_HOME" ]; then
    hits=$(grep -rIlF "$REAL_HOME" "$dir" 2>/dev/null || true)
    if [ -n "$hits" ]; then note "$tag: privacy scan: real HOME in: $hits"; bad=1; fi
  fi
  if [ "${#user}" -ge 5 ]; then
    hits=$(grep -rIlw "$user" "$dir" 2>/dev/null || true)
    if [ -n "$hits" ]; then note "$tag: privacy scan: user name in: $hits"; bad=1; fi
  fi
  hits=$(grep -rIhoE '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}' "$dir" 2>/dev/null | LC_ALL=C sort -u | grep -Ev '@example\.com$' || true)
  if [ -n "$hits" ]; then note "$tag: privacy scan: non-example email(s): $(echo "$hits" | tr '\n' ' ')"; bad=1; fi
  hits=$(grep -rIlE '(gh[pousr]_[A-Za-z0-9]{36,}|AKIA[0-9A-Z]{16}|BEGIN [A-Z ]*PRIVATE KEY)' "$dir" 2>/dev/null | while IFS= read -r f; do
    if grep -E 'gh[pousr]_[A-Za-z0-9]{36,}' "$f" | grep -qv "$FAKE_TOKEN"; then echo "$f"; fi
  done || true)
  if [ -n "$hits" ]; then note "$tag: privacy scan: secret-shaped string other than the fake token in: $hits"; bad=1; fi
  return $bad
}

write_provenance() {
  local tag=$1 file=$2
  {
    echo "tag: $tag"
    echo "commit: $(cat "$WORK/bin/$tag/sha")"
    echo "go_toolchain: $(cat "$WORK/bin/$tag/toolchain")"
    echo "go_version: $(cat "$WORK/bin/$tag/go-version" 2>/dev/null || echo unknown)"
    echo "ethos_version: $(eth version 2>&1 | head -n 1)"
    echo "steps:"
    while IFS="$(printf '\t')" read -r name status; do
      printf '  %s: %s\n' "$name" "$status"
    done <"$STEPS"
  } >"$file"
}

# ------------------------------------------------------------ per version

do_version() {
  local tag=$1
  if ! build_tag "$tag"; then mark_failed "$tag(build)"; return 0; fi
  W=$WORK/run/$tag
  purge_dir "$W"
  mkdir -p "$W/tmp" "$W/home" "$W/repo"
  BIN=$WORK/bin/$tag/ethos
  REPO=$(cd "$W/repo" && pwd)
  HOME_DIR=$(cd "$W/home" && pwd)
  LOG=$W/scenario.log
  STEPS=$W/steps.tsv
  FAKE_PID=$$
  : >"$LOG"
  note "$tag: running the scenario in $W"
  if ! run_scenario "$tag"; then mark_failed "$tag(scenario)"; return 0; fi
  if ! snapshot "$tag"; then mark_failed "$tag(privacy)"; return 0; fi
  note "$tag: snapshot written to $OUT/corpus/$tag"
}

for tag in $VERSIONS; do
  do_version "$tag"
done

# ------------------------------------------------------------------ damage

# shellcheck source=scripts/legacy-corpus-damage.sh
# (damage fixtures and README generation are defined below)

write_damage() {
  local d=$OUT/damage
  purge_dir "$d"
  mkdir -p "$d"
  # Placeholder session and checkout ids used by every fixture.
  local sid=sess-damage-0001 day=2026-11-04
  local arc=".punt-labs/local/ethos/trace/sessions/$day-$sid"
  local live=".punt-labs/local/ethos/sessions/$sid.audit.jsonl"
  local good bad

  base_repo() { # DIR: a minimal enabled repository
    mkdir -p "$1/repo/.punt-labs/ethos" "$1/repo/.punt-labs/local/ethos"
    printf 'agent: claude\n' >"$1/repo/.punt-labs/ethos.yaml"
    : >"$1/repo/.punt-labs/ethos/enabled"
    printf '# ethos: shared machine-local zone (never track)\n.punt-labs/local/\n' >"$1/repo/.gitignore"
  }
  readme() { printf '%s\n' "$2" >"$1/README"; }
  # An audit line as a v4.22.x binary writes it.
  line() { # TS TOOL COMMAND
    printf '{"path_format":2,"record":"blame","redacted":true,"session":"%s","tool":"%s","tool_input":{"command":"%s"},"ts":"%s"}\n' "$sid" "$2" "$3" "$1"
  }
  good=$(line 2026-11-04T10:00:00.000001Z Bash "echo one")

  # 1. corrupt sealed chunk
  mkdir -p "$d/corrupt-sealed-chunk"; base_repo "$d/corrupt-sealed-chunk"
  mkdir -p "$d/corrupt-sealed-chunk/repo/$arc"
  printf 'this is not json\n{"ts":\n' >"$d/corrupt-sealed-chunk/repo/$arc/audit-1793786400000001000-1793786400000002000.jsonl"
  readme "$d/corrupt-sealed-chunk" "A sealed audit chunk whose bytes are not decodable JSON lines; no surviving chunk covers it (the audit-quarantine case)."

  # 2. invalid backslash escapes (ethos-jn25)
  mkdir -p "$d/invalid-escape-lines"; base_repo "$d/invalid-escape-lines"
  mkdir -p "$d/invalid-escape-lines/repo/$arc"
  {
    echo "$good"
    printf '{"session":"%s","tool":"Bash","tool_input":{"command":"cat \\[REDACTED:path]/notes"},"ts":"2026-11-04T10:00:01.000001Z"}\n' "$sid"
  } >"$d/invalid-escape-lines/repo/$arc/audit-1793786401000001000-1793786401000001000.jsonl"
  readme "$d/invalid-escape-lines" "A tracked chunk with a line carrying an invalid backslash escape from hand redaction, \\[REDACTED:path] (ethos-jn25 shape); the other line is valid."

  # 3. corrupt chunk conflicting with a surviving chunk (ethos-h5z8)
  mkdir -p "$d/conflicting-recovered-line"; base_repo "$d/conflicting-recovered-line"
  mkdir -p "$d/conflicting-recovered-line/repo/$arc"
  echo "$good" >"$d/conflicting-recovered-line/repo/$arc/audit-1793786400000001000-1793786400000001000.jsonl"
  {
    line 2026-11-04T10:00:00.000001Z Bash "echo ONE-DIFFERENT"
    printf 'trailing garbage\n'
  } >"$d/conflicting-recovered-line/repo/$arc/audit-1793786400000001000-1793786400000009000.jsonl"
  readme "$d/conflicting-recovered-line" "A corrupt chunk whose only decodable line has the same ts as a line in a surviving valid chunk but a different payload (ethos-h5z8 shape); recovery must not install the conflicting line."

  # 4. overlay in both legacy and new locations
  mkdir -p "$d/overlay-both-locations"; base_repo "$d/overlay-both-locations"
  mkdir -p "$d/overlay-both-locations/repo/.punt-labs/ethos/identities/tperson.ext" "$d/overlay-both-locations/repo/.punt-labs/local/ethos/identities/tperson.ext"
  printf 'name: Test Person\nhandle: tperson\nkind: human\n' >"$d/overlay-both-locations/repo/.punt-labs/ethos/identities/tperson.yaml"
  printf 'notify: y\n' >"$d/overlay-both-locations/repo/.punt-labs/ethos/identities/tperson.ext/vox.local.yaml"
  printf 'notify: n\n' >"$d/overlay-both-locations/repo/.punt-labs/local/ethos/identities/tperson.ext/vox.local.yaml"
  readme "$d/overlay-both-locations" "A repository extension overlay present both beside the identity (legacy v4.10-v4.20 location) and under .punt-labs/local/ethos/identities (new location), with different values."

  # 5. both config files
  mkdir -p "$d/both-config-files"; base_repo "$d/both-config-files"
  printf 'agent: claude\n' >"$d/both-config-files/repo/.punt-labs/ethos/config.yaml"
  printf 'agent: wperson\n' >"$d/both-config-files/repo/.punt-labs/ethos.yaml"
  readme "$d/both-config-files" "Both .punt-labs/ethos/config.yaml (pre-v2.1) and .punt-labs/ethos.yaml, naming different agents."

  # 6. force-added local file
  mkdir -p "$d/force-added-local-file"; base_repo "$d/force-added-local-file"
  mkdir -p "$d/force-added-local-file/repo/.punt-labs/local/ethos/sessions"
  echo "$good" >"$d/force-added-local-file/repo/$live"
  printf '.punt-labs/local/ethos/sessions/%s.audit.jsonl\n.punt-labs/ethos/enabled\n.punt-labs/ethos.yaml\n.gitignore\n' "$sid" >"$d/force-added-local-file/tracked-files.txt"
  readme "$d/force-added-local-file" "A live audit file under .punt-labs/local that git add -f put into the index; tracked-files.txt is the index's path set."

  # 7. tracked trace under .punt-labs/ethos/sessions
  mkdir -p "$d/tracked-trace-under-sessions"; base_repo "$d/tracked-trace-under-sessions"
  mkdir -p "$d/tracked-trace-under-sessions/repo/.punt-labs/ethos/sessions/$sid"
  echo "$good" >"$d/tracked-trace-under-sessions/repo/.punt-labs/ethos/sessions/$sid/audit.jsonl"
  echo "$good" >"$d/tracked-trace-under-sessions/repo/.punt-labs/ethos/sessions/$sid.audit.jsonl"
  printf '.punt-labs/ethos/sessions/%s/audit.jsonl\n.punt-labs/ethos/sessions/%s.audit.jsonl\n.punt-labs/ethos/enabled\n.punt-labs/ethos.yaml\n.gitignore\n' "$sid" "$sid" >"$d/tracked-trace-under-sessions/tracked-files.txt"
  readme "$d/tracked-trace-under-sessions" "Audit trace written under the tracked record tree .punt-labs/ethos/sessions (a v3.12-v4.20 layout) and committed; tracked-files.txt is the index's path set."

  # 8. stale pre-commit hook section calling hook commit-trailers
  mkdir -p "$d/stale-hook-section/repo/.punt-labs/ethos" "$d/stale-hook-section/git/hooks"
  base_repo "$d/stale-hook-section"
  cat >"$d/stale-hook-section/git/hooks/commit-msg" <<'EOF'
#!/bin/sh
# --- BEGIN ETHOS DES-058 TRAILERS ---
# Copied by an older ethos enable: calls the retired trailer subcommand.
ethos_bin=$(command -v ethos) || exit 0
trailers=$("$ethos_bin" hook commit-trailers) || exit 0
[ -n "$trailers" ] && printf '\n%s\n' "$trailers" >>"$1"
# --- END ETHOS DES-058 TRAILERS ---
EOF
  chmod +x "$d/stale-hook-section/git/hooks/commit-msg"
  readme "$d/stale-hook-section" "A commit-msg hook section from before DES-083 that calls ethos hook commit-trailers (the retired subcommand) instead of the hook commit-msg shim; under git/hooks."

  # 9. dead session roster without a checkout field
  mkdir -p "$d/roster-without-checkout/home/.punt-labs/ethos/sessions/current"
  base_repo "$d/roster-without-checkout"
  cat >"$d/roster-without-checkout/home/.punt-labs/ethos/sessions/$sid.yaml" <<EOF
session: $sid
started: "2026-11-04T10:00:00Z"
host: @HOST@
participants:
    - agent_id: tperson
      persona: tperson
      joined: "2026-11-04T10:00:00Z"
    - agent_id: "2147483646"
      persona: claude
      parent: tperson
      joined: "2026-11-04T10:00:00Z"
EOF
  : >"$d/roster-without-checkout/home/.punt-labs/ethos/sessions/$sid.lock"
  printf '%s\n' "$sid" >"$d/roster-without-checkout/home/.punt-labs/ethos/sessions/current/2147483646"
  readme "$d/roster-without-checkout" "A session roster with no checkout field whose claude participant pid (2147483646) is not running, plus the sessions/current pointer file named for that pid."

  # 10. repo .gitignore ignoring .punt-labs/ethos/sessions (ethos-7ax7)
  mkdir -p "$d/gitignore-ignores-sessions"; base_repo "$d/gitignore-ignores-sessions"
  mkdir -p "$d/gitignore-ignores-sessions/repo/.punt-labs/ethos/sessions"
  printf '# ethos: shared machine-local zone (never track)\n.punt-labs/local/\n.punt-labs/ethos/sessions/\n' >"$d/gitignore-ignores-sessions/repo/.gitignore"
  echo "$good" >"$d/gitignore-ignores-sessions/repo/.punt-labs/ethos/sessions/$sid.audit.jsonl"
  readme "$d/gitignore-ignores-sessions" "A repository .gitignore that ignores .punt-labs/ethos/sessions, with an untracked audit file there (ethos-7ax7 shape)."

  # 10b. identity with a top-level voice field. Every released binary in the
  # corpus migrates the field on write, so only a hand-made file has this shape.
  mkdir -p "$d/top-level-voice-field/home/.punt-labs/ethos/identities"; base_repo "$d/top-level-voice-field"
  printf 'name: Voice Person\nhandle: vperson\nkind: human\nemail: voice@example.com\nvoice:\n  provider: say\n  voice: Samantha\n' >"$d/top-level-voice-field/home/.punt-labs/ethos/identities/vperson.yaml"
  readme "$d/top-level-voice-field" "A global identity file with a top-level voice: field (pre-v2.0); hand-made because every released binary migrates the field into the vox extension on write."

  # 11. live audit file with pre-redaction lines (the 4.22.1 stuck-warning shape)
  mkdir -p "$d/live-audit-pre-redaction"; base_repo "$d/live-audit-pre-redaction"
  mkdir -p "$d/live-audit-pre-redaction/repo/.punt-labs/local/ethos/sessions"
  {
    printf '{"session":"%s","tool":"Bash","tool_input":{"command":"cat @HOME@/.netrc; echo %s"},"ts":"2026-11-04T09:00:00.000001Z"}\n' "$sid" "$FAKE_TOKEN"
    echo "$good"
  } >"$d/live-audit-pre-redaction/repo/$live"
  readme "$d/live-audit-pre-redaction" "A live session audit file whose first line was written before redaction (v4.20.0): it holds a home path and the fake token unredacted; every commit warns and quarantine, repair and scrub refuse it."
}

# ------------------------------------------------------------------ readme

write_readme() {
  local f=$OUT/README.md t d size
  size=$(corpus_size)
  {
    echo "# Legacy corpus and damage fixtures"
    echo
    echo "Test data for the automatic upgrader (\`internal/autorepair\`, epic ethos-ftey)."
    echo "Everything here is generated; do not edit it by hand."
    echo
    echo "## Regenerating"
    echo
    echo '```sh'
    echo "make legacy-corpus"
    echo "# which runs:"
    echo "scripts/legacy-corpus.sh"
    echo "# one tag only (the damage fixtures and this README are rewritten from what is present):"
    echo 'scripts/legacy-corpus.sh --versions "v4.22.1"'
    echo '```'
    echo
    echo "\`make test-legacy-corpus\` (\`scripts/legacy_corpus_test.sh\`) rebuilds one tag into a temporary directory"
    echo "and checks it against the committed copy. Neither target is part of \`make check\`: they build nine"
    echo "binaries from tags and need full history (\`git fetch --tags\`)."
    echo
    echo "## What a snapshot holds"
    echo
    echo "\`corpus/<tag>/\` is the state one released binary wrote while running a fixed scenario in an"
    echo "isolated HOME and a fresh scratch git repository:"
    echo
    echo "- \`repo/\` is the scratch repository's working tree, without \`.git\` and without"
    echo "  \`.punt-labs/local/ethos/locks/\` (lock file names are hashes of the scratch path)."
    echo "- \`home/\` is the scratch HOME limited to \`.punt-labs/ethos\`. Files \`ethos seed\` deployed and the"
    echo "  scenario never changed are omitted (starter roles, talents, bundles: about 700 KB per version,"
    echo "  a function of the binary); they are listed in \`seed-omitted.txt\`."
    echo "- \`git/\` replaces \`.git\`, which git cannot commit under testdata and whose objects hold the scratch"
    echo "  paths. \`repo.bundle\` and \`index\` are re-created from the rewritten tree with the same tracked path"
    echo "  set (\`tracked-files.txt\`), the original commit message (including any trailer the installed hooks"
    echo "  added) and a fixed date; \`hooks/\` are the non-sample hooks \`ethos enable\` installed."
    echo "- \`PROVENANCE.txt\` records the tag, commit, toolchain and the status of every scenario step."
    echo
    echo "Absolute scratch paths are rewritten to \`@REPO@\` and \`@HOME@\`; the host name to \`@HOST@\`, the user name to"
    echo "\`@USER@\` and the fake process id (a file name under \`sessions/current/\` and the roster agent id) to"
    echo "\`@PID@\`. A hash that covered a rewritten path is not recomputed. Identities are synthetic"
    echo "(Test Person, \`person@example.com\`). The only secret-shaped string is the fake token"
    echo "\`$FAKE_TOKEN\`, which pre-v4.20.0 binaries store unredacted in their audit logs."
    echo
    echo "The scenario, per version and only with commands that version supports: seed, setup from a file,"
    echo "identity creation (worker, evaluator, and one with a top-level \`voice:\` field), a vox extension and a"
    echo "local overlay, a team, enable, session-start, a mission from a contract file using \`inputs.bead\`, a"
    echo "mission from flags, result, reflect, advance, a second result and close on the first mission, a claim"
    echo "of the second and a delegation (\`hook pre-tool-use\` with an Agent payload), an ADR, three"
    echo "\`hook audit-log\` calls whose tool input holds the scratch HOME path and the fake token, and one"
    echo "\`git commit\` through the installed hooks."
    echo
    echo "## Binaries"
    echo
    echo "| Tag | Commit | Go toolchain |"
    echo "|---|---|---|"
    for d in "$OUT"/corpus/*/; do
      t=$(basename "$d")
      [ -f "$d/PROVENANCE.txt" ] || continue
      echo "| $t | $(sed -n 's/^commit: //p' "$d/PROVENANCE.txt") | $(sed -n 's/^go_toolchain: //p' "$d/PROVENANCE.txt") |"
    done
    echo
    echo "## Steps a version could not do"
    echo
    echo "Recorded, not faked: a step is listed when the version lacks the command or flag, drops the data,"
    echo "or the step failed. Every other step ran and succeeded."
    echo
    for d in "$OUT"/corpus/*/; do
      t=$(basename "$d")
      [ -f "$d/PROVENANCE.txt" ] || continue
      echo "- $t"
      grep -E '^  [^:]+: (skipped|failed)' "$d/PROVENANCE.txt" | perl -pe 's/^  /  - /' || true
    done
    echo
    echo "## Damage fixtures"
    echo
    echo "\`damage/<name>/\` holds a minimal \`repo/\` (and \`home/\`, \`git/\` or \`tracked-files.txt\` where the shape needs"
    echo "them) with one defect each. \`README\` in each directory states the shape."
    echo
    for d in "$OUT"/damage/*/; do
      [ -f "$d/README" ] || continue
      echo "- \`$(basename "$d")\`: $(cat "$d/README")"
    done
    echo
    echo "## Size"
    echo
    echo "$size"
  } >"$f"
}

corpus_size() {
  local bytes files
  bytes=$( { find "$OUT/corpus" "$OUT/damage" -type f -exec wc -c {} + 2>/dev/null || true; } | awk '$2=="total"{t+=$1} END{print t+0}')
  files=$(find "$OUT/corpus" "$OUT/damage" -type f 2>/dev/null | wc -l | tr -d ' ')
  echo "$bytes bytes in $files files under \`corpus/\` and \`damage/\` (target: under 2 MB = 2097152 bytes)."
}

if [ "$DO_DAMAGE" -eq 1 ]; then write_damage; fi
if [ "$DO_README" -eq 1 ]; then write_readme; fi

if [ -n "$FAILED" ]; then
  note "FAILED:$FAILED"
  exit 1
fi
note "done: $(corpus_size)"
