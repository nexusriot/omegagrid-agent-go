#!/usr/bin/env python3
"""
Export a ChromaDB collection (id, document, metadata, embedding) to JSONL.

One-shot migration helper for the Go gateway switchover from the Python
sidecar's ChromaDB to chromem-go. Run this from any environment that has
the same chromadb version as the sidecar (use the sidecar container or
`pip install chromadb==0.5.5`).

Embeddings are exported verbatim, so no re-embedding is needed and there
is no risk of drift from a different embedding model version.

Example:
    python3 export.py \
        --persist /app/data/vector_db \
        --collection memories \
        --out /app/data/vector_db.jsonl
"""
from __future__ import annotations

import argparse
import json
import sys
from typing import Any, Dict, List

import chromadb
from chromadb.config import Settings


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--persist", required=True, help="ChromaDB persist directory (e.g. data/vector_db)")
    p.add_argument("--collection", default="memories", help="collection name")
    p.add_argument("--out", required=True, help="output JSONL path")
    p.add_argument("--batch", type=int, default=500, help="page size for col.get()")
    args = p.parse_args()

    client = chromadb.PersistentClient(
        path=args.persist,
        settings=Settings(anonymized_telemetry=False),
    )
    try:
        col = client.get_collection(name=args.collection)
    except Exception as e:
        print(f"error: collection {args.collection!r} not found in {args.persist!r}: {e}", file=sys.stderr)
        return 2

    total = col.count()
    print(f"exporting {total} records from collection={args.collection!r}", file=sys.stderr)

    written = 0
    offset = 0
    with open(args.out, "w", encoding="utf-8") as f:
        while True:
            batch: Dict[str, Any] = col.get(
                limit=args.batch,
                offset=offset,
                include=["embeddings", "documents", "metadatas"],
            )
            ids: List[str] = batch.get("ids") or []
            if not ids:
                break
            embs = batch.get("embeddings") or [None] * len(ids)
            docs = batch.get("documents") or [""] * len(ids)
            metas = batch.get("metadatas") or [{}] * len(ids)
            for i, mid in enumerate(ids):
                emb_raw = embs[i] if i < len(embs) else None
                if emb_raw is None:
                    print(f"warning: id={mid} has no embedding, skipping", file=sys.stderr)
                    continue
                rec = {
                    "id": mid,
                    "document": docs[i] if i < len(docs) else "",
                    "metadata": metas[i] if i < len(metas) and metas[i] is not None else {},
                    "embedding": [float(x) for x in emb_raw],
                }
                f.write(json.dumps(rec, ensure_ascii=False) + "\n")
                written += 1
            offset += len(ids)
            if len(ids) < args.batch:
                break

    print(f"wrote {written}/{total} records to {args.out}", file=sys.stderr)
    return 0 if written == total else 1


if __name__ == "__main__":
    sys.exit(main())
