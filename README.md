# Visioball API

Requires Go 1.27.1+, Docker Compose and FFmpeg.

From this directory:

```bash
docker compose up -d
go run ./src
```

Wait for storage to initialize; retry the Go command if startup fails.
The API runs at http://localhost:8080. Demo curl commands are in `requests/`.

Stop the API with Ctrl+C and storage with `docker compose stop`.
