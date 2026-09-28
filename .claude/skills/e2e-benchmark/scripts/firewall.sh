# Usage: firewall.sh open|close <machine>
# Opens port 50052 on one VM to the public IP of this Mac only.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
action=${1-} m=${2-}
is_vm "$m" || die "usage: firewall.sh open|close <one of: $VMS>"
rule=$(vm_name "$m")-querybench

case "$action" in
  open)
    ip=$(curl -fsS --max-time 10 https://api.ipify.org) || die "cannot find the public IP of this Mac"
    if gc compute firewall-rules describe "$rule" >/dev/null 2>&1; then
      gc compute firewall-rules update "$rule" --source-ranges="$ip/32" >/dev/null
    else
      gc compute firewall-rules create "$rule" --network=default --direction=INGRESS \
        --allow=tcp:50052 --source-ranges="$ip/32" --target-tags="$(vm_name "$m")" >/dev/null
    fi
    log "firewall $rule open to $ip"
    ;;
  close)
    if gc compute firewall-rules describe "$rule" >/dev/null 2>&1; then
      gc compute firewall-rules delete "$rule" --quiet >/dev/null
      log "firewall $rule deleted"
    fi
    ;;
  *) die "usage: firewall.sh open|close <machine>" ;;
esac
