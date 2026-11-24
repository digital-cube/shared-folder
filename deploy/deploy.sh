#!/usr/bin/env bash
# deploy/deploy.sh — build shfd, shfd-gui and the download packages from a
# clean `git archive` of one commit and put them on a server: versions/<sha>,
# an atomic swap of the symlink current, a restart, health checks, and an
# automatic rollback when the checks fail. docs/deploy.md has the details.
#
#   deploy/deploy.sh <target> [--ref <git-ref>] [--dry-run] [--skip-tests]
#                             [--only shfd|gui|downloads[,…]]
#
# <target> is deploy/targets/<target>.env (or a path to an .env file).
# Runs on the machine that builds: Go 1.27+, Node 22+ with npm, make, git,
# curl, ssh. Server-side steps are in deploy/remote.sh.
#
# Exit codes: 0 deployed (or already there); 1 failed before anything on
# the server changed; 2 bad usage or configuration; 3 failed after a change
# and rolled back to the previous versions; 4 failed and the rollback did
# not complete either: see the report, docs/deploy.md "Ručni povratak".

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO=$(cd "$HERE/.." && pwd)
REMOTE_SH=$HERE/remote.sh
VERSION_PKG=github.com/digital-cube/shared-folder/internal/version.Version

usage() {
	cat <<EOF
usage: deploy/deploy.sh <target> [--ref <git-ref>] [--dry-run] [--skip-tests]
                         [--only shfd|gui|downloads[,...]]

  <target>       deploy/targets/<target>.env, or a path to an .env file
  --ref REF      the commit to deploy (default HEAD); built from git archive,
                 never from the working tree
  --dry-run      print every step and command; change nothing on the server
                 (server state is still read), build nothing
  --skip-tests   do not run go test ./... and make gui-test before building
  --only LIST    deploy only these parts (comma list, or repeat the flag):
                 shfd (binary; restarts SHFD_SERVICES), gui (shfd-gui;
                 restarts GUI_SERVICES), downloads (make release packages)
EOF
}

# --- output -----------------------------------------------------------------

step_no=0
step() { step_no=$((step_no + 1)); printf '\n==> [%d] %s\n' "$step_no" "$*" >&2; }
info() { printf '    %s\n' "$*" >&2; }
warn() { printf '    WARNING: %s\n' "$*" >&2; }
die() { printf '\nERROR: %s\n' "$*" >&2; exit 1; }
usage_die() { printf 'deploy.sh: %s\n\n' "$*" >&2; usage >&2; exit 2; }
config_die() { printf 'deploy.sh: %s\n' "$*" >&2; exit 2; }

# run: a local command; printed, and run unless --dry-run.
run() {
	printf '    %s$' "$([[ $DRY_RUN == 1 ]] && echo '(dry-run) ')" >&2
	printf ' %q' "$@" >&2
	printf '\n' >&2
	[[ $DRY_RUN == 1 ]] && return 0
	"$@"
}

# run_in DIR CMD…: run, in DIR (env -C is not on macOS).
run_in() {
	local dir=$1
	shift
	printf '    %s$ (cd %q &&' "$([[ $DRY_RUN == 1 ]] && echo '(dry-run) ')" "$dir" >&2
	printf ' %q' "$@" >&2
	printf ')\n' >&2
	[[ $DRY_RUN == 1 ]] && return 0
	(cd "$dir" && "$@")
}

# q: one word quoted for a POSIX shell (the login shell on the server may
# be sh, so no $'…' from printf %q).
q() { printf "'%s'" "${1//\'/\'\\\'\'}"; }
qall() { local a out=(); for a in "$@"; do out+=("$(q "$a")"); done; echo "${out[*]}"; }

has() { [[ ",$1," == *",$2,"* ]]; }

# --- arguments ----------------------------------------------------------------

