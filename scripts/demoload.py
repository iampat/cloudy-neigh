"""Dataset loader for cloudy-neigh customer demo."""

import glob
import logging
import os
import sys
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
flags.DEFINE_integer("batch_size", 1000, "Batch size for writes")
flags.DEFINE_integer("max_docs", None, "Maximum documents to stream")
flags.DEFINE_string("target", "localhost:50051", "Target ingest address")
flags.DEFINE_string("namespace", "main", "Target namespace")

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%H:%M:%S",
)
logger = logging.getLogger("demoload")


def send_batch(
    stub: index_pb2_grpc.IngestServiceStub,
    namespace: str,
    docs: list[index_pb2.Document],
) -> None:
    """Send a batch of documents to the cloudy-neigh ingestion service."""
    stub.Upsert(index_pb2.UpsertRequest(namespace=namespace, documents=docs))


def parse_lang_from_path(file_path: str) -> str:
    """Extract language code from the file path structure (e.g. datasets/en/0000.parquet)."""
    parent = os.path.basename(os.path.dirname(os.path.abspath(file_path)))
    if len(parent) in (2, 3) and parent.isalpha():
        return parent
    return "en"


def load_dataset(
    data_dir: str,
    batch_size: int = 1000,
    max_docs: int | None = None,
    target: str = "localhost:50051",
    namespace: str = "main",
) -> None:
    """Read Parquet files from data_dir and stream batches to the target service."""
    pattern = os.path.join(data_dir, "**", "*.parquet")
    files = sorted(glob.glob(pattern, recursive=True))

    if not files:
        logger.error("No Parquet files found in %s", data_dir)
        sys.exit(1)

    logger.info("Found %d Parquet file(s) in %s", len(files), data_dir)
    total = 0
    total_batches = 0
    start_time = time.time()

    with grpc.insecure_channel(target) as channel:
        stub = index_pb2_grpc.IngestServiceStub(channel)

        for file_path in files:
            lang = parse_lang_from_path(file_path)
            logger.info("Reading %s (lang=%s)...", file_path, lang)
            pf = pq.ParquetFile(file_path)

            for batch in pf.iter_batches(batch_size=batch_size):
                pydict = batch.to_pydict()
                n = len(pydict["_id"])
                docs: list[index_pb2.Document] = []

                for i in range(n):
                    docs.append(
                        index_pb2.Document(
                            id=str(pydict["_id"][i]),
                            vector=pydict["emb"][i],
                            attributes={
                                "url": str(pydict["url"][i] or ""),
                                "title": str(pydict["title"][i] or ""),
                                "text": str(pydict["text"][i] or ""),
                                "lang": lang,
                            },
                        )
                    )

                send_batch(stub, namespace, docs)
                total += n
                total_batches += 1

                if total_batches % 10 == 0:
                    elapsed = time.time() - start_time
                    rate = total / elapsed if elapsed > 0 else 0.0
                    logger.info(
                        "Streamed %d docs (%d batches) [%.0f docs/s]",
                        total,
                        total_batches,
                        rate,
                    )

                if max_docs and total >= max_docs:
                    logger.info("Reached limit of %d documents", max_docs)
                    break

            if max_docs and total >= max_docs:
                break

    elapsed = time.time() - start_time
    rate = total / elapsed if elapsed > 0 else 0.0
    logger.info(
        "Finished streaming %d documents in %.2fs (%.0f docs/s)",
        total,
        elapsed,
        rate,
    )


def main(argv: list[str]) -> None:
    del argv  # Unused.
    load_dataset(
        data_dir=FLAGS.data_dir,
        batch_size=FLAGS.batch_size,
        max_docs=FLAGS.max_docs,
        target=FLAGS.target,
        namespace=FLAGS.namespace,
    )


if __name__ == "__main__":
    app.run(main)
