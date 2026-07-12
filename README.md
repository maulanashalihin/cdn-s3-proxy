# CDN S3 Proxy

Go Fiber reverse proxy with disk caching for Wasabi (or any S3-compatible) storage. Drop-in replacement for Bunny CDN — same URL structure, self-hosted.

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
- **Drop-in** — same URL path as Bunny CDN, just change hostname

## Requirements

- Go 1.21+
- Wasabi (or S3-compatible) bucket
- systemd (optional, for auto-restart)

## Quick start

```bash
# Clone
git clone https://github.com/maulanashalihin/cdn-s3-proxy.git
cd cdn-s3-proxy

# Configure
cp .env.example .env
# edit .env with your Wasabi credentials

# Build
go build -o cdn-proxy .

# Run (manual)
./cdn-proxy

# Or install as systemd service
sudo cp cdn-proxy.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now cdn-proxy
```

## Configuration

All via environment variables (`.env`):

| Variable | Description | Default |
|---|---|---|
| `WASABI_ACCESS_KEY` | S3 access key | — |
| `WASABI_SECRET_KEY` | S3 secret key | — |
| `WASABI_BUCKET` | Bucket name | — |
| `WASABI_REGION` | Region | `ap-southeast-1` |
| `WASABI_ENDPOINT` | S3 endpoint URL | — |
| `CACHE_DIR` | Disk cache directory | `./cache` |

## URL mapping

The proxy expects paths in the format `/{bucket}/{key}` (with optional double slash):

```
Bunny CDN:    https://driplab.b-cdn.net/bucketname//assets/file.webp
This proxy:   https://cdn.example.com/bucketname//assets/file.webp
```

Just change the hostname — path stays the same.

## Endpoints

| Path | Description |
|---|---|
| `/*` | Proxy & cache any S3 object |
| `/health` | Health check |

## Deployment behind Cloudflare

1. Point your CDN domain to the server (A record)
2. Set up an Origin Rule in Cloudflare → `cdn.example.com/*` → port `7999`
3. (Optional) Add a Cache Rule to cache static assets at the edge

## License

MIT
