# Sourced by every script. Override any value with an environment variable.

GCP_ACCOUNT=${GCP_ACCOUNT:-amiri1982@gmail.com}
GCP_PROJECT=${GCP_PROJECT:-kentrolabs-ai}

RUN_NAME=${RUN_NAME:-$(date +%F)-e2e}
RUN_DIR=${RUN_DIR:-$REPO/bench/$RUN_NAME}

BUCKET=${BUCKET:-kentrolabs-ai-cloudy-neigh-bench}
TENANT=${TENANT:-cloudy}
NAMESPACE=${NAMESPACE:?set NAMESPACE to the experiment name, e.g. f32 or fp16}
GCS_STORE=${GCS_STORE:-gs://$BUCKET/$TENANT}
MAC_STORE=${MAC_STORE:-file://$HOME/cloudy-bench-data/$TENANT?create_dir=true}

DATA_DIR=${DATA_DIR:-$REPO/datasets/cohere-wikipedia}
HF_DATASET=${HF_DATASET:-CohereLabs/wikipedia-2023-11-embed-multilingual-v3}
HF_FILES=${HF_FILES:-en/0000.parquet en/0001.parquet en/0002.parquet en/0003.parquet en/0004.parquet en/0005.parquet en/0006.parquet de/0000.parquet es/0000.parquet fr/0000.parquet}
GROUNDTRUTH=${GROUNDTRUTH:-$REPO/bench/groundtruth-1000x100.jsonl}

BATCH_SIZE=${BATCH_SIZE:-1000}
SEGMENTS=${SEGMENTS:-1000}

QUERIES=${QUERIES:-300}
WARMUP=${WARMUP:-20}
TOP_KS=${TOP_KS:-10 100}
FILTERS=${FILTERS:-all en}

VMS=${VMS:-emr gnr}
LOADER=${LOADER:-gnr}
MAX_RUN=${MAX_RUN:-12h}
CREATE_ROUNDS=${CREATE_ROUNDS:-40}
POLL=${POLL:-30}
KEEP_VMS=${KEEP_VMS:-0}

VARIANTS_mac=${VARIANTS_mac:-simd}
PROFILE_mac=${PROFILE_mac:-simd}

VM_emr=${VM_emr:-e2e-emr}
PLATFORM_emr=${PLATFORM_emr:-Intel Emerald Rapids}
MODEL_emr=${MODEL_emr:-207}
SHAPES_emr=${SHAPES_emr:-n4-highmem-4,us-central1-a n4-highmem-4,us-central1-b n4-highmem-4,us-central1-f n4-highmem-4,us-east1-c n4-highmem-4,us-east1-b}
VARIANTS_emr=${VARIANTS_emr:-simd fp16}
PROFILE_emr=${PROFILE_emr:-simd fp16}

VM_gnr=${VM_gnr:-e2e-gnr}
PLATFORM_gnr=${PLATFORM_gnr:-Intel Granite Rapids}
MODEL_gnr=${MODEL_gnr:-173}
SHAPES_gnr=${SHAPES_gnr:-c4-highcpu-16,us-central1-f c4-highcpu-16,us-central1-a c4-highmem-8,us-central1-a c4-highmem-8,us-central1-f c4-standard-8,us-central1-a c4-standard-8,us-central1-f c4-standard-16,us-central1-a c4-standard-16,us-central1-f}
VARIANTS_gnr=${VARIANTS_gnr:-simd fp16}
PROFILE_gnr=${PROFILE_gnr:-simd fp16}