TARGET="" REF="" DRY_RUN=0 SKIP_TESTS=0 ONLY=""
while (($#)); do
	case $1 in
	-h | --help) usage; exit 0 ;;
	--ref) (($# >= 2)) || usage_die "--ref needs a value"; REF=$2; shift ;;
	--ref=*) REF=${1#*=} ;;
	--dry-run) DRY_RUN=1 ;;
	--skip-tests) SKIP_TESTS=1 ;;
	--only) (($# >= 2)) || usage_die "--only needs a value"; ONLY+=${ONLY:+,}$2; shift ;;
	--only=*) ONLY+=${ONLY:+,}${1#*=} ;;
	-*) usage_die "unknown option $1" ;;
	*) [[ -z $TARGET ]] || usage_die "one target only (got $TARGET and $1)"; TARGET=$1 ;;
	esac
	shift
done
[[ -n $TARGET ]] || usage_die "no target"
REF=${REF:-HEAD}

COMPS=""
for c in ${ONLY//,/ }; do
	case $c in
	shfd | gui | downloads) has "$COMPS" "$c" || COMPS+=${COMPS:+,}$c ;;
	*) usage_die "--only: unknown part '$c' (shfd, gui, downloads)" ;;
	esac
done
COMPS=${COMPS:-shfd,gui,downloads}

# --- target -----------------------------------------------------------------

if [[ $TARGET == */* || $TARGET == *.env ]]; then
	TARGET_FILE=$TARGET
else
	TARGET_FILE=$HERE/targets/$TARGET.env
fi
[[ -f $TARGET_FILE ]] || config_die "no target file $TARGET_FILE (copy deploy/targets/example.env)"
# Variables the target may set. The environment does not leak in (an
# exported SUDO or SSH_TARGET of the shell is ignored); DEPLOY_<VAR>=… in
# the environment overrides the file for one run (docs/deploy.md).
CONFIG_VARS=(SSH_TARGET SSH_JUMP SSH_OPTS PUBLIC_URL GUI_PUBLIC_URL SHFD_PREFIX GUI_PREFIX
	DOWNLOADS_DIR KEEP_VERSIONS SHFD_SERVICES GUI_SERVICES SHFD_LOCAL_URL GUI_LOCAL_URL
	GUI_BASE CHECK_TIMEOUT SERVER_PLATFORM SUDO SYSTEMCTL)
for v in "${CONFIG_VARS[@]}"; do unset "$v"; done
# shellcheck source=/dev/null
source "$TARGET_FILE"
for v in "${CONFIG_VARS[@]}"; do
	o=DEPLOY_$v
	if [[ -n ${!o+x} ]]; then printf -v "$v" '%s' "${!o}"; fi
done

: "${SSH_TARGET:?$TARGET_FILE: SSH_TARGET is required (user@host, or local)}"
: "${PUBLIC_URL:?$TARGET_FILE: PUBLIC_URL is required}"
SSH_JUMP=${SSH_JUMP:-}
SSH_OPTS=${SSH_OPTS:-}
PUBLIC_URL=${PUBLIC_URL%/}
SHFD_PREFIX=${SHFD_PREFIX:-/opt/shfd}
GUI_PREFIX=${GUI_PREFIX:-/opt/shfd-gui}
DOWNLOADS_DIR=${DOWNLOADS_DIR:-}
KEEP_VERSIONS=${KEEP_VERSIONS:-5}
SHFD_SERVICES=${SHFD_SERVICES:-shfd shfd-thumbs}
GUI_SERVICES=${GUI_SERVICES:-shfd-gui}
SHFD_LOCAL_URL=${SHFD_LOCAL_URL:-http://127.0.0.1:8443}
GUI_LOCAL_URL=${GUI_LOCAL_URL:-http://127.0.0.1:8444}
CHECK_TIMEOUT=${CHECK_TIMEOUT:-30}
SERVER_PLATFORM=${SERVER_PLATFORM:-linux/amd64}
SUDO=${SUDO-sudo}
SYSTEMCTL=${SYSTEMCTL:-systemctl}
GO=${GO:-go}
if ! [[ $KEEP_VERSIONS =~ ^[0-9]+$ ]] || ((KEEP_VERSIONS < 2)); then
	config_die "KEEP_VERSIONS must be 2 or more (current and previous are always kept)"
fi
[[ $CHECK_TIMEOUT =~ ^[0-9]+$ ]] || config_die "CHECK_TIMEOUT must be seconds"
[[ $SERVER_PLATFORM == */* ]] || config_die "SERVER_PLATFORM must be os/arch"

