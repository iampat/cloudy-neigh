"""Query latency benchmark for the cloudy-neigh demo."""

import logging
import random
import statistics
import time

from absl import app, flags
import grpc
import pyarrow.parquet as pq

from proto.cloudyneigh.v1 import index_pb2, index_pb2_grpc

FLAGS = flags.FLAGS
flags.DEFINE_string(
    "data_dir",
    "datasets/cohere-wikipedia",
    "Directory containing Parquet files",
)
flags.DEFINE_string("parquet", "en/0000.parquet", "Parquet file for query vectors")
flags.DEFINE_string("target", "localhost:50052", "Target query address")
flags.DEFINE_string("namespace", "main", "Target namespace")
flags.DEFINE_integer("queries", 100, "Number of timed queries")
flags.DEFINE_integer("warmup", 10, "Number of warmup queries")
flags.DEFINE_integer("top_k", 10, "top_k per query")
flags.DEFINE_string("filter_lang", "", "Optional lang equality filter")

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
) -> tuple[list[float], int]:
    latencies: list[float] = []
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
        resp = stub.Query(req)
        latencies.append(time.perf_counter() - start)
        hits = len(resp.hits)
        if i == 0 and resp.hits:
            rec = resp.hits[0].record
            logger.info(
                "first hit: id=%s attrs=%s",
                rec.id,
                {k: v.string_value[:40] for k, v in rec.attributes.items()},
            )
    return latencies, hits


def report(label: str, latencies: list[float]) -> None:
    ms = sorted(x * 1000 for x in latencies)
    n = len(ms)
    logger.info(
        "%s: n=%d mean=%.2fms p50=%.2fms p95=%.2fms p99=%.2fms max=%.2fms",
        label,
        n,
        statistics.fmean(ms),
        ms[n // 2],
        ms[int(n * 0.95) - 1],
        ms[int(n * 0.99) - 1],
        ms[-1],
    )


def main(argv: list[str]) -> None:
    del argv  # Unused.
    random.seed(7)
    path = f"{FLAGS.data_dir}/{FLAGS.parquet}"
    vectors = load_query_vectors(path, max(FLAGS.queries, FLAGS.warmup))
    logger.info("Loaded %d query vectors from %s", len(vectors), path)

    with grpc.insecure_channel(FLAGS.target) as channel:
        stub = index_pb2_grpc.QueryServiceStub(channel)

        run_queries(
            stub, vectors, FLAGS.warmup, FLAGS.top_k, FLAGS.namespace, FLAGS.filter_lang
        )
        latencies, last_hits = run_queries(
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
    report(label, latencies)
    logger.info("last response hits: %d", last_hits)


if __name__ == "__main__":
    app.run(main)
