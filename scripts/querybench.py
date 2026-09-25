import json
import logging
import os
import random
import statistics
import time

from absl import app, flags
import grpc
import pyarrow.parquet as pq

from proto.cloudyneigh.v1 import index_pb2, index_pb2_grpc
from scripts.interceptor import TenantClientInterceptor

FLAGS = flags.FLAGS
flags.DEFINE_string(
    "data_dir",
    "datasets/cohere-wikipedia",
    "Directory containing Parquet files",
)
flags.DEFINE_string("parquet", "en/0000.parquet", "Parquet file for query vectors")
flags.DEFINE_string("target", "localhost:50052", "Target query address")
flags.DEFINE_string("namespace", "default", "Target namespace")
flags.DEFINE_string("tenant", "cloudy", "Target tenant")
flags.DEFINE_integer("queries", 100, "Number of timed queries")
flags.DEFINE_integer("warmup", 10, "Number of warmup queries")
flags.DEFINE_integer("top_k", 10, "top_k per query")
flags.DEFINE_string("filter_lang", "", "Optional lang equality filter")
flags.DEFINE_string("out", "", "Optional JSONL file for per-query measurements")

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%H:%M:%S",
)
logger = logging.getLogger("querybench")


def load_query_vectors(path: str, count: int) -> list[list[float]]:
    pf = pq.ParquetFile(path)
    batch = next(pf.iter_batches(batch_size=count, columns=["emb"]))
    return batch.to_pydict()["emb"]


def run_queries(
    stub: index_pb2_grpc.QueryServiceStub,
    vectors: list[list[float]],
    count: int,
    top_k: int,
    namespace: str,
    filter_lang: str,
) -> tuple[list[dict], int]:
    results: list[dict] = []
    hits = 0
    for i in range(count):
        req = index_pb2.QueryRequest(
            namespace=namespace,
            vector=vectors[i % len(vectors)],
            top_k=top_k,
        )
        if filter_lang:
            req.filter.field = "lang"
            req.filter.value.string_value = filter_lang
        start = time.perf_counter()
        resp, call = stub.Query.with_call(req)
        total = time.perf_counter() - start
        trailer = dict(call.trailing_metadata())
        results.append(
            {
                "query": i % len(vectors),
                "total_s": total,
                "server_s": int(trailer["server-time-us"]) / 1e6,
                "ids": [h.record.id for h in resp.hits],
                "scores": [h.score for h in resp.hits],
            }
        )
        hits = len(resp.hits)
        if i == 0 and resp.hits:
            rec = resp.hits[0].record
            logger.info(
                "first hit: id=%s attrs=%s",
                rec.id,
                {k: v.string_value[:40] for k, v in rec.attributes.items()},
            )
    return results, hits


def report(label: str, latencies: list[float]) -> None:
    if not latencies:
        logger.warning("%s: no completed queries", label)
        return
    ms = sorted(x * 1000 for x in latencies)
    n = len(ms)
    p50 = ms[max(0, min(n - 1, n // 2))]
    p90 = ms[max(0, min(n - 1, int(n * 0.90) - 1))]
    p95 = ms[max(0, min(n - 1, int(n * 0.95) - 1))]
    p99 = ms[max(0, min(n - 1, int(n * 0.99) - 1))]
    logger.info(
        "%s: n=%d mean=%.2fms p50=%.2fms p90=%.2fms p95=%.2fms p99=%.2fms max=%.2fms",
        label,
        n,
        statistics.fmean(ms),
        p50,
        p90,
        p95,
        p99,
        ms[-1],
    )


def main(argv: list[str]) -> None:
    del argv  # Unused.
    random.seed(7)
    base = os.environ.get("BUILD_WORKING_DIRECTORY", ".")
    if os.path.isabs(FLAGS.parquet):
        path = FLAGS.parquet
    elif os.path.isfile(os.path.join(base, FLAGS.parquet)):
        path = os.path.join(base, FLAGS.parquet)
    else:
        data_dir = (
            FLAGS.data_dir
            if os.path.isabs(FLAGS.data_dir)
            else os.path.join(base, FLAGS.data_dir)
        )
        path = os.path.join(data_dir, FLAGS.parquet)
    vectors = load_query_vectors(path, max(FLAGS.queries, FLAGS.warmup))
    logger.info("Loaded %d query vectors from %s", len(vectors), path)

    with grpc.insecure_channel(FLAGS.target) as channel:
        intercepted = grpc.intercept_channel(
            channel, TenantClientInterceptor(FLAGS.tenant)
        )
        stub = index_pb2_grpc.QueryServiceStub(intercepted)

        run_queries(
            stub,
            vectors,
            FLAGS.warmup,
            FLAGS.top_k,
            FLAGS.namespace,
            FLAGS.filter_lang,
        )
        results, last_hits = run_queries(
            stub,
            vectors,
            FLAGS.queries,
            FLAGS.top_k,
            FLAGS.namespace,
            FLAGS.filter_lang,
        )

    label = f"top_k={FLAGS.top_k}"
    if FLAGS.filter_lang:
        label += f" lang={FLAGS.filter_lang}"
    report(f"{label} total", [r["total_s"] for r in results])
    report(f"{label} server", [r["server_s"] for r in results])
    report(
        f"{label} client-server",
        [r["total_s"] - r["server_s"] for r in results],
    )
    if FLAGS.out:
        out = FLAGS.out if os.path.isabs(FLAGS.out) else os.path.join(base, FLAGS.out)
        with open(out, "w") as f:
            for r in results:
                f.write(json.dumps(r) + "\n")
        logger.info("Wrote %d measurements to %s", len(results), out)
    logger.info("last response hits: %d", last_hits)


if __name__ == "__main__":
    app.run(main)
