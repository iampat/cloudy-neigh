# Usage: microbench.sh mac|<machine>
# Runs the tests, then the pinned Go benchmarks and the Scan1M CPU profiles. Skips a machine with BENCH_DONE.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
m=${1-}
check_machine "$m"
marker=$RUN_DIR/$m/BENCH_DONE
[ ! -f "$marker" ] || { log "$m: microbench done already"; exit 0; }
if [ "$m" = mac ]; then
  bash "$SCRIPTS/host/bench.sh" "$RUN_DIR" mac
else
  ZONE=$(vm_zone_or_die "$m")
  export ZONE
  vm_bg "$m" bench host/bench.sh "$REMOTE_RUN" "$m"
  vm_wait "$m" bench
  vm_pull "$m" "$m" "$RUN_DIR"
fi
touch "$marker"
