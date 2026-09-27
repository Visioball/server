package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type server struct {
	db                *sql.DB
	objects           *minio.Client
	bucket, publicURL string
	uploads           chan struct{}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return errors.New("ffmpeg is required for audio validation")
	}
	db, err := sql.Open("postgres", env("DATABASE_URL", "postgres://visioball:visioball@localhost:5432/visioball?sslmode=disable"))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS games (id uuid PRIMARY KEY, body json NOT NULL)`)
	if err != nil {
		return err
	}
	endpoint, err := url.Parse(env("S3_ENDPOINT", "http://localhost:9000"))
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return errors.New("invalid S3_ENDPOINT")
	}
	objects, err := minio.New(endpoint.Host, &minio.Options{
		Creds:  credentials.NewStaticV4(env("S3_ACCESS_KEY", "visioball"), env("S3_SECRET_KEY", "visioball-local-secret"), ""),
		Secure: endpoint.Scheme == "https", Region: env("S3_REGION", "us-east-1"),
	})
	if err != nil {
		return err
	}
	s := &server{db: db, objects: objects, bucket: env("S3_BUCKET", "visioball"), publicURL: strings.TrimRight(env("S3_PUBLIC_URL", "http://localhost:9000/visioball"), "/"), uploads: make(chan struct{}, 2)}
	if !validURL(s.publicURL) || len(s.publicURL) > 1900 {
		return errors.New("S3_PUBLIC_URL must be an absolute HTTP or HTTPS URL")
	}
	// The dedicated demo bucket must already exist. Grant only anonymous object reads.
	policy, _ := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{"Effect": "Allow", "Principal": "*", "Action": []string{"s3:GetObject"}, "Resource": []string{"arn:aws:s3:::" + s.bucket + "/*"}}}})
	if err = objects.SetBucketPolicy(ctx, s.bucket, string(policy)); err != nil {
		return err
	}
	httpServer := &http.Server{Addr: env("LISTEN_ADDR", "127.0.0.1:8080"), Handler: s, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	stop, shutdown := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer shutdown()
	go func() {
		<-stop.Done()
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(c)
	}()
	log.Printf("API listening on %s", httpServer.Addr)
	err = httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
