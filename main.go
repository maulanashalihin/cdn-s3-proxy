package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

type CacheMeta struct {
	ContentType   string `json:"content_type"`
	ContentLength int64  `json:"content_length"`
	ETag          string `json:"etag"`
	CachedAt      int64  `json:"cached_at"`
	TTL           int64  `json:"ttl"`
}

var (
	s3Client   *s3.Client
	bucket     string
	cacheDir   string
	defaultTTL int64 = 86400 * 7 // 7 days
)

func main() {
	bucket = os.Getenv("WASABI_BUCKET")
	if bucket == "" {
		log.Fatal("WASABI_BUCKET not set")
	}

	accessKey := os.Getenv("WASABI_ACCESS_KEY")
	secretKey := os.Getenv("WASABI_SECRET_KEY")
	endpoint := os.Getenv("WASABI_ENDPOINT")
	region := os.Getenv("WASABI_REGION")
	if region == "" {
		region = "ap-southeast-1"
	}

	cacheDir = os.Getenv("CACHE_DIR")
	if cacheDir == "" {
		cacheDir = "./cache"
	}

	// --- Init S3 client (auto-signs all requests) ---
	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		log.Fatalf("config load: %v", err)
	}

	s3Client = s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true // Wasabi pake path-style, bukan virtual-hosted
	})

	os.MkdirAll(cacheDir, 0755)

	// --- Fiber app ---
	app := fiber.New(fiber.Config{
		Concurrency: 256 * 1024,
		// Biar fiber gak nambahin charset sendiri ke Content-Type
		DisablePreParseMultipartForm: true,
	})

	app.Use(recover.New())
	app.Use(logger.New(logger.Config{
		Format: "[${time}] ${ip} ${status} ${latency} ${method} ${path} - ${resBody}${bytesReceived} ${bytesSent}\n",
	}))

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	// All other routes → proxy to Wasabi
	app.All("/*", handleRequest)

	addr := ":7999"
	log.Printf("🚀 CDN proxy starting on %s (bucket=%s, cache=%s)", addr, bucket, cacheDir)
	log.Fatal(app.Listen(addr))
}

func handleRequest(c *fiber.Ctx) error {
	path := c.Path()

	// Extract S3 key dari URL path
	// Format: /{bucket}/{key} atau /{bucket}//{key}
	key := extractS3Key(path)
	if key == "" {
		log.Printf("invalid path: %s", path)
		return c.Status(400).SendString("invalid path: /<bucket>/<key>")
	}

	// Optional: validate bucket name di path cocok
	if !strings.HasPrefix(c.Path(), "/"+bucket) {
		// Allow anyway — maybe path doesn't include bucket. Fallback ke configured bucket.
		// Tapi untuk case ini, path selalu diawali bucket name.
	}

	// --- Cache check ---
	cacheKey := sha256Hex(path)
	cacheFile := filepath.Join(cacheDir, cacheKey)
	metaFile := filepath.Join(cacheDir, cacheKey+".meta")

	if meta, err := readMeta(metaFile); err == nil {
		if time.Now().Unix() < meta.CachedAt+meta.TTL {
			// Cache HIT — serve langsung dari disk
			c.Set("Content-Type", meta.ContentType)
			c.Set("X-Cache", "HIT")
			c.Set("X-Cache-Age", fmt.Sprintf("%d", time.Now().Unix()-meta.CachedAt))

			// Handle ETag / If-None-Match
			if meta.ETag != "" && c.Get("If-None-Match") == meta.ETag {
				return c.SendStatus(fiber.StatusNotModified)
			}

			// Handle Range requests (penting buat video/image partial)
			rangeHeader := c.Get("Range")
			if rangeHeader != "" {
				return serveRange(c, cacheFile, meta, rangeHeader)
			}

			return c.SendFile(cacheFile)
		}
	}

	// --- Cache MISS — fetch dari Wasabi dengan S3 auth ---
	log.Printf("MISS: %s", path)

	obj, err := s3Client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		log.Printf("S3 error (key=%s): %v", key, err)
		return c.Status(502).JSON(fiber.Map{
			"error": "upstream fetch failed",
			"key":   key,
		})
	}
	defer obj.Body.Close()

	// Baca body
	body, err := io.ReadAll(obj.Body)
	if err != nil {
		log.Printf("read error: %v", err)
		return c.Status(502).SendString("read error")
	}

	// Metadata dari S3 response
	contentType := "application/octet-stream"
	if obj.ContentType != nil {
		contentType = *obj.ContentType
	}
	etag := ""
	if obj.ETag != nil {
		etag = strings.Trim(*obj.ETag, "\"")
	}

	// Simpan ke cache (async biar response cepet)
	go writeCache(cacheFile, metaFile, body, contentType, etag)

	// Serve ke client
	c.Set("Content-Type", contentType)
	c.Set("X-Cache", "MISS")
	if etag != "" {
		c.Set("ETag", etag)
	}

	// Handle Range request juga untuk first fetch (unlikely tp amankan)
	rangeHeader := c.Get("Range")
	if rangeHeader != "" {
		return serveRangeFromBytes(c, body, contentType, rangeHeader)
	}

	c.Set("Content-Length", strconv.Itoa(len(body)))
	return c.Send(body)
}

