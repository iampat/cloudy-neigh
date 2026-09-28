# Usage: vm.sh create|setup|delete <machine> | vm.sh list
set -euo pipefail
. "$(dirname "$0")/lib.sh"
action=${1-}
m=${2-}

cpu_model() {
  for _ in $(seq 1 12); do
    if vm_ssh "$m" "lscpu" >"$RUN_DIR/$m/lscpu.txt"; then
      awk -F: '/^[[:space:]]*Model:/ {gsub(/ /, "", $2); print $2}' "$RUN_DIR/$m/lscpu.txt"
      return 0
    fi
    sleep 10
  done
  return 1
}

create() {
  local name platform model round sz shape zone got
  name=$(vm_name "$m")
  platform=$(var PLATFORM "$m")
  model=$(var MODEL "$m")
  mkdir -p "$RUN_DIR/$m"
  if [ -n "$(vm_zone "$m")" ]; then
    log "$name exists"
    return 0
  fi
  for round in $(seq 1 "$CREATE_ROUNDS"); do
    for sz in $(var SHAPES "$m"); do
      shape=${sz%,*} zone=${sz#*,}
      log "round $round: $name $shape $zone"
      gc compute instances create "$name" --zone="$zone" --machine-type="$shape" \
        --min-cpu-platform="$platform" --image-family=debian-12 --image-project=debian-cloud \
        --boot-disk-size=60GB --boot-disk-type=hyperdisk-balanced --scopes=storage-full \
        --tags="$name" --labels="cloudy-e2e=$RUN_NAME" \
        --max-run-duration="$MAX_RUN" --instance-termination-action=DELETE \
        >>"$RUN_DIR/$m/create.log" 2>&1 || continue
      export ZONE=$zone
      got=$(cpu_model) || got=unknown
      if [ "$got" = "$model" ]; then
        echo "name $name" >"$RUN_DIR/$m/vm.txt"
        echo "shape $shape" >>"$RUN_DIR/$m/vm.txt"
        echo "zone $zone" >>"$RUN_DIR/$m/vm.txt"
        log "$name up: $shape $zone, CPU model $got"
        return 0
      fi
      log "$name has CPU model $got, not $model: delete it"
      gc compute instances delete "$name" --zone="$zone" --quiet >/dev/null 2>&1 || true
      unset ZONE
    done
    sleep 90
  done
  die "no capacity for $name after $CREATE_ROUNDS rounds"
}

setup() {
  [ -f "$RUN_DIR/src.tar.gz" ] || die "no $RUN_DIR/src.tar.gz: run build.sh"
  ZONE=$(vm_zone_or_die "$m")
  export ZONE
  if vm_ssh "$m" "test -e $REMOTE_RUN/jobs/setup.ok"; then
    log "$m: setup done already"
    return 0
  fi
  vm_ssh "$m" "rm -rf bin $REMOTE_SCRIPTS src.tar.gz && mkdir -p bin $REMOTE_RUN" || die "ssh to $m failed"
  vm_scp_to "$m" bin/ "$RUN_DIR"/linux/*
  vm_scp_to "$m" src.tar.gz "$RUN_DIR/src.tar.gz"
  vm_scp_to "$m" "$REMOTE_SCRIPTS" "$SCRIPTS"
  vm_bg "$m" setup host/setup.sh "$m"
  vm_wait "$m" setup
  vm_pull "$m" "$m" "$RUN_DIR"
}

case "$action" in
  create | setup)
    is_vm "$m" || die "usage: vm.sh $action <one of: $VMS>"
    "$action"
    ;;
  delete)
    is_vm "$m" || die "usage: vm.sh delete <one of: $VMS>"
    zone=$(vm_zone "$m")
    [ -z "$zone" ] || gc compute instances delete "$(vm_name "$m")" --zone="$zone" --quiet
    ;;
  list)
    gc compute instances list --filter="labels.cloudy-e2e:*" \
      --format='table(name,zone.basename(),machineType.basename(),status,labels.cloudy-e2e)'
    ;;
  *) die "usage: vm.sh create|setup|delete <machine> | vm.sh list" ;;
esac
