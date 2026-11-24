#!/usr/bin/env bash
# deploy/remote.sh — the server side of deploy/deploy.sh (docs/deploy.md).
#
# deploy.sh never logs in to a shell on the server: every step is one call
#
#   ssh <target> "env K=V … bash -s -- <command> <args…>" < deploy/remote.sh
#
# (or the same `bash -s` on this machine when SSH_TARGET=local). The
# commands below are the only things a deploy does on the server.
#
#   preflight                      read-only: current versions, units, dirs
#   stage                          new temp dir; prints its path (deploy.sh
#                                  then untars the bundle into it)
#   install STAGE SHA COMPS        versions/<sha> (shfd, gui), packages
#   switch SHA COMPS               current -> versions/<sha>, atomically
#   restart UNITS                  systemctl restart, one unit at a time
#   check SHA SHFD_V GUI_V         local health checks (127.0.0.1), retried
#   rollback STAGE COMPS SHFD_PREV GUI_PREV UNITS
#   cleanup STAGE SHA COMPS SHFD_PREV GUI_PREV
#   discard STAGE                  remove STAGE (a deploy that stopped early)
#
# COMPS is a comma list of shfd, gui, downloads; "-" stands for "none" in
# any list argument. Configuration comes from the environment (set by
# deploy.sh from deploy/targets/<target>.env). With DRY_RUN=1 every command
# that would change something is printed instead of run; reading is still
# done. This script never touches data, databases, systemd units or nginx.

set -euo pipefail

: "${SHFD_PREFIX:?}" "${GUI_PREFIX:?}"
DOWNLOADS_DIR=${DOWNLOADS_DIR:-}
KEEP_VERSIONS=${KEEP_VERSIONS:-5}
SHFD_SERVICES=${SHFD_SERVICES:-shfd shfd-thumbs}
GUI_SERVICES=${GUI_SERVICES:-shfd-gui}
SHFD_LOCAL_URL=${SHFD_LOCAL_URL:-http://127.0.0.1:8443}
GUI_LOCAL_URL=${GUI_LOCAL_URL:-http://127.0.0.1:8444}
GUI_BASE=${GUI_BASE:-}
CHECK_TIMEOUT=${CHECK_TIMEOUT:-30}
DRY_RUN=${DRY_RUN:-0}
# SUDO and SYSTEMCTL may be words ("sudo -n"); split on purpose.
SUDO=${SUDO-sudo}
SYSTEMCTL=${SYSTEMCTL:-systemctl}
read -r -a sudo_cmd <<<"$SUDO"
read -r -a systemctl_cmd <<<"$SYSTEMCTL"

say() { printf '  [server] %s\n' "$*" >&2; }
die() { printf '  [server] ERROR: %s\n' "$*" >&2; exit 1; }

# run: a command that changes the server. Printed, and run unless DRY_RUN.
run() {
	printf '  [server]%s $' "$([[ $DRY_RUN == 1 ]] && echo ' (dry-run)')" >&2
	printf ' %q' "$@" >&2
	printf '\n' >&2
	[[ $DRY_RUN == 1 ]] && return 0
	"$@"
}
# srun: run as root (SUDO); SUDO="" runs as the login user.
srun() { run "${sudo_cmd[@]}" "$@"; }

has() { [[ ",$1," == *",$2,"* ]]; }
# list VAR WORDS: split WORDS into array VAR; "-" is the empty list.
list() {
	local -n _l=$1
	_l=()
	[[ $2 == - ]] || read -r -a _l <<<"$2"
}

prefix_of() {
	case $1 in
	shfd) echo "$SHFD_PREFIX" ;;
	gui) echo "$GUI_PREFIX" ;;
	*) die "unknown component $1" ;;
	esac
}

