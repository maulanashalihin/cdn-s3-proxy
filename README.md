# CDN S3 Proxy

Go Fiber reverse proxy with disk caching for Wasabi (or any S3-compatible) storage. Simple URL structure — no bucket prefix in the path, just your object keys directly.

## How it works

```
Client → Cloudflare → :7999 → [disk cache] → Wasabi (S3 auth)
```

Every request is fetched from Wasabi with proper AWS Signature V4, cached to disk, and served. Subsequent requests hit the local cache (sub-millisecond).

## Features

- **S3 auth** — AWS Signature V4 via official SDK
- **Disk cache** — 7-day TTL, SHA256-keyed, metadata in JSON
- **Range requests** — video/images partial content
- **ETag / 304** — conditional requests, saves bandwidth
- **`X-Cache: HIT/MISS`** — debug headers

## Requirements

- Go 1.21+
- Wasabi (or S3-compatible) bucket
- systemd (optional, for auto-restart)

## Quick start

### Opsi A: Package manager (Ubuntu/Debian)

_TBD — future APT repo_

### Opsi B: Install dari release (recommended)

```bash
# 1. Download binary terbaru
curl -L -o cdn-proxy https://github.com/maulanashalihin/cdn-s3-proxy/releases/download/v1.0.0/cdn-proxy
chmod +x cdn-proxy

# 2. Pindahkan ke PATH
sudo mv cdn-proxy /usr/local/bin/

# 3. Buat config
sudo mkdir -p /etc/cdn-proxy /var/cache/cdn-proxy
sudo chown root:root /etc/cdn-proxy
sudo chmod 600 /etc/cdn-proxy

# 4. Isi .env
sudo tee /etc/cdn-proxy/.env << 'EOF'
WASABI_ACCESS_KEY=your_access_key
WASABI_SECRET_KEY=your_secret_key
WASABI_BUCKET=your_bucket
WASABI_REGION=ap-southeast-1
WASABI_ENDPOINT=https://s3.ap-southeast-1.wasabisys.com
CACHE_DIR=/var/cache/cdn-proxy
EOF

# 5. Download systemd unit
sudo curl -o /etc/systemd/system/cdn-proxy.service \
  https://raw.githubusercontent.com/maulanashalihin/cdn-s3-proxy/main/cdn-proxy.service

# 6. Start
sudo systemctl daemon-reload
sudo systemctl enable --now cdn-proxy
```

### Opsi C: Build dari source (pake Go)

```bash
git clone https://github.com/maulanashalihin/cdn-s3-proxy.git
cd cdn-s3-proxy
cp .env.example .env
# edit .env with your Wasabi credentials
go build -o cdn-proxy .
./cdn-proxy
```

## Configuration

All via environment variables (`.env`):

| Variable | Description | Default |
|----------|-------------|--------|
| `WASABI_ACCESS_KEY` | S3 access key | — |
| `WASABI_SECRET_KEY` | S3 secret key | — |
| `WASABI_BUCKET` | Bucket name | — |
| `WASABI_REGION` | Region | `ap-southeast-1` |
| `WASABI_ENDPOINT` | S3 endpoint URL | — |
| `CACHE_DIR` | Disk cache directory | `./cache` |

## URL mapping

The proxy maps the request path directly to the S3 object key in your configured bucket. No bucket prefix needed.

```
Old (with bucket prefix):   https://cdn.example.com/bucketname/robots.txt
New (direct key):           https://cdn.example.com/robots.txt
```

The configured bucket is resolved from `WASABI_BUCKET` env — not from the URL path.

## Endpoints

| Path | Description |
|------|-------------|
| `/*` | Proxy & cache any S3 object |
| `/health` | Health check |

## Deployment behind Cloudflare

1. Point your CDN domain to the server (A record)
2. Set up an Origin Rule in Cloudflare → `cdn.example.com/*` → port `7999`
3. (Optional) Add a Cache Rule to cache static assets at the edge

## License

MIT
