package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/minio/minio-go/v7"
)

func validateMedia(ctx context.Context, b []byte, media string) error {
	if len(b) == 0 {
		return fmt.Errorf("empty file")
	}
	if media == "image/png" || media == "image/jpeg" {
		cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
		if err != nil || (media == "image/png" && format != "png") || (media == "image/jpeg" && format != "jpeg") {
			return fmt.Errorf("invalid image format")
		}
		if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 16_000_000 {
			return fmt.Errorf("image exceeds 16 megapixels")
		}
		_, _, err = image.Decode(bytes.NewReader(b))
		return err
	}
	format := "mp3"
	if media == "audio/wav" {
		format = "wav"
		if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" || uint64(binary.LittleEndian.Uint32(b[4:8]))+8 != uint64(len(b)) {
			return fmt.Errorf("invalid WAV signature or length")
		}
	} else if len(b) < 3 || !(string(b[:3]) == "ID3" || (b[0] == 0xff && b[1]&0xe0 == 0xe0)) {
		return fmt.Errorf("invalid MP3 signature")
	}
	file, err := os.CreateTemp("", "visioball-audio-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(b); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Force the demuxer, disallow external protocols, and decode the entire audio.
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-xerror", "-nostdin", "-threads", "1", "-max_alloc", "67108864", "-protocol_whitelist", "file", "-f", format, "-i", file.Name(), "-map", "0:a:0", "-f", "null", "-")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

func (s *server) upload(w http.ResponseWriter, r *http.Request) {
	media := r.Header.Get("Content-Type")
	ext := ""
	limit := int64(5 << 20)
	if r.URL.Path == "/image" {
		switch media {
		case "image/png":
			ext = "png"
		case "image/jpeg":
			ext = "jpg"
		}
	} else {
		limit = 20 << 20
		switch media {
		case "audio/mpeg":
			ext = "mp3"
		case "audio/wav":
			ext = "wav"
		}
	}
	if ext == "" || r.Header.Get("Content-Encoding") != "" {
		problem(w, 415, "Unsupported media type or content encoding.", "")
		return
	}
	select {
	case s.uploads <- struct{}{}:
		defer func() { <-s.uploads }()
	default:
		problem(w, 503, "Upload capacity is busy; try again later.", "")
		return
	}
	b, ok := readBody(w, r, limit)
	if !ok {
		return
	}
	if err := validateMedia(r.Context(), b, media); err != nil {
		problem(w, 422, "File is empty, corrupt, mismatched, or exceeds decoder limits.", "")
		return
	}
	key := uuid() + "." + ext
	options := minio.PutObjectOptions{ContentType: media, DisableMultipart: true}
	options.SetMatchETagExcept("*")
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if _, err := s.objects.PutObject(ctx, s.bucket, key, bytes.NewReader(b), int64(len(b)), options); err != nil {
		problem(w, 503, "Object storage is unavailable.", "")
		return
	}
	publicURL := s.publicURL + "/" + key
	w.Header().Set("Location", publicURL)
	reply(w, 201, map[string]any{"url": publicURL, "contentType": media, "sizeBytes": len(b)})
}
