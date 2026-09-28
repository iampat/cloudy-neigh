# Runs on a VM. Usage: host/setup.sh <machine>
set -euo pipefail
. "$(dirname "$0")/../lib.sh"
m=$1
out=$HOME/$REMOTE_RUN/$m
mkdir -p "$out"

sudo DEBIAN_FRONTEND=noninteractive apt-get update -q
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -q build-essential python3-venv git curl
sudo curl -fsSL -o /usr/local/bin/bazel \
  https://github.com/bazelbuild/bazelisk/releases/download/v1.29.0/bazelisk-linux-amd64
sudo chmod +x /usr/local/bin/bazel
rm -rf "$REPO" && mkdir -p "$REPO" && tar -xzf "$HOME/src.tar.gz" -C "$REPO"
lscpu >"$out/lscpu.txt"
lscpu -e=CPU,CORE,SOCKET,NODE >"$out/lscpu-e.txt"
free -g >"$out/free.txt"

python3 -m venv "$HOME/hf"
"$HOME/hf/bin/pip" install -q huggingface_hub==2.0.0
t0=$(date +%s)
read -ra files <<<"$HF_FILES"
"$HOME/hf/bin/hf" download "$HF_DATASET" "${files[@]}" --repo-type dataset --local-dir "$DATA_DIR"
echo "download_s $(($(date +%s) - t0))" >"$out/setup.txt"

t0=$(date +%s)
(cd "$REPO" && bazel build //scripts:querybench //scripts:demoload)
echo "build_s $(($(date +%s) - t0))" >>"$out/setup.txt"