# --- local and remote execution -------------------------------------------------

TMP=$(mktemp -d "${TMPDIR:-/tmp}/shfd-deploy.XXXXXX")
SSH=(ssh -o BatchMode=yes -o ConnectTimeout=15 -o ServerAliveInterval=15
	-o ControlMaster=auto -o "ControlPath=$TMP/ssh-%C" -o ControlPersist=300)
[[ -n $SSH_JUMP ]] && SSH+=(-J "$SSH_JUMP")
if [[ -n $SSH_OPTS ]]; then read -r -a _o <<<"$SSH_OPTS"; SSH+=("${_o[@]}"); fi

remote_env() {
	local e=(SHFD_PREFIX="$SHFD_PREFIX" GUI_PREFIX="$GUI_PREFIX" DOWNLOADS_DIR="$DOWNLOADS_DIR"
		KEEP_VERSIONS="$KEEP_VERSIONS" SHFD_SERVICES="$SHFD_SERVICES" GUI_SERVICES="$GUI_SERVICES"
		SHFD_LOCAL_URL="$SHFD_LOCAL_URL" GUI_LOCAL_URL="$GUI_LOCAL_URL" GUI_BASE="${GUI_BASE:-}"
		CHECK_TIMEOUT="$CHECK_TIMEOUT" DRY_RUN="$DRY_RUN" SUDO="$SUDO" SYSTEMCTL="$SYSTEMCTL")
	printf '%s\0' "${e[@]}"
}

# remote CMD ARGS…: one command of remote.sh on the server.
remote() {
	local envs=()
	mapfile -d '' envs < <(remote_env)
	if [[ $SSH_TARGET == local ]]; then
		env "${envs[@]}" bash -s -- "$@" <"$REMOTE_SH"
	else
		"${SSH[@]}" "$SSH_TARGET" "env $(qall "${envs[@]}") bash -s -- $(qall "$@")" <"$REMOTE_SH"
	fi
}

# upload STAGE: the bundle (tar on stdin) into STAGE on the server.
upload() {
	if [[ $SSH_TARGET == local ]]; then
		tar -x -C "$1"
	else
		"${SSH[@]}" "$SSH_TARGET" "tar -x -C $(q "$1")"
	fi
}

# probe URL WANT: like remote.sh's: 200, "ok":true, and version WANT ("-"
# for any). Prints what it found.
probe() {
	local body v
	body=$(curl -fsS --max-time 10 "$1" 2>&1) || { echo "unreachable: ${body//$'\n'/ }"; return 1; }
	v=$(sed -n 's/.*"version":"\([^"]*\)".*/\1/p' <<<"$body")
	[[ $body == *'"ok":true'* ]] || { echo "not ok: $body"; return 1; }
	[[ $2 == - || $v == "$2" ]] || { echo "version $v, want $2"; return 1; }
	echo "ok, version $v"
}

# probe_downloads URL SHA: GET /v1/downloads is enabled and lists SHA's
# packages.
probe_downloads() {
	local body
	body=$(curl -fsS --max-time 10 "$1" 2>&1) || { echo "unreachable: ${body//$'\n'/ }"; return 1; }
	[[ $body == *'"enabled":true'* ]] || { echo "downloads not enabled (shfd serve without --downloads?)"; return 1; }
	[[ $body == *"\"version\":\"$2\""* ]] || { echo "no package of version $2"; return 1; }
	[[ $body == *'"sha256":"'* ]] || { echo "packages without SHA256SUMS"; return 1; }
	echo "ok, packages of $2 listed"
}

