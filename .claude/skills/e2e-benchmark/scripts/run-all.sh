# Usage: run-all.sh
# Runs every phase in order. Rerun it to resume: each phase skips the work it finished.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
mkdir -p "$RUN_DIR/logs"
L=$RUN_DIR/logs
s=$SCRIPTS

bash "$s/build.sh"
if [ "$KEEP_VMS" != 1 ]; then
  trap 'bash "$s/cleanup.sh"' EXIT
fi

pids=""
for m in $VMS; do
  (bash "$s/vm.sh" create "$m" && bash "$s/vm.sh" setup "$m" && bash "$s/microbench.sh" "$m") \
    >"$L/vm-$m.log" 2>&1 &
  pids="$pids $!"
  log "$m: create, setup and microbench in the background, log $L/vm-$m.log"
done

log "mac: microbench, load, e2e"
bash "$s/microbench.sh" mac >"$L/microbench-mac.log" 2>&1
bash "$s/load.sh" mac >"$L/load-mac.log" 2>&1
bash "$s/e2e.sh" mac >"$L/e2e-mac.log" 2>&1

for p in $pids; do
  wait "$p" || die "a VM phase failed: see $L/vm-*.log"
done

log "$LOADER: load into $GCS_STORE"
bash "$s/load.sh" "$LOADER" >"$L/load-$LOADER.log" 2>&1

pids=""
for m in $VMS; do
  bash "$s/e2e.sh" "$m" >"$L/e2e-$m.log" 2>&1 &
  pids="$pids $!"
  log "$m: e2e in the background, log $L/e2e-$m.log"
done
for p in $pids; do
  wait "$p" || die "an e2e phase failed: see $L/e2e-*.log"
done

bash "$s/report.sh"
