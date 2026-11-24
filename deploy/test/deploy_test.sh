#!/usr/bin/env bash
# deploy/test/deploy_test.sh [REF] — deploy/deploy.sh against this machine
# as the server (make deploy-test, docs/deploy.md).
#
# The "server" is a temp directory: opt/shfd and opt/shfd-gui with
# versions/ and current, downloads/, data/ and db/. The units are real
# shfd serve, shfd thumbs and shfd-gui processes run by fake-systemctl;
# SUDO empty. The server is reached over ssh through a throwaway sshd on
# 127.0.0.1 (as in internal/sshdtest) when sshd and ssh-keygen are there,
# else with SSH_TARGET=local; DEPLOY_TEST_TRANSPORT=local|ssh forces one.
# Scenarios, in this order:
#
#   1. --dry-run changes nothing (and builds nothing)
#   2. a deploy whose shfd does not come up rolls back: exit 3, current and
#      the downloads are as before, the old versions answer
#   3. a deploy succeeds: versions, /healthz, /api/healthz, /v1/downloads;
#      cleanup keeps KEEP_VERSIONS=2 newest plus the previous one, and only
#      the new packages; data is untouched
#   4. a second deploy of the same commit only checks (already deployed)
#   5. bad usage exits 2
#
# REF (default HEAD) is built twice, so this takes a few minutes; it needs
# what deploy.sh needs (Go, Node 22 with npm) plus a free port pair.
# KEEP_TMP=1 keeps the temp directory; DEPLOY_TEST_RUN_TESTS=1 lets
# scenario 3 run go test and make gui-test as well.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO=$(cd "$HERE/../.." && pwd)
REF=${1:-HEAD}
GO=${GO:-go}
export GO

T=$(mktemp -d "${TMPDIR:-/tmp}/shfd-deploy-test.XXXXXX")
export FAKE_ROOT=$T/server
FAKE_SHFD_PORT=$((20000 + RANDOM % 20000))
FAKE_GUI_PORT=$((FAKE_SHFD_PORT + 1))
export FAKE_SHFD_PORT FAKE_GUI_PORT
FAKECTL=$HERE/fake-systemctl
SYSTEMCTL_WORDS="env FAKE_ROOT=$FAKE_ROOT FAKE_SHFD_PORT=$FAKE_SHFD_PORT FAKE_GUI_PORT=$FAKE_GUI_PORT $FAKECTL"

pass=0
fails=()
ok() { pass=$((pass + 1)); printf '  ok   %s\n' "$*"; }
bad() { fails+=("$*"); printf '  FAIL %s\n' "$*"; }
check() { # check DESCRIPTION CMD…
	local d=$1
	shift
	if "$@"; then ok "$d"; else bad "$d"; fi
}
section() { printf '\n######## %s\n' "$*"; }

SSHD_PID=""
finish() {
	local rc=$?
	"$FAKECTL" stop shfd-gui shfd-thumbs shfd >/dev/null 2>&1 || true
	if [[ -n $SSHD_PID ]]; then kill "$SSHD_PID" 2>/dev/null || true; fi
	if [[ ${KEEP_TMP:-0} == 1 ]]; then echo "kept $T"; else rm -rf "$T"; fi
	exit "$rc"
}
trap finish EXIT

SHA=$(git -C "$REPO" rev-parse --short "$REF^{commit}")
OLD1=aaaaaa1 OLD2=aaaaaa2 PREV=aaaaaa3 # PREV is current before the deploys

# --- seed the "server": what the first, manual install leaves ------------------

section "seed $FAKE_ROOT (current: $PREV, older: $OLD1 $OLD2)"
S=$FAKE_ROOT
mkdir -p "$S/opt/shfd/versions" "$S/opt/shfd-gui/versions" "$S/downloads" "$S/data" "$S/db"
(cd "$REPO" && "$GO" build -trimpath -ldflags "-X github.com/digital-cube/shared-folder/internal/version.Version=$PREV" \
	-o "$S/opt/shfd/versions/$PREV/shfd" ./cmd/shfd)