# A prefix we are about to rm -rf / mv under must look like one.
check_prefix() {
	[[ $1 == /* && $1 != / && $1 != *..* ]] || die "refusing prefix '$1': not an absolute path"
}

# The name a symlink current points to inside versions/: "abc1234" for
# versions/abc1234, "" for anything else (missing, not ours).
current_version() {
	local t
	t=$(readlink "$1/current" 2>/dev/null) || return 0
	t=${t%/}
	case $t in
	versions/* | "$1"/versions/*) echo "${t##*/}" ;;
	*) echo "?$t" ;;
	esac
}

unit_env() { # unit_env UNIT NAME: the value of Environment=NAME=… of UNIT
	local env kv
	env=$("${systemctl_cmd[@]}" show --property=Environment --value "$1" 2>/dev/null) || return 0
	for kv in $env; do
		[[ $kv == "$2="* ]] && { echo "${kv#*=}"; return 0; }
	done
	return 0
}

cmd_preflight() {
	local comps=$1 problems=() u t
	for t in curl tar readlink find sha256sum; do
		command -v "$t" >/dev/null || problems+=("command $t not found on the server")
	done
	local units=()
	has "$comps" shfd && read -r -a units <<<"$SHFD_SERVICES"
	has "$comps" gui && { read -r -a t <<<"$GUI_SERVICES"; units+=("${t[@]}"); }
	for u in "${units[@]}"; do
		if ! "${systemctl_cmd[@]}" cat "$u" >/dev/null 2>&1; then
			problems+=("systemd unit $u.service does not exist (first install is manual: docs/server-ssh.md, docs/gui.md)")
		fi
	done
	local c p v
	for c in shfd gui; do
		has "$comps" "$c" || continue
		p=$(prefix_of "$c")
		check_prefix "$p"
		v=$(current_version "$p")
		if [[ ! -d $p/versions ]]; then
			problems+=("$p/versions does not exist (first install is manual)")
		elif [[ -z $v ]]; then
			problems+=("$p/current is missing (first install is manual)")
		elif [[ $v == \?* ]]; then
			problems+=("$p/current -> ${v#\?} is not a directory under versions/")
		elif [[ ! -d $p/versions/$v ]]; then
			problems+=("$p/current -> versions/$v, which does not exist")
		fi
		echo "${c}_current=$v"
	done
	local shfd_unit gui_unit
	read -r shfd_unit _ <<<"$SHFD_SERVICES"
	read -r gui_unit _ <<<"$GUI_SERVICES"
	if has "$comps" shfd && ! "${systemctl_cmd[@]}" cat "$shfd_unit" 2>/dev/null | grep -q -- "$SHFD_PREFIX/current"; then
		say "warning: unit $shfd_unit does not mention $SHFD_PREFIX/current; is that the binary it runs?"
	fi
	if has "$comps" gui && ! "${systemctl_cmd[@]}" cat "$gui_unit" 2>/dev/null | grep -q -- "$GUI_PREFIX/current"; then
		say "warning: unit $gui_unit does not mention $GUI_PREFIX/current; is that what it runs?"
	fi
	# Unless the target says otherwise: the downloads directory shfd serves
	# and the GUI's base path, from the units. SHFD_GUI_BASE= (root) is a
	# value too, so unset ("-") and empty differ.
	if [[ -z $DOWNLOADS_DIR ]]; then
		DOWNLOADS_DIR=$(unit_env "$shfd_unit" SHFD_DOWNLOADS)
	fi
	echo "downloads_dir=$DOWNLOADS_DIR"
	if "${systemctl_cmd[@]}" show --property=Environment --value "$gui_unit" 2>/dev/null | tr ' ' '\n' | grep -q '^SHFD_GUI_BASE='; then
		echo "unit_gui_base=$(unit_env "$gui_unit" SHFD_GUI_BASE)"
	else
		echo "unit_gui_base=-"
	fi
	if has "$comps" downloads; then
		if [[ -z $DOWNLOADS_DIR ]]; then
			problems+=("DOWNLOADS_DIR is not set and the shfd unit has no SHFD_DOWNLOADS")
		elif [[ ! -d $DOWNLOADS_DIR ]]; then
			problems+=("downloads directory $DOWNLOADS_DIR does not exist")
		fi
	fi
	if ((${#problems[@]})); then
		for p in "${problems[@]}"; do printf '  [server] PROBLEM: %s\n' "$p" >&2; done
		exit 1
	fi
	return 0
}

cmd_stage() {
	if [[ $DRY_RUN == 1 ]]; then
		say "(dry-run) mktemp -d /tmp/shfd-deploy.XXXXXX"
		echo "/tmp/shfd-deploy.DRYRUN"
		return 0
	fi
	mktemp -d "${TMPDIR:-/tmp}/shfd-deploy.XXXXXX"
}

# install_dir PREFIX SHA SRC_CMD…: a new versions/<sha>, filled by SRC_CMD
# into versions/<sha>.new and renamed into place.
install_version() {
	local p=$1 sha=$2 kind=$3 src=$4
	check_prefix "$p"
	local dir=$p/versions/$sha new=$p/versions/$sha.new
	[[ $(current_version "$p") != "$sha" ]] || die "$p/current already is $sha"
	srun rm -rf -- "$new"
	srun install -d -m 0755 -- "$new"
	case $kind in
	shfd) srun install -m 0755 -- "$src" "$new/shfd" ;;
	gui) srun tar -x -z --no-same-owner -C "$new" -f "$src" ;;
	esac
	if [[ -e $dir ]]; then
		say "versions/$sha exists (not current): replacing it"
		srun rm -rf -- "$dir"
	fi
	srun mv -T -- "$new" "$dir"
	srun touch -- "$dir" # its mtime orders cleanup
	if [[ $DRY_RUN != 1 ]]; then
		case $kind in
		shfd) [[ -x $dir/shfd ]] || die "$dir/shfd is missing" ;;
		gui) [[ -x $dir/bin/shfd-gui && -f $dir/server.js ]] || die "$dir is not a shfd-gui build" ;;
		esac
	fi
	return 0
}