// extractS3Key extracts the S3 object key from the URL path.
// Input: /slugpost//assets/file.webp → output: //assets/file.webp (normalized to /assets/file.webp)
func extractS3Key(path string) string {
	path = strings.TrimPrefix(path, "/")
	// Cari separator pertama setelah bucket name
	idx := strings.Index(path, "/")
	if idx < 0 {
		return ""
	}
	// idx = end of bucket name
	// key = everything after bucket (including the leading slash)
	key := path[idx:]
	// Normalize double slashes
	for strings.Contains(key, "//") {
		key = strings.ReplaceAll(key, "//", "/")
	}
	return key
}

// --- Cache helpers ---

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func readMeta(path string) (*CacheMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m CacheMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func writeCache(cachePath, metaPath string, body []byte, contentType, etag string) {
	// Tulis file body
	if err := os.WriteFile(cachePath, body, 0644); err != nil {
		log.Printf("cache write error: %v", err)
		return
	}

	meta := CacheMeta{
		ContentType:   contentType,
		ContentLength: int64(len(body)),
		ETag:          etag,
		CachedAt:      time.Now().Unix(),
		TTL:           defaultTTL,
	}
	data, err := json.Marshal(meta)
	if err != nil {
		log.Printf("meta marshal error: %v", err)
		return
	}
	if err := os.WriteFile(metaPath, data, 0644); err != nil {
		log.Printf("meta write error: %v", err)
	}
}

// --- Range request handlers ---

func serveRange(c *fiber.Ctx, cacheFile string, meta *CacheMeta, rangeHeader string) error {
	// Buka file buat dapat ukuran
	stat, err := os.Stat(cacheFile)
	if err != nil {
		return c.Status(500).SendString("cache stat error")
	}

	start, end, ok := parseRange(rangeHeader, stat.Size())
	if !ok {
		c.Set("Content-Range", fmt.Sprintf("bytes */%d", stat.Size()))
		return c.Status(416).SendString("range not satisfiable")
	}

	// Baca partial file
	f, err := os.Open(cacheFile)
	if err != nil {
		return c.Status(500).SendString("cache read error")
	}
	defer f.Close()

	buf := make([]byte, end-start+1)
	_, err = f.ReadAt(buf, start)
	if err != nil {
		return c.Status(500).SendString("cache read error")
	}

	c.Set("Content-Type", meta.ContentType)
	c.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, stat.Size()))
	c.Set("Content-Length", strconv.Itoa(len(buf)))
	c.Set("X-Cache", "HIT")
	return c.Status(206).Send(buf)
}

func serveRangeFromBytes(c *fiber.Ctx, body []byte, contentType, rangeHeader string) error {
	total := int64(len(body))
	start, end, ok := parseRange(rangeHeader, total)
	if !ok {
		c.Set("Content-Range", fmt.Sprintf("bytes */%d", total))
		return c.Status(416).SendString("range not satisfiable")
	}

	chunk := body[start : end+1]
	c.Set("Content-Type", contentType)
	c.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
	c.Set("Content-Length", strconv.Itoa(len(chunk)))
	c.Set("X-Cache", "MISS")
	return c.Status(206).Send(chunk)
}

func parseRange(rangeHeader string, fileSize int64) (int64, int64, bool) {
	if !strings.HasPrefix(rangeHeader, "bytes=") {
		return 0, 0, false
	}
	rangeStr := strings.TrimPrefix(rangeHeader, "bytes=")
	parts := strings.SplitN(rangeStr, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}

	if parts[0] == "" {
		// Suffix range: -500 → last 500 bytes
		suffix, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || suffix <= 0 {
			return 0, 0, false
		}
		s := fileSize - suffix
		if s < 0 {
			s = 0
		}
		return s, fileSize - 1, true
	}

	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 || start >= fileSize {
		return 0, 0, false
	}

	if parts[1] == "" {
		return start, fileSize - 1, true
	}

	end, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || end < start || end >= fileSize {
		return start, fileSize - 1, true
	}

	return start, end, true
}
