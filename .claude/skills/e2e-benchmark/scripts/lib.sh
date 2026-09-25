# Sourced by every script: config plus shared helpers. Runs on the Mac and on a VM.

SCRIPTS=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
if [ "$(uname)" = Linux ]; then
  REPO=${REPO:-$HOME/src}
else
  REPO=${REPO:-$(git -C "$SCRIPTS" rev-parse --show-toplevel)}
fi
. "$SCRIPTS/config.sh"

REMOTE_SCRIPTS=e2e-scripts
REMOTE_RUN=run

log() { echo "[$(date +%T)] $*" >&2; }
die() {
  log "ERROR: $*"
  exit 1
}

var() {
  local n="$1_$2"
  printf '%s' "${!n-}"
}

is_vm() {
  case " $VMS " in *" $1 "*) return 0 ;; esac
  return 1
}

check_machine() {
  [ "$1" = mac ] || is_vm "$1" || die "unknown machine '$1': use mac or one of: $VMS"
}

now() {
  if [ "$(uname)" = Linux ]; then date +%s.%N; else perl -MTime::HiRes=time -e 'printf "%.3f\n", time'; fi
}

bin_dir() {
  if [ "$(uname)" = Linux ]; then echo "$HOME/bin"; else echo "$1/mac/bin"; fi
}

# Prints "<server cpu> <client cpus>": one CPU on the highest core, and every CPU of the other cores.
# The SMT sibling of the server CPU stays idle. Prints nothing on the Mac, which cannot pin.
pin_layout() {
  [ "$(uname)" = Linux ] || return 0
  lscpu -p=CPU,CORE | awk -F, '
    !/^#/ { n++; cpu[n] = $1; core[n] = $2 + 0; if (core[n] > max) max = core[n] }
    END {
      for (i = 1; i <= n; i++)
        if (core[i] == max) { if (s == "") s = cpu[i] }
        else cl = cl (cl == "" ? "" : ",") cpu[i]
      print s, cl
    }'
}

pinned() {
  local cpus=$1
  shift
  if [ -n "$cpus" ]; then taskset -c "$cpus" "$@"; else "$@"; fi
}

segment_count() {
  local store=$1 path listing
  case "$store" in
    file://*)
      path=${store#file://}
      path=${path%%\?*}/ns/$NAMESPACE/segments
      if [ -d "$path" ]; then find "$path" -name '*.recordio' | wc -l | tr -d ' '; else echo 0; fi
      ;;
    gs://*)
      if ! listing=$(gc storage ls "$store/ns/$NAMESPACE/segments/" 2>&1); then
        case "$listing" in *"matched no objects"*) echo 0 && return ;; esac
        die "cannot list $store/ns/$NAMESPACE/segments/: $listing"
      fi
      awk '/\.recordio$/ { n++ } END { print n + 0 }' <<<"$listing"
      ;;
    *) die "unknown store '$store'" ;;
  esac
}

