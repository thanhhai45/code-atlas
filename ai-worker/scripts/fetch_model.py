"""Download the ONNX build of all-MiniLM-L6-v2 that fastembed publishes on GCS.

Usage: python scripts/fetch_model.py [DEST]   (default: ./models/all-MiniLM-L6-v2)

Uses only the standard library so the Docker image needs no curl or apt-get.
"""

import io
import pathlib
import sys
import tarfile
import urllib.request

URL = "https://storage.googleapis.com/qdrant-fastembed/sentence-transformers-all-MiniLM-L6-v2.tar.gz"


def main() -> None:
    dest = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else "models/all-MiniLM-L6-v2")
    if (dest / "model.onnx").exists():
        print(f"model already in {dest}")
        return
    dest.mkdir(parents=True, exist_ok=True)
    with urllib.request.urlopen(URL, timeout=300) as resp:
        data = resp.read()
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as tar:
        for member in tar.getmembers():
            name = pathlib.PurePosixPath(member.name)
            # Skip the top-level directory and macOS resource forks (._*).
            if not member.isfile() or name.name.startswith("._") or len(name.parts) < 2:
                continue
            target = dest / pathlib.Path(*name.parts[1:])
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(tar.extractfile(member).read())
    if not (dest / "model.onnx").exists():
        sys.exit(f"download did not contain model.onnx: {URL}")
    print(f"model ready in {dest}")


if __name__ == "__main__":
    main()
