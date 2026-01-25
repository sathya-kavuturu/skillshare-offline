# Skillshare Offline Downloader

A modern, robust downloader for Skillshare courses that supports the new Cloudflare Streams video platform (2025).

> [!NOTE]  
> This tool is for educational and archival purposes only. Please support the creators by purchasing a subscription.

## Features

- **Cloudflare Streams Support**: Handles the new video delivery system that broke legacy downloaders.
- **Parallel Downloads**: Fast, concurrent downloads (4 videos at once).
- **Project Resources**: Downloads included class resources (zip, pdf, etc.) and instructions.
- **Anti-Detection**: Built-in browser fingerprinting to bypass bot detection.
- **Clean Organization**: Videos named and numbered correctly (e.g., `01. Introduction.mp4`).
- **Resumable**: Skips files that are already downloaded.

## Installation

### Instant Install (Go)

If you have Go installed, you can install the tool directly:

```bash
go install github.com/xenking/skillshare-offline@latest
```

### Pre-built Binaries

Check the [Releases](https://github.com/xenking/skillshare-offline/releases) page for your operating system.

### Build from Source

**Requirements**:

- [Go](https://go.dev/dl/) 1.25+
- [ffmpeg](https://ffmpeg.org/download.html) (must be in your PATH)

```bash
git clone https://github.com/xenking/skillshare-offline.git
cd skillshare-offline
go build .
# or use make
make
```

### GitHub Actions

This repository includes a GitHub Actions workflow that automatically builds and releases binaries for Windows, Linux, and macOS whenever a new tag (e.g., `v1.0.0`) is pushed.

## Usage

1. **Login to Skillshare** in your browser.
2. **Export your cookies** to a `cookies.txt` file (using a browser extension like "Get cookies.txt LOCALLY").
3. **Run the downloader**:

```bash
# Basic usage
./skillshare-dl --url "class-url" --cookie-file cookies.txt

# Example
./skillshare-dl \
  --url "https://www.skillshare.com/en/classes/3d-modeling-for-beginners/123456" \
  --cookie-file cookies.txt \
  --output-dir ./downloads
```

### Options

| Flag               | Description                          | Default        |
| ------------------ | ------------------------------------ | -------------- |
| `--url`            | Full URL of the Skillshare class     | (Required)     |
| `--cookie-file`    | Path to Netscape-format cookies file | `cookies.txt`  |
| `--output-dir`     | Directory to save downloads          | `./downloaded` |
| `--skip-resources` | Skip downloading class resources     | `false`        |
| `--verbose`        | Enable debug logging                 | `false`        |

## Troubleshooting

- **403 Forbidden / Authentication Failed**: Your `cookies.txt` might be expired or invalid. Refresh the skillshare page and export fresh cookies.
- **ffmpeg not found**: Ensure `ffmpeg` is installed and available in your system terminal.
- **TLS Handshake Error**: If you see networking errors, ensure you are not blocking Cloudflare domains. The tool uses a Firefox fingerprint to blend in.

## License

MIT License. See [LICENSE](LICENSE) for details.

## Disclaimer

This software is provided "as is" without warranty of any kind. The authors are not responsible for any misuse.
