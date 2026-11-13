#!/usr/bin/env python3
"""构造 Docker schema2 镜像层 / config / manifest / 多架构 index。

纯标准库，不需要 Docker 守护进程。产物交给 push.sh 通过 Registry v2 API 上传。
"""
import argparse
import gzip
import hashlib
import io
import json
import pathlib
import tarfile
import time

ENV = [
    "PORT=21087",
    "HOST=0.0.0.0",
    "DATA_DIR=/data",
    "PUBLIC_DIR=/app/www",
    "TZ=Asia/Shanghai",
    "PUID=0",
    "PGID=0",
    "UMASK=022",
    "MAX_AUDIO_MB=0",
    "MAX_EXPORT_MB=0",
    "MAX_EXPORT_ITEMS=0",
    "RECORD_FORMAT=wav",
    "DOWNLOAD_FORMAT=wav",
    "ENABLE_HTTPS=0",
    "TLS_SAN=localhost",
    "HTTPS_REDIRECT_PORT=0",
]
LABELS = {
    "org.opencontainers.image.title": "Voice Memo Suite",
    "org.opencontainers.image.description": "voice memo: record, playback, transcribe, summarize, search, export",
    "org.opencontainers.image.version": "0.2.1",
    "org.opencontainers.image.licenses": "MIT",
}
LAYER_MT = "application/vnd.docker.image.rootfs.diff.tar.gzip"
CONFIG_MT = "application/vnd.docker.container.image.v1+json"
MANIFEST_MT = "application/vnd.docker.distribution.manifest.v2+json"
INDEX_MT = "application/vnd.docker.distribution.manifest.list.v2+json"


def digest(data: bytes) -> str:
    return "sha256:" + hashlib.sha256(data).hexdigest()


def dump(obj) -> bytes:
    return json.dumps(obj, separators=(",", ":"), ensure_ascii=False).encode("utf-8")


def add_dir(tar, name, mode=0o755, uid=0, gid=0):
    ti = tarfile.TarInfo(name)
    ti.type = tarfile.DIRTYPE
    ti.mode, ti.uid, ti.gid, ti.mtime = mode, uid, gid, 0
    tar.addfile(ti)


def add_file(tar, name, data, mode=0o644, uid=0, gid=0):
    ti = tarfile.TarInfo(name)
    ti.size, ti.mode, ti.uid, ti.gid, ti.mtime = len(data), mode, uid, gid, 0
    tar.addfile(ti, io.BytesIO(data))


def build_layer(binary, www, ca, out_path):
    raw = io.BytesIO()
    with tarfile.open(fileobj=raw, mode="w", format=tarfile.USTAR_FORMAT) as tar:
        for d in ("app/", "app/www/", "etc/", "etc/ssl/", "etc/ssl/certs/", "data/", "tmp/"):
            add_dir(tar, d, mode=0o1777 if d == "tmp/" else 0o755)
        add_file(tar, "app/voice-memo", pathlib.Path(binary).read_bytes(), 0o755)
        for name in ("index.html", "styles.css", "app.js"):
            add_file(tar, "app/www/" + name, pathlib.Path(www, name).read_bytes())
        add_file(tar, "etc/ssl/certs/ca-certificates.crt", pathlib.Path(ca).read_bytes())
        add_file(tar, "data/.keep", b"")
    layer_tar = raw.getvalue()
    gz = gzip.compress(layer_tar, mtime=0)
    pathlib.Path(out_path).write_bytes(gz)
    return digest(gz), digest(layer_tar), len(gz)