cmd_install() {
	local stage=$1 sha=$2 comps=$3
	[[ $sha =~ ^[0-9a-f]{4,40}$ ]] || die "bad version $sha"
	if has "$comps" shfd; then
		say "shfd -> $SHFD_PREFIX/versions/$sha"
		install_version "$SHFD_PREFIX" "$sha" shfd "$stage/shfd"
	fi
	if has "$comps" gui; then
		say "shfd-gui -> $GUI_PREFIX/versions/$sha"
		install_version "$GUI_PREFIX" "$sha" gui "$stage/shfd-gui.tgz"
	fi
	if has "$comps" downloads; then
		local dl=$DOWNLOADS_DIR f name
		say "packages -> $dl"
		# What rollback needs: the old SHA256SUMS and the old file list.
		run mkdir -p "$stage/prev"
		if [[ $DRY_RUN != 1 ]]; then
			find "$dl" -mindepth 1 -maxdepth 1 -name 'shared-folder-*.tar.gz' -printf '%f\n' >"$stage/prev/packages"
			if [[ -f $dl/SHA256SUMS ]]; then cp -p -- "$dl/SHA256SUMS" "$stage/prev/SHA256SUMS"; fi
		fi
		# Packages first (a hidden temp name is never served), the list
		# last: shf update trusts SHA256SUMS, so it may only name packages
		# that are already complete. Until the restart, clients still see
		# the old shfd and fetch the old package, so for now the list holds
		# both versions; cleanup writes the new one alone.
		for f in "$stage"/dist/shared-folder-*.tar.gz; do
			[[ -e $f || $DRY_RUN == 1 ]] || die "no packages in $stage/dist"
			name=${f##*/}
			srun install -m 0644 -- "$f" "$dl/.$name.tmp"
			srun mv -f -- "$dl/.$name.tmp" "$dl/$name"
		done
		if [[ $DRY_RUN != 1 ]]; then
			cat "$stage/dist/SHA256SUMS" >"$stage/SHA256SUMS.both"
			if [[ -f $stage/prev/SHA256SUMS ]]; then
				# keep old lines whose name the new list does not have
				awk 'NR == FNR { new[$2] = 1; next } !($2 in new)' \
					"$stage/dist/SHA256SUMS" "$stage/prev/SHA256SUMS" >>"$stage/SHA256SUMS.both"
			fi
		fi
		srun install -m 0644 -- "$stage/SHA256SUMS.both" "$dl/.SHA256SUMS.tmp"
		srun mv -f -- "$dl/.SHA256SUMS.tmp" "$dl/SHA256SUMS"
	fi
	return 0
}

# point PREFIX VERSION: current -> versions/VERSION, atomically (rename).
point() {
	local p=$1 v=$2
	check_prefix "$p"
	[[ -n $v && $v != */* && ($DRY_RUN == 1 || -d $p/versions/$v) ]] || die "no $p/versions/$v"
	srun ln -sfn -- "versions/$v" "$p/current.new"
	srun mv -T -- "$p/current.new" "$p/current"
}

cmd_switch() {
	local sha=$1 comps=$2
	has "$comps" shfd && point "$SHFD_PREFIX" "$sha"
	has "$comps" gui && point "$GUI_PREFIX" "$sha"
	return 0
}

cmd_restart() {
	local u rc=0 units
	list units "$1"
	for u in "${units[@]}"; do
		srun "${systemctl_cmd[@]}" restart "$u" || { say "restart $u failed"; rc=1; }
	done
	return $rc
}

# probe URL WANT: ok when URL answers 200 with "ok":true and, when WANT is
# not "-", "version":"WANT". Prints the body's version (or the error).
probe() {
	local body v
	body=$(curl -fsS --max-time 5 "$1" 2>&1) || { echo "unreachable: ${body//$'\n'/ }"; return 1; }
	v=$(sed -n 's/.*"version":"\([^"]*\)".*/\1/p' <<<"$body")
	[[ $body == *'"ok":true'* ]] || { echo "not ok: $body"; return 1; }
	[[ $2 == - || $v == "$2" ]] || { echo "version $v, want $2"; return 1; }
	echo "ok, version $v"
}

cmd_check() {
	local shfd_v=$1 gui_v=$2 deadline res rc
	if [[ $DRY_RUN == 1 ]]; then
		say "(dry-run) curl $SHFD_LOCAL_URL/healthz (version $shfd_v), $GUI_LOCAL_URL$GUI_BASE/api/healthz (version $gui_v), retried up to ${CHECK_TIMEOUT}s"
		return 0
	fi
	deadline=$((SECONDS + CHECK_TIMEOUT))
	local -A passed=()
	local name url want
	while :; do
		rc=0
		for name in shfd gui; do
			[[ -n ${passed[$name]:-} ]] && continue
			if [[ $name == shfd ]]; then
				url=$SHFD_LOCAL_URL/healthz want=$shfd_v
			else
				[[ $gui_v == skip ]] && continue
				url=$GUI_LOCAL_URL$GUI_BASE/api/healthz want=$gui_v
			fi
			if res=$(probe "$url" "$want"); then
				passed[$name]=1
				say "$url: $res"
			else
				rc=1
				last_err[$name]="$url: $res"
			fi
		done
		((rc == 0)) && return 0
		if ((SECONDS >= deadline)); then
			for name in "${!last_err[@]}"; do
				[[ -z ${passed[$name]:-} ]] && say "FAILED $name: ${last_err[$name]}"
			done
			return 1
		fi
		sleep 1
	done
}
declare -A last_err=()

cmd_rollback() {
	local stage=$1 comps=$2 shfd_prev=$3 gui_prev=$4 units=$5 rc=0
	has "$comps" shfd && { point "$SHFD_PREFIX" "$shfd_prev" || rc=1; }
	has "$comps" gui && { point "$GUI_PREFIX" "$gui_prev" || rc=1; }
	if has "$comps" downloads; then
		local dl=$DOWNLOADS_DIR f name
		if [[ -f $stage/prev/packages ]]; then
			# Packages this deploy added go; the ones that were there stay.
			for f in "$dl"/shared-folder-*.tar.gz; do
				[[ -e $f ]] || continue
				name=${f##*/}
				grep -qxF -- "$name" "$stage/prev/packages" || srun rm -f -- "$f" || rc=1
			done
			if [[ -f $stage/prev/SHA256SUMS ]]; then
				if ! srun install -m 0644 -- "$stage/prev/SHA256SUMS" "$dl/.SHA256SUMS.tmp" ||
					! srun mv -f -- "$dl/.SHA256SUMS.tmp" "$dl/SHA256SUMS"; then
					rc=1
				fi
			else
				srun rm -f -- "$dl/SHA256SUMS" || rc=1
			fi
		else
			say "no record of the old packages in $stage/prev; $dl left as it is"
		fi
	fi
	cmd_restart "$units" || rc=1
	return $rc
}

# prune PREFIX KEEP CURRENT PREV: versions/ keeps the KEEP newest (by
# mtime), and always CURRENT and PREV; leftovers *.new go too.
prune() {
	local p=$1 keep=$2 cur=$3 prev=$4 n=0 name
	check_prefix "$p"
	[[ -d $p/versions ]] || return 0
	# In a dry run the new version is not there yet; it would be the newest.
	[[ -d $p/versions/$cur ]] || n=1
	while read -r _ name; do
		[[ -n $name && $name != */* && $name != . && $name != .. ]] || continue
		if [[ $name == *.new ]]; then
			srun rm -rf -- "$p/versions/$name"
			continue
		fi
		n=$((n + 1))
		if [[ $name == "$cur" || $name == "$prev" ]] || ((n <= keep)); then
			continue
		fi
		say "remove old version $p/versions/$name"
		srun rm -rf -- "$p/versions/$name"
	done < <(find "$p/versions" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %f\n' | sort -rn)
}

cmd_cleanup() {
	local stage=$1 sha=$2 comps=$3 shfd_prev=$4 gui_prev=$5
	if ! [[ $KEEP_VERSIONS =~ ^[0-9]+$ ]] || ((KEEP_VERSIONS < 2)); then
		die "KEEP_VERSIONS must be 2 or more"
	fi
	has "$comps" shfd && prune "$SHFD_PREFIX" "$KEEP_VERSIONS" "$sha" "$shfd_prev"
	has "$comps" gui && prune "$GUI_PREFIX" "$KEEP_VERSIONS" "$sha" "$gui_prev"
	if has "$comps" downloads; then
		local dl=$DOWNLOADS_DIR f name
		srun install -m 0644 -- "$stage/dist/SHA256SUMS" "$dl/.SHA256SUMS.tmp"
		srun mv -f -- "$dl/.SHA256SUMS.tmp" "$dl/SHA256SUMS"
		for f in "$dl"/shared-folder-*.tar.gz "$dl"/.shared-folder-*.tar.gz.tmp; do
			[[ -e $f ]] || continue
			name=${f##*/}
			[[ $name == shared-folder-"$sha"-* ]] && continue
			say "remove old package $name"
			srun rm -f -- "$f"
		done
	fi
	cmd_discard "$stage"
}

cmd_discard() {
	local stage=$1
	[[ $stage == */shfd-deploy.* ]] || die "not a stage directory: $stage"
	run rm -rf -- "$stage"
}

cmd=${1:-}
shift || true
case $cmd in
preflight) cmd_preflight "$@" ;;
stage) cmd_stage ;;
install) cmd_install "$@" ;;
switch) cmd_switch "$@" ;;
restart) cmd_restart "$@" ;;
check) cmd_check "$@" ;;
rollback) cmd_rollback "$@" ;;
cleanup) cmd_cleanup "$@" ;;
discard) cmd_discard "$@" ;;
*) die "unknown command '$cmd'" ;;
esac