if [[ ! -f $REPO/gui/dist/server.js ]]; then make -C "$REPO" gui; fi
cp -a "$REPO/gui/dist" "$S/opt/shfd-gui/versions/$PREV"
echo "$PREV" >"$S/opt/shfd-gui/versions/$PREV/VERSION"
for v in "$OLD1" "$OLD2"; do
	mkdir -p "$S/opt/shfd/versions/$v" "$S/opt/shfd-gui/versions/$v/bin"
	cp "$S/opt/shfd/versions/$PREV/shfd" "$S/opt/shfd/versions/$v/shfd"
done
ln -s "versions/$PREV" "$S/opt/shfd/current"
ln -s "versions/$PREV" "$S/opt/shfd-gui/current"
# Ages: OLD1 and OLD2 are newer than PREV, so with KEEP_VERSIONS=2 only the
# rule "previous always stays" keeps PREV.
for p in shfd shfd-gui; do
	touch -d '3 days ago' "$S/opt/$p/versions/$PREV"
	touch -d '2 days ago' "$S/opt/$p/versions/$OLD1"
	touch -d '1 day ago' "$S/opt/$p/versions/$OLD2"
done
head -c 1000 /dev/urandom >"$S/downloads/shared-folder-$PREV-linux-amd64.tar.gz"
(cd "$S/downloads" && sha256sum ./*.tar.gz | sed 's| \./| |' >SHA256SUMS)
echo "not a package" >"$S/downloads/README.txt"
echo "member data" >"$S/data/sentinel.txt"

# --- transport: a throwaway sshd, or local ---------------------------------------

TRANSPORT=${DEPLOY_TEST_TRANSPORT:-}
SSHD=$(command -v sshd || echo /usr/sbin/sshd)
if [[ -z $TRANSPORT ]]; then
	TRANSPORT=local
	if [[ -x $SSHD ]] && command -v ssh-keygen >/dev/null; then TRANSPORT=ssh; fi
fi
SSH_TARGET_LINE="SSH_TARGET=local"
if [[ $TRANSPORT == ssh ]]; then
	mkdir -p "$T/sshd"
	ssh-keygen -q -t ed25519 -N "" -f "$T/sshd/host_ed25519"
	ssh-keygen -q -t ed25519 -N "" -f "$T/sshd/client_ed25519"
	cp "$T/sshd/client_ed25519.pub" "$T/sshd/authorized_keys"
	SSHD_PORT=$((FAKE_SHFD_PORT + 2))
	cat >"$T/sshd/sshd_config" <<EOF
Port $SSHD_PORT
ListenAddress 127.0.0.1
HostKey $T/sshd/host_ed25519
AuthorizedKeysFile $T/sshd/authorized_keys
PidFile $T/sshd/sshd.pid
AllowUsers $(id -un)
StrictModes no
UsePAM no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
AuthenticationMethods publickey
PermitTTY no
AllowTcpForwarding no
X11Forwarding no
EOF
	"$SSHD" -D -e -f "$T/sshd/sshd_config" 2>"$T/sshd/log" &
	SSHD_PID=$!
	ssh_opts="-p $SSHD_PORT -i $T/sshd/client_ed25519 -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR"
	SSH_TARGET_LINE="SSH_TARGET=$(id -un)@127.0.0.1
SSH_OPTS=\"$ssh_opts\""
	read -r -a _o <<<"$ssh_opts"
	for _ in $(seq 50); do
		ssh "${_o[@]}" -o BatchMode=yes "$(id -un)@127.0.0.1" true 2>/dev/null && break
		sleep 0.2
	done
fi
echo "transport: $TRANSPORT"

cat >"$T/target.env" <<EOF
$SSH_TARGET_LINE
PUBLIC_URL=http://127.0.0.1:$FAKE_SHFD_PORT
GUI_PUBLIC_URL=http://127.0.0.1:$FAKE_GUI_PORT/gui
SHFD_PREFIX=$S/opt/shfd
GUI_PREFIX=$S/opt/shfd-gui
# DOWNLOADS_DIR empty: read from SHFD_DOWNLOADS of the (fake) unit
DOWNLOADS_DIR=
KEEP_VERSIONS=2
SHFD_SERVICES="shfd shfd-thumbs"
GUI_SERVICES="shfd-gui"
SHFD_LOCAL_URL=http://127.0.0.1:$FAKE_SHFD_PORT
GUI_LOCAL_URL=http://127.0.0.1:$FAKE_GUI_PORT
CHECK_TIMEOUT=15
SERVER_PLATFORM=$("$GO" env GOOS)/$("$GO" env GOARCH)
SUDO=
SYSTEMCTL="$SYSTEMCTL_WORDS"
EOF

"$FAKECTL" restart shfd shfd-thumbs shfd-gui
for _ in $(seq 50); do
	curl -fsS "http://127.0.0.1:$FAKE_SHFD_PORT/healthz" >/dev/null 2>&1 &&
		curl -fsS "http://127.0.0.1:$FAKE_GUI_PORT/gui/api/healthz" >/dev/null 2>&1 && break
	sleep 0.2
done

version_of() { curl -fsS --max-time 5 "$1" 2>/dev/null | sed -n 's/.*"version":"\([^"]*\)".*/\1/p'; }
target_of() { readlink "$S/opt/$1/current"; }
snapshot() { # everything a deploy may touch, with times and link targets
	find "$S/opt" "$S/downloads" -printf '%p %y %s %T@ %l\n' | sort
}
dl_state() { (cd "$S/downloads" && ls -A && cat SHA256SUMS && sha256sum ./*) | sort; }

check "seed: shfd answers $PREV" test "$(version_of "http://127.0.0.1:$FAKE_SHFD_PORT/healthz")" = "$PREV"
check "seed: shfd-gui answers $PREV" test "$(version_of "http://127.0.0.1:$FAKE_GUI_PORT/gui/api/healthz")" = "$PREV"

deploy() { # deploy ARGS…: runs deploy.sh, output into $T/out; sets RC
	set +e
	"$REPO/deploy/deploy.sh" "$T/target.env" "$@" 2>&1 | tee "$T/out" | sed 's/^/    | /'
	RC=${PIPESTATUS[0]}
	set -e
}

# --- 1. dry run ---------------------------------------------------------------------

section "1. --dry-run changes nothing"
before=$(snapshot)
deploy --ref "$REF" --dry-run
check "dry run exits 0" test "$RC" = 0
if [[ $TRANSPORT == ssh ]]; then check "went over ssh" grep -q "server: $(id -un)@127.0.0.1" "$T/out"; fi
check "dry run says so" grep -q "DRY RUN, nothing was changed" "$T/out"
check "dry run prints the symlink swap" grep -q "(dry-run) \$ ln -sfn -- versions/$SHA" "$T/out"
check "dry run prints the restarts" grep -q "(dry-run) \$ .*fake-systemctl restart shfd" "$T/out"
check "dry run: nothing changed under opt/ and downloads/" test "$(snapshot)" = "$before"
check "dry run: no temp dir on the server" bash -c "! ls -d ${TMPDIR:-/tmp}/shfd-deploy.DRYRUN 2>/dev/null"

# --- 2. failed deploy rolls back ---------------------------------------------------------

section "2. shfd $SHA does not come up: rollback"
dl_before=$(dl_state)
echo "$SHA" >"$S/fail-version"
deploy --ref "$REF" --skip-tests
rm -f "$S/fail-version"
check "failed deploy exits 3" test "$RC" = 3
check "report says rolled back" grep -q "FAILED, ROLLED BACK" "$T/out"
check "shfd current back on $PREV" test "$(target_of shfd)" = "versions/$PREV"
check "shfd-gui current back on $PREV" test "$(target_of shfd-gui)" = "versions/$PREV"
check "shfd answers $PREV again" test "$(version_of "http://127.0.0.1:$FAKE_SHFD_PORT/healthz")" = "$PREV"
check "shfd-gui answers $PREV again" test "$(version_of "http://127.0.0.1:$FAKE_GUI_PORT/gui/api/healthz")" = "$PREV"
check "downloads exactly as before" test "$(dl_state)" = "$dl_before"
check "versions/$SHA left for a look" test -x "$S/opt/shfd/versions/$SHA/shfd"
check "old versions untouched by a failed deploy" test -d "$S/opt/shfd/versions/$OLD1"
check "data untouched" test "$(cat "$S/data/sentinel.txt")" = "member data"

# --- 3. successful deploy and cleanup ---------------------------------------------------------

section "3. deploy $SHA"
if [[ ${DEPLOY_TEST_RUN_TESTS:-0} == 1 ]]; then deploy --ref "$REF"; else deploy --ref "$REF" --skip-tests; fi
check "deploy exits 0" test "$RC" = 0
check "report says OK" grep -q "): OK" "$T/out"
check "shfd current -> versions/$SHA" test "$(target_of shfd)" = "versions/$SHA"
check "shfd-gui current -> versions/$SHA" test "$(target_of shfd-gui)" = "versions/$SHA"
check "shfd answers $SHA" test "$(version_of "http://127.0.0.1:$FAKE_SHFD_PORT/healthz")" = "$SHA"
check "shfd-gui answers $SHA" test "$(version_of "http://127.0.0.1:$FAKE_GUI_PORT/gui/api/healthz")" = "$SHA"
check "shfd-thumbs runs again" bash -c "'$FAKECTL' is-active shfd-thumbs >/dev/null"
for p in shfd shfd-gui; do
	check "$p versions/: $SHA, $OLD2 (newest 2) and $PREV (previous)" \
		test "$(find "$S/opt/$p/versions" -mindepth 1 -maxdepth 1 -printf '%f\n' | sort | tr '\n' ' ')" = "$(printf '%s\n' "$SHA" "$OLD2" "$PREV" | sort | tr '\n' ' ')"
done
pk=$(find "$S/downloads" -maxdepth 1 -name 'shared-folder-*.tar.gz' -printf '%f ')
check "downloads: only packages of $SHA ($pk)" bash -c "[[ -n '$pk' && '$pk' != *$PREV* ]] && ! ls '$S/downloads' | grep -v -e '^shared-folder-$SHA-' -e '^SHA256SUMS\$' -e '^README.txt\$'"
check "downloads: a file that is not a package stays" test -f "$S/downloads/README.txt"
check "SHA256SUMS lists exactly the packages" bash -c "cd '$S/downloads' && sha256sum -c --quiet SHA256SUMS && test \$(wc -l <SHA256SUMS) = \$(ls shared-folder-*.tar.gz | wc -l)"
check "/v1/downloads lists $SHA with checksums" bash -c "curl -fsS http://127.0.0.1:$FAKE_SHFD_PORT/v1/downloads | grep -q '\"version\":\"$SHA\".*\"sha256\":\"'"
check "no temp dir left on the server" bash -c "! ls -d ${TMPDIR:-/tmp}/shfd-deploy.* 2>/dev/null | grep -v -e shfd-deploy-test >/dev/null"
check "data untouched" test "$(cat "$S/data/sentinel.txt")" = "member data"

# --- 4. same commit again: only checks --------------------------------------------------------------

section "4. $SHA again (shfd, gui): nothing to deploy"
before=$(snapshot)
deploy --ref "$REF" --only shfd --only gui
check "exits 0" test "$RC" = 0
check "says already deployed" grep -q "OK, already deployed" "$T/out"
check "checked the GUI's version too" grep -q "gui/api/healthz: ok, version $SHA" "$T/out"
check "nothing changed" test "$(snapshot)" = "$before"

# --- 5. usage ---------------------------------------------------------------------------------------------

section "5. bad usage"
deploy --only nonsense
check "unknown --only part exits 2" test "$RC" = 2
deploy --ref no-such-ref-xyz
check "unknown ref exits 2" test "$RC" = 2

printf '\n%d passed, %d failed\n' "$pass" "${#fails[@]}"
for f in "${fails[@]}"; do printf '  FAIL %s\n' "$f"; done
((${#fails[@]} == 0))