def build_config(arch, diff_id, created):
    return dump({
        "architecture": arch,
        "os": "linux",
        "created": created,
        "config": {
            "Env": ENV,
            "Entrypoint": ["/app/voice-memo"],
            "WorkingDir": "/app",
            "ExposedPorts": {"21087/tcp": {}},
            "Volumes": {"/data": {}},
            "Labels": LABELS,
            "StopSignal": "SIGTERM",
            "Healthcheck": {
                "Test": ["CMD", "/app/voice-memo", "--health"],
                "Interval": 30_000_000_000,
                "Timeout": 5_000_000_000,
                "StartPeriod": 10_000_000_000,
                "Retries": 3,
            },
        },
        "rootfs": {"type": "layers", "diff_ids": [diff_id]},
        "history": [{"created": created, "created_by": "voice-memo static binary + www"}],
    })


def build_manifest(config_bytes, config_digest, layer_digest, layer_size):
    return dump({
        "schemaVersion": 2,
        "mediaType": MANIFEST_MT,
        "config": {"mediaType": CONFIG_MT, "size": len(config_bytes), "digest": config_digest},
        "layers": [{"mediaType": LAYER_MT, "size": layer_size, "digest": layer_digest}],
    })


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--root", required=True, help="项目根目录（含 backend/ 与 www/）")
    ap.add_argument("--out", required=True, help="产物目录")
    ap.add_argument("--tag", default="0.2.1")
    ap.add_argument("--amd64", required=True, help="linux/amd64 静态二进制")
    ap.add_argument("--arm64", required=True, help="linux/arm64 静态二进制")
    ap.add_argument("--ca", default="", help="CA bundle（PEM）")
    args = ap.parse_args()

    root = pathlib.Path(args.root)
    out = pathlib.Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    ca = args.ca
    if not ca:
        for candidate in ("/etc/ssl/cert.pem", "/etc/ssl/certs/ca-certificates.crt"):
            if pathlib.Path(candidate).exists():
                ca = candidate
                break
    if not ca:
        raise SystemExit("找不到 CA bundle，请用 --ca 指定")

    created = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    uploads = []
    index_entries = []

    for arch, binary in (("amd64", args.amd64), ("arm64", args.arm64)):
        layer_path = out / f"layer-{arch}.tar.gz"
        layer_digest, diff_id, layer_size = build_layer(binary, root / "www", ca, layer_path)
        uploads.append(("blob", layer_digest, str(layer_path), "application/octet-stream", "-"))

        config_bytes = build_config(arch, diff_id, created)
        config_digest = digest(config_bytes)
        config_path = out / f"config-{arch}.json"
        config_path.write_bytes(config_bytes)
        uploads.append(("blob", config_digest, str(config_path), "application/octet-stream", "-"))

        manifest_bytes = build_manifest(config_bytes, config_digest, layer_digest, layer_size)
        manifest_digest = digest(manifest_bytes)
        manifest_path = out / f"manifest-{arch}.json"
        manifest_path.write_bytes(manifest_bytes)
        uploads.append(("manifest", manifest_digest, str(manifest_path), MANIFEST_MT, f"{args.tag}-{arch}"))

        index_entries.append({
            "mediaType": MANIFEST_MT,
            "size": len(manifest_bytes),
            "digest": manifest_digest,
            "platform": {"architecture": arch, "os": "linux"},
        })
        print(f"{arch}: layer={layer_size/1024/1024:.2f}MiB diffID={diff_id[7:19]}… config={config_digest[7:19]}… manifest={manifest_digest[7:19]}…")

    index_bytes = dump({
        "schemaVersion": 2,
        "mediaType": INDEX_MT,
        "manifests": index_entries,
    })
    index_path = out / "index.json"
    index_path.write_bytes(index_bytes)
    uploads.append(("index", digest(index_bytes), str(index_path), INDEX_MT, args.tag))

    tsv = "\n".join("\t".join(row) for row in uploads) + "\n"
    (out / "uploads.tsv").write_text(tsv, encoding="utf-8")
    print(f"index={digest(index_bytes)}  共 {len(uploads)} 个上传对象 -> {out / 'uploads.tsv'}")
    print(f"CA={ca}  created={created}")


if __name__ == "__main__":
    main()