# public_checks: from this machine, through nginx; retried up to
# CHECK_TIMEOUT. Sets PUBLIC_FAIL to what failed last.
public_checks() {
	local shfd_want=$1 gui_want=$2 dl_want=$3 deadline name fn url want res
	local -A ok=() err=()
	deadline=$((SECONDS + CHECK_TIMEOUT))
	while :; do
		for name in shfd gui downloads; do
			[[ -n ${ok[$name]:-} ]] && continue
			case $name in
			shfd) fn=probe url=$PUBLIC_URL/healthz want=$shfd_want ;;
			gui) fn=probe url=$GUI_PUBLIC_URL/api/healthz want=$gui_want ;;
			downloads) fn=probe_downloads url=$PUBLIC_URL/v1/downloads want=$dl_want ;;
			esac
			if [[ $want == skip ]]; then
				ok[$name]=1
			elif res=$("$fn" "$url" "$want"); then
				ok[$name]=1
				info "$url: $res"
			else
				err[$name]="$url: $res"
			fi
		done
		((${#ok[@]} == 3)) && return 0
		if ((SECONDS >= deadline)); then
			PUBLIC_FAIL=""
			for name in shfd gui downloads; do
				[[ -n ${ok[$name]:-} ]] && continue
				info "FAILED: ${err[$name]}"
				PUBLIC_FAIL+="${PUBLIC_FAIL:+; }${err[$name]}"
			done
			return 1
		fi
		sleep 1
	done
}

# --- the state of a deploy, for the exit trap -------------------------------------

PHASE=preflight   # preflight → build → staged → changed → finished
STAGE="" FAIL_REASON="" SHFD_PREV="" GUI_PREV="" DEPLOY=""
DEPLOY_UNITS=() SHA="" FULL=""

cleanup_local() {
	if [[ $SSH_TARGET != local ]]; then
		"${SSH[@]}" -O exit "$SSH_TARGET" >/dev/null 2>&1 || true
	fi
	rm -rf "$TMP"
}

report() { # report STATUS LINES…
	printf '\n==== deploy %s %s (%s): %s\n' "$TARGET" "${SHA:-?}" "$REF" "$1" >&2
	shift
	local l
	for l in "$@"; do printf '     %s\n' "$l" >&2; done
}

on_exit() {
	local rc=$?
	trap - EXIT INT TERM
	set +e
	if ((rc != 0)) && [[ $PHASE == changed ]]; then
		info ""
		step "ROLLBACK to the previous versions (${FAIL_REASON:-interrupted or failed, exit $rc})"
		local rb=0 units
		units=${DEPLOY_UNITS[*]:-}
		remote rollback "$STAGE" "$DEPLOY" "${SHFD_PREV:--}" "${GUI_PREV:--}" "${units:--}" || rb=1
		local shfd_want=- gui_want=skip
		has "$DEPLOY" shfd && shfd_want=$SHFD_PREV
		has "$DEPLOY" gui && gui_want=$GUI_PREV
		if ((rb == 0)); then
			step "checking the previous versions"
			remote check "$shfd_want" "$gui_want" || rb=1
			((rb == 0)) && { public_checks "$shfd_want" "$gui_want" skip || rb=1; }
		fi
		[[ -n $STAGE ]] && remote discard "$STAGE" >/dev/null 2>&1
		local lines=("reason: ${FAIL_REASON:-exit $rc}")
		has "$DEPLOY" shfd && lines+=("shfd:      $SHFD_PREFIX/current -> versions/$SHFD_PREV (was about to be $SHA)")
		has "$DEPLOY" gui && lines+=("shfd-gui:  $GUI_PREFIX/current -> versions/$GUI_PREV (was about to be $SHA)")
		has "$DEPLOY" downloads && lines+=("downloads: $DOWNLOADS_DIR restored (packages and SHA256SUMS of before)")
		lines+=("versions/$SHA stays on the server for a look; the next cleanup removes it")
		if ((rb == 0)); then
			report "FAILED, ROLLED BACK (exit 3)" "${lines[@]}" "the previous versions answer their health checks"
			rc=3
		else
			report "FAILED, ROLLBACK INCOMPLETE (exit 4)" "${lines[@]}" \
				"the rollback or its checks failed: see the output above and docs/deploy.md (Ručni povratak)"
			rc=4
		fi
	elif ((rc != 0)) && [[ $PHASE == staged ]]; then
		remote discard "$STAGE" >/dev/null 2>&1
		report "FAILED (exit 1), nothing changed on the server" "reason: ${FAIL_REASON:-exit $rc}"
	elif ((rc != 0)) && [[ $PHASE != finished ]]; then
		report "FAILED (exit $rc), nothing changed on the server" "reason: ${FAIL_REASON:-see above}"
	fi
	cleanup_local
	exit "$rc"
}
trap on_exit EXIT
trap 'FAIL_REASON="interrupted"; exit 130' INT TERM

fail() { FAIL_REASON=$1; die "$1"; }

# --- 1. local preflight -----------------------------------------------------------

step "target $TARGET ($TARGET_FILE), parts: $COMPS$([[ $DRY_RUN == 1 ]] && echo ', DRY RUN')"
info "server: $SSH_TARGET${SSH_JUMP:+ via $SSH_JUMP}, public: $PUBLIC_URL"

for t in git tar curl make; do command -v "$t" >/dev/null || fail "$t not found"; done
[[ $SSH_TARGET == local ]] || command -v ssh >/dev/null || fail "ssh not found"

FULL=$(git -C "$REPO" rev-parse --verify --quiet "$REF^{commit}") || config_die "--ref $REF is not a commit of $REPO"
SHA=$(git -C "$REPO" rev-parse --short "$FULL")
info "ref $REF = $SHA ($(git -C "$REPO" log -1 --format='%s' "$FULL" | cut -c1-70))"
if [[ -n $(git -C "$REPO" status --porcelain 2>/dev/null) ]]; then
	info "note: the working tree has changes; they are not deployed (the build is git archive $SHA)"
fi

if [[ $DRY_RUN != 1 ]]; then
	if has "$COMPS" shfd || has "$COMPS" downloads; then
		command -v "$GO" >/dev/null || fail "$GO not found (Go 1.27+ is needed; set GO=/path/to/go)"
		gov=$("$GO" env GOVERSION)
		want=$(git -C "$REPO" show "$FULL:go.mod" | sed -n 's/^go \([0-9.]*\).*/\1/p')
		if [[ $(printf '%s\n%s\n' "${want:-1.27}" "${gov#go}" | sort -V | head -1) != "${want:-1.27}" ]]; then
			fail "$gov is older than go ${want:-1.27} from go.mod"
		fi
		info "Go: $gov"
	fi
	if has "$COMPS" gui; then
		if ! command -v node >/dev/null || ! command -v npm >/dev/null; then
			fail "node and npm are needed for shfd-gui (Node 22+)"
		fi
		nodev=$(node -p 'process.versions.node')
		((${nodev%%.*} >= 22)) || fail "Node $nodev is older than 22"
		info "Node: $nodev"
	fi
fi

# --- 2. server preflight (read-only, also in a dry run) ---------------------------------

step "server preflight (read-only)"
pre=$(remote preflight "$COMPS") || fail "server preflight failed (see PROBLEM lines above); nothing was changed"
while IFS='=' read -r k v; do
	case $k in
	shfd_current) SHFD_PREV=$v ;;
	gui_current) GUI_PREV=$v ;;
	downloads_dir) DOWNLOADS_DIR=$v ;;
	unit_gui_base) unit_gui_base=$v ;;
	esac
