# Usage: load.sh mac|<machine>
# Loads the 1M corpus into the Mac store, or from the VM into the GCS store. Skips a machine with LOAD_DONE.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
m=${1-}
check_machine "$m"
marker=$RUN_DIR/$m/LOAD_DONE
[ ! -f "$marker" ] || { log "$m: load done already"; exit 0; }
if [ "$m" = mac ]; then
  bash "$SCRIPTS/host/load.sh" "$RUN_DIR" mac "$MAC_STORE"
else
  ZONE=$(vm_zone_or_die "$m")
  export ZONE
  gc storage buckets describe "gs://$BUCKET" --format='value(location)' >"$RUN_DIR/bucket-location.txt"
  vm_bg "$m" load host/load.sh "$REMOTE_RUN" "$m" "$GCS_STORE"
  vm_wait "$m" load
  vm_pull "$m" "$m" "$RUN_DIR"
fi
touch "$marker"
cat "$RUN_DIR/$m/load.txt"
