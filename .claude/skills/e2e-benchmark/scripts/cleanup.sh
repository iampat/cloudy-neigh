# Usage: cleanup.sh
# Deletes the firewall rules and the VMs of this run. Safe to run twice.
set -uo pipefail
. "$(dirname "$0")/lib.sh"
for m in $VMS; do
  bash "$SCRIPTS/firewall.sh" close "$m"
done
gc compute instances list --filter="labels.cloudy-e2e=$RUN_NAME" --format='value(name,zone.basename())' |
  while read -r name zone; do
    log "delete $name in $zone"
    gc compute instances delete "$name" --zone="$zone" --quiet </dev/null
  done