store_bytes() {
  local p
  case "$1" in
    gs://*) gc storage du -s "$1" | awk '{print $1}' ;;
    file://*)
      p=${1#file://}
      du -sk "${p%%\?*}" | awk '{print $1 * 1024}'
      ;;
  esac
}

write_tenants() {
  printf '{"tenants":{"%s":{"storage_url":"%s"}}}\n' "$TENANT" "$2" >"$1"
}

# Usage: built <target>. Builds the target and prints the absolute path of its executable.
built() {
  (cd "$REPO" && bazel build "$1" >/dev/null 2>&1) || die "cannot build $1"
  (cd "$REPO" && echo "$REPO/$(bazel cquery --output=files "$1" 2>/dev/null | grep -m1 '^bazel-out/')")
}

# Usage: scenarios <querybench> <target> <out dir> <client cpus>
scenarios() {
  local qb=$1 target=$2 out=$3 cpus=$4 k f lang
  mkdir -p "$out"
  for k in $TOP_KS; do
    for f in $FILTERS; do
      lang=""
      [ "$f" = en ] && lang=en
      log "client $target top$k-$f"
      pinned "$cpus" "$qb" --target="$target" --tenant="$TENANT" --namespace="$NAMESPACE" --data_dir="$DATA_DIR" --queries="$QUERIES" \
        --warmup="$WARMUP" --top_k="$k" --filter_lang="$lang" --out="$out/top$k-$f.jsonl" \
        >"$out/top$k-$f.log" 2>&1 || die "querybench failed: $out/top$k-$f.log"
    done
  done
}

gc() { gcloud "$@" --account="$GCP_ACCOUNT" --project="$GCP_PROJECT"; }

vm_name() { var VM "$1"; }

vm_zone() {
  gc compute instances list --filter="name=$(vm_name "$1")" --format='value(zone.basename())' 2>/dev/null
}

vm_zone_or_die() {
  local z
  z=$(vm_zone "$1")
  [ -n "$z" ] || die "VM $(vm_name "$1") does not exist: run vm.sh create $1"
  echo "$z"
}

vm_ip() {
  gc compute instances describe "$(vm_name "$1")" --zone="$(vm_zone_or_die "$1")" \
    --format='value(networkInterfaces[0].accessConfigs[0].natIP)'
}

vm_account() {
  gc compute instances describe "$(vm_name "$1")" --zone="${ZONE:-$(vm_zone_or_die "$1")}" \
    --format='value(serviceAccounts[0].email)'
}

vm_ssh() {
  local m=$1
  shift
  gc compute ssh "$(vm_name "$m")" --zone="${ZONE:-$(vm_zone_or_die "$m")}" --command="$*" 2>/dev/null
}

vm_scp_to() {
  local m=$1 dest=$2
  shift 2
  gc compute scp --recurse "$@" "$(vm_name "$m"):$dest" --zone="${ZONE:-$(vm_zone_or_die "$m")}" >/dev/null 2>&1 ||
    die "scp to $(vm_name "$m"):$dest failed"
}

# Usage: vm_pull <machine> <path under ~/run> <local parent dir>
vm_pull() {
  mkdir -p "$3"
  gc compute scp --recurse "$(vm_name "$1"):$REMOTE_RUN/$2" "$3/" --zone="${ZONE:-$(vm_zone_or_die "$1")}" \
    >/dev/null 2>&1 || die "scp from $(vm_name "$1"):$REMOTE_RUN/$2 failed"
}

remote_env() {
  local v
  for v in RUN_NAME TENANT NAMESPACE VMS QUERIES WARMUP TOP_KS FILTERS BATCH_SIZE SEGMENTS HF_DATASET HF_FILES; do
    printf '%s=%q ' "$v" "${!v-}"
  done
  for v in $VMS; do
    printf 'PROFILE_%s=%q ' "$v" "$(var PROFILE "$v")"
  done
}

# Usage: vm_bg <machine> <job> <script under scripts/> <args...>
# Starts the job detached on the VM. The job writes ~/run/jobs/<job>.ok or .fail when it stops.
vm_bg() {
  local m=$1 job=$2 cmd account
  shift 2
  account=$(vm_account "$m")
  [ -n "$account" ] || die "$(vm_name "$m") has no service account"
  cmd="GCP_ACCOUNT=$(printf '%q' "$account") $(remote_env) bash $REMOTE_SCRIPTS/$(printf '%q ' "$@")"
  cmd="$cmd && touch $REMOTE_RUN/jobs/$job.ok || touch $REMOTE_RUN/jobs/$job.fail"
  vm_ssh "$m" "mkdir -p $REMOTE_RUN/jobs && rm -f $REMOTE_RUN/jobs/$job.* &&
    (nohup bash -c $(printf '%q' "$cmd") >$REMOTE_RUN/jobs/$job.log 2>&1 </dev/null &)" ||
    die "cannot start $job on $(vm_name "$m")"
  log "$(vm_name "$m"): started $job"
}

vm_wait() {
  local m=$1 job=$2
  until vm_ssh "$m" "test -e $REMOTE_RUN/jobs/$job.ok -o -e $REMOTE_RUN/jobs/$job.fail"; do sleep "$POLL"; done
  if ! vm_ssh "$m" "test -e $REMOTE_RUN/jobs/$job.ok"; then
    vm_ssh "$m" "tail -20 $REMOTE_RUN/jobs/$job.log" >&2 || true
    die "$(vm_name "$m"): $job failed, log at ~/$REMOTE_RUN/jobs/$job.log"
  fi
  log "$(vm_name "$m"): $job done"
}