done <<<"$pre"
# GUI base path: the target's GUI_BASE, else SHFD_GUI_BASE of the unit,
# else shfd-gui's default /gui. "/" is the root, like "".
if [[ -z ${GUI_BASE+x} ]]; then
	GUI_BASE=/gui
	[[ ${unit_gui_base:--} != - ]] && GUI_BASE=$unit_gui_base
fi
GUI_BASE=${GUI_BASE%/}
GUI_PUBLIC_URL=${GUI_PUBLIC_URL:-$PUBLIC_URL$GUI_BASE}
GUI_PUBLIC_URL=${GUI_PUBLIC_URL%/}

DEPLOY=""
for c in shfd gui; do
	has "$COMPS" "$c" || continue
	prev=$SHFD_PREV
	[[ $c == gui ]] && prev=$GUI_PREV
	if [[ $prev == "$SHA" ]]; then
		info "$c: already on $SHA, skipped"
	else
		info "$c: current $prev -> $SHA"
		DEPLOY+=${DEPLOY:+,}$c
	fi
done
if has "$COMPS" downloads; then
	DEPLOY+=${DEPLOY:+,}downloads
	info "downloads: $DOWNLOADS_DIR"
fi
info "GUI base path: '${GUI_BASE}' (public check $GUI_PUBLIC_URL/api/healthz)"

