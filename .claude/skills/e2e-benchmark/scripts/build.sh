# Usage: build.sh
# Builds the Mac and linux/amd64 binaries into $RUN_DIR, the Mac querybench, and the source archive.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
cd "$REPO"
targets=(//cmd/cloudy //vector:vector_test //query:query_test)
mac=(-c opt --@rules_go//go/config:race=false)
linux=("${mac[@]}" --platforms=@rules_go//go/toolchain:linux_amd64 --use_target_platform_for_tests)

copy() {
  local dest=$1 kind=$2 t f
  shift 2
  mkdir -p "$dest"
  log "build $kind"
  bazel build "$@" "${targets[@]}" >"$RUN_DIR/build-$kind.log" 2>&1 || die "build failed: $RUN_DIR/build-$kind.log"
  for t in "${targets[@]}"; do
    f=$(bazel cquery "$@" --output=files "$t" 2>/dev/null)
    case "$(file -L "$f")" in
      *"$kind"*) ;;
      *) die "$t is not $kind: $(file -L "$f")" ;;
    esac
    rm -f "$dest/$(basename "$f")"
    cp "$f" "$dest/"
    chmod u+w "$dest/$(basename "$f")"
  done
}
copy "$RUN_DIR/mac/bin" Mach-O "${mac[@]}"
copy "$RUN_DIR/linux" ELF "${linux[@]}"

log "build querybench and demoload"
bazel build //scripts:querybench //scripts:demoload >"$RUN_DIR/build-scripts.log" 2>&1 ||
  die "build failed: $RUN_DIR/build-scripts.log"

[ -z "$(git status --porcelain --untracked-files=no)" ] || log "warning: uncommitted changes stay off the VMs"
git archive --format=tar.gz -o "$RUN_DIR/src.tar.gz" HEAD
{
  echo "commit $(git rev-parse HEAD)"
  echo "branch $(git rev-parse --abbrev-ref HEAD)"
  echo "built $(date -u +%FT%TZ)"
} >"$RUN_DIR/build.txt"
sysctl -n machdep.cpu.brand_string hw.ncpu hw.memsize >"$RUN_DIR/mac/cpu.txt"
file "$RUN_DIR"/mac/bin/* "$RUN_DIR"/linux/* | cut -c1-120
