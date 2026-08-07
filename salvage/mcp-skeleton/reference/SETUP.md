# Setup

How to configure and run the service in its different modes. All configuration is
read from environment variables; a local `.env` file is loaded automatically at
startup (copy `.env.example` to `.env`). Real environment variables always win
over the file, so the same image works locally and in Docker.

## Prerequisites

| Need it for | Requirement |
| --- | --- |
| Everything | Go (see `go.mod` for the version) to build |
| Web search / scraping | Chrome or Chromium on the host/image (chromedp drives it) |
| Summarization | An OpenAI-compatible LLM endpoint (e.g. LM Studio) reachable from the service |
| `download-audio` | `yt-dlp` and `ffmpeg` on the PATH |
| `send-email` | SMTP credentials (e.g. a Gmail app password) |

## Build & run

```bash
go build -o go-api.exe .

./go-api.exe        # HTTP server on :8080
./go-api.exe mcp    # MCP server over stdio (for LM Studio etc.)
```

## All variables

| Variable | Default | Used by |
| --- | --- | --- |
| `LLM_BASE_URL` | `http://localhost:1234` | Summarization + `get-models` |
| `LLM_MODEL` | `local-model` | Model id used for summarization |
| `LLM_API_KEY` | _(none)_ | Bearer token for the LLM endpoint |
| `SMTP_HOST` | `smtp.gmail.com` | `send-email` |
| `SMTP_PORT` | `587` | `send-email` (`465` = implicit TLS) |
| `SMTP_USERNAME` | _(none)_ | `send-email` auth |
| `SMTP_PASSWORD` | _(none)_ | `send-email` auth |
| `SMTP_FROM` | _(`SMTP_USERNAME`)_ | `send-email` sender address |
| `YTDLP_PATH` | `yt-dlp` | `download-audio` executable |
| `DOWNLOAD_DIR` | `downloads` | `download-audio` output folder |
| `AUDIO_FORMAT` | `mp3` | `download-audio` output format |
| `DOWNLOAD_TIMEOUT_MIN` | `20` | `download-audio` per-job cap |

## Example configurations

### LM Studio on the same machine

```dotenv
LLM_BASE_URL=http://localhost:1234
LLM_MODEL=llama-3.1-8b-instruct
```

Not sure what to put in `LLM_MODEL`? Leave it at the default, start the service,
and call `get-models` (MCP) or `GET /models` to see what the endpoint serves, then
set the id you want. (See "Discovering models" below.)

### Remote / cloud OpenAI-compatible endpoint

```dotenv
LLM_BASE_URL=https://api.your-provider.com
LLM_MODEL=some-hosted-model
LLM_API_KEY=sk-...
```

### LLM running on the host, service in Docker

From inside a container, `localhost` is the container, not your machine. Point at
the host gateway:

```dotenv
LLM_BASE_URL=http://host.docker.internal:1234
```

### Email digests (Gmail)

Gmail needs an **app password** (not your account password), with 2FA enabled.

```dotenv
SMTP_HOST=smtp.gmail.com
SMTP_PORT=587
SMTP_USERNAME=you@gmail.com
SMTP_PASSWORD=your-16-char-app-password
SMTP_FROM=you@gmail.com
```

### Audio downloads

```dotenv
DOWNLOAD_DIR=/data/audio
AUDIO_FORMAT=mp3
DOWNLOAD_TIMEOUT_MIN=30
```

Make sure `yt-dlp` and `ffmpeg` are installed (in Docker, e.g.
`apt-get install -y yt-dlp ffmpeg`).

## MCP (LM Studio)

Point LM Studio's `mcp.json` at the binary. The summarizer model is chosen here,
not by the calling LLM:

```json
{
  "mcpServers": {
    "go-search": {
      "command": "C:\\path\\to\\go-api.exe",
      "args": ["mcp"],
      "env": {
        "LLM_MODEL": "your-small-summarizer-model",
        "SMTP_USERNAME": "you@gmail.com",
        "SMTP_PASSWORD": "your-app-password"
      }
    }
  }
}
```

## Discovering models

If you don't know which model id to configure, ask the endpoint:

```bash
curl localhost:8080/models
# { "base_url": "...", "current": "local-model", "models": ["llama-3.1-8b-instruct", ...] }
```

The same is available as the `get-models` MCP tool, so a model that finds itself
misconfigured can list the options and you can set `LLM_MODEL` accordingly.

## Overriding the model per request

`LLM_MODEL` is the default summarizer, but every summarizing endpoint/tool also
accepts an optional `model` to override it for a single call (HTTP query param
`model=`, MCP `model` argument). For example, summarize with a larger model just
this once:

```bash
curl "localhost:8080/search/google/summary?q=...&model=llama-3.3-70b-instruct"
```

Omit it to use `LLM_MODEL`.