DEPLOY_UNITS=()
if has "$DEPLOY" shfd; then read -r -a _u <<<"$SHFD_SERVICES"; DEPLOY_UNITS+=("${_u[@]}"); fi
if has "$DEPLOY" gui; then read -r -a _u <<<"$GUI_SERVICES"; DEPLOY_UNITS+=("${_u[@]}"); fi

# Expected versions after the restart: SHA for what is deployed; shfd must
# be healthy either way (any version), a GUI that is not deployed is left
# alone (an old one may not have /api/healthz).
SHFD_WANT=- GUI_WANT=skip DL_WANT=skip
has "$DEPLOY" shfd && SHFD_WANT=$SHA
has "$DEPLOY" gui && GUI_WANT=$SHA
has "$DEPLOY" downloads && DL_WANT=$SHA

if [[ -z $DEPLOY ]]; then
	# What was asked for is already current: only check that it answers.
	has "$COMPS" shfd && SHFD_WANT=$SHA
	has "$COMPS" gui && GUI_WANT=$SHA
	step "nothing to deploy: checking that $SHA answers"
	remote check "$SHFD_WANT" "$GUI_WANT" || fail "local health checks failed"
	[[ $DRY_RUN == 1 ]] || public_checks "$SHFD_WANT" "$GUI_WANT" "$DL_WANT" || fail "public checks failed: $PUBLIC_FAIL"
	PHASE=finished
	report "OK, already deployed"
	exit 0
fi

# --- 3. build from git archive -----------------------------------------------------------

PHASE=build
SRC=$TMP/src OUT=$TMP/out
goos=${SERVER_PLATFORM%/*} goarch=${SERVER_PLATFORM#*/}
step "build $SHA from git archive (not from the working tree) in $TMP"
run mkdir -p "$SRC" "$OUT"
info "git archive --format=tar $FULL | tar -x -C $SRC"
if [[ $DRY_RUN != 1 ]]; then
	git -C "$REPO" archive --format=tar "$FULL" | tar -x -C "$SRC"
fi
if [[ $SKIP_TESTS == 1 ]]; then
	info "tests skipped (--skip-tests)"
else
	if has "$DEPLOY" shfd || has "$DEPLOY" downloads; then
		step "go test ./..."
		run_in "$SRC" "$GO" test ./... || fail "go test failed"
	fi
	if has "$DEPLOY" gui; then
		step "make gui-test"
		run make -C "$SRC" gui-test || fail "make gui-test failed"
	fi
