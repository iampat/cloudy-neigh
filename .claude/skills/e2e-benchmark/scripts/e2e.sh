# Usage: e2e.sh mac|<machine> [variant...]
# Runs one query server per variant, then the local client and, for a VM, the Mac client.
# A variant with a DONE marker is skipped, so a rerun resumes.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
m=${1-}
check_machine "$m"
shift
variants=${*:-$(var VARIANTS "$m")}

if [ "$m" = mac ]; then
  for v in $variants; do
    d=$RUN_DIR/e2e/mac/$v
    [ ! -f "$d/DONE" ] || { log "mac $v: done already"; continue; }
    bash "$SCRIPTS/host/serve.sh" "$RUN_DIR" mac "$v" "$MAC_STORE" mac
    bash "$SCRIPTS/host/stop.sh" "$RUN_DIR" mac "$v"
    touch "$d/DONE"
  done
  exit 0
fi

ZONE=$(vm_zone_or_die "$m")
export ZONE
ip=$(vm_ip "$m")
qb=$(built //scripts:querybench)
trap 'bash "$SCRIPTS/firewall.sh" close "$m"' EXIT
bash "$SCRIPTS/firewall.sh" open "$m"
for v in $variants; do
  d=$RUN_DIR/e2e/$m/$v
  [ ! -f "$d/DONE" ] || { log "$m $v: done already"; continue; }
  vm_bg "$m" "serve-$v" host/serve.sh "$REMOTE_RUN" "$m" "$v" "$GCS_STORE" vm
  vm_wait "$m" "serve-$v"
  scenarios "$qb" "$ip:50052" "$d/mac" ""
  vm_ssh "$m" "bash $REMOTE_SCRIPTS/host/stop.sh $REMOTE_RUN $m $v mac" || die "cannot stop the $v server"
  vm_pull "$m" "e2e/$m/$v" "$RUN_DIR/e2e/$m"
  touch "$d/DONE"
  log "$m $v: done"
done