fi
if has "$DEPLOY" shfd; then
	step "shfd for $SERVER_PLATFORM"
	run_in "$SRC" env CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" "$GO" build -trimpath \
		-ldflags "-s -w -X $VERSION_PKG=$SHA" -o "$OUT/shfd" ./cmd/shfd || fail "go build shfd failed"
	if [[ $DRY_RUN != 1 && $("$GO" env GOOS)/$("$GO" env GOARCH) == "$SERVER_PLATFORM" ]]; then
		v=$("$OUT/shfd" version 2>&1 || true)
		[[ $v == *"$SHA"* ]] || fail "the built shfd says '$v', not $SHA"
		info "shfd version: $v"
	fi
fi
if has "$DEPLOY" gui; then
	step "shfd-gui (make gui)"
	run make -C "$SRC" gui VERSION="$SHA" || fail "make gui failed"
	run tar -C "$SRC/gui/dist" -czf "$OUT/shfd-gui.tgz" . || fail "tar of gui/dist failed"
fi
if has "$DEPLOY" downloads; then
	step "packages (make release)"
	run make -C "$SRC" release VERSION="$SHA" DIST_DIR="$OUT/dist" GO="$GO" || fail "make release failed"
fi

# --- 4. upload and install (nothing is live yet) -------------------------------------------

step "upload to the server"
STAGE=$(remote stage) || fail "could not create a temp directory on the server"
PHASE=staged
info "tar -C $OUT -cf - . | ${SSH_TARGET}: tar -x -C $STAGE"
if [[ $DRY_RUN != 1 ]]; then
	tar -C "$OUT" -cf - . | upload "$STAGE" || fail "upload failed"
fi

step "install $SHA into versions/ and the packages into the downloads directory"
# From here on the server has changed (downloads first); any failure rolls back.
PHASE=changed
remote install "$STAGE" "$SHA" "$DEPLOY" || fail "install on the server failed"

# --- 5. switch, restart, check ---------------------------------------------------------------

step "switch current -> versions/$SHA (previous: shfd ${SHFD_PREV:--}, gui ${GUI_PREV:--})"
remote switch "$SHA" "$DEPLOY" || fail "switching the symlinks failed"

step "restart ${DEPLOY_UNITS[*]:-(none)}"
units=${DEPLOY_UNITS[*]:-}
remote restart "${units:--}" || fail "systemctl restart failed"

step "local checks on the server (up to ${CHECK_TIMEOUT}s)"
remote check "$SHFD_WANT" "$GUI_WANT" || fail "local health checks on the server failed"

step "public checks via $PUBLIC_URL (up to ${CHECK_TIMEOUT}s)"
if [[ $DRY_RUN == 1 ]]; then
	info "(dry-run) curl $PUBLIC_URL/healthz (version $SHFD_WANT), $GUI_PUBLIC_URL/api/healthz ($GUI_WANT), $PUBLIC_URL/v1/downloads ($DL_WANT)"
else
	public_checks "$SHFD_WANT" "$GUI_WANT" "$DL_WANT" || fail "public checks failed: $PUBLIC_FAIL"
fi

# --- 6. cleanup ------------------------------------------------------------------------------------

PHASE=finished
step "cleanup: keep $KEEP_VERSIONS versions (always current and previous), packages of $SHA only"
if ! remote cleanup "$STAGE" "$SHA" "$DEPLOY" "${SHFD_PREV:--}" "${GUI_PREV:--}"; then
	warn "cleanup failed; the deploy itself is fine. Old versions or packages may be left over."
fi

lines=()
has "$DEPLOY" shfd && lines+=("shfd:      versions/$SHFD_PREV -> versions/$SHA (rollback: docs/deploy.md)")
has "$DEPLOY" gui && lines+=("shfd-gui:  versions/$GUI_PREV -> versions/$SHA")
has "$DEPLOY" downloads && lines+=("downloads: packages of $SHA in $DOWNLOADS_DIR")
lines+=("restarted: ${DEPLOY_UNITS[*]:-none}")
if [[ $DRY_RUN == 1 ]]; then
	report "DRY RUN, nothing was changed" "${lines[@]}"
else
	report "OK" "${lines[@]}"
fi
