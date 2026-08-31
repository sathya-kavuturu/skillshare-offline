package cloudflare

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/grafov/m3u8"
	"github.com/sardanioss/httpcloak"
)

// Downloader handles downloading videos from Cloudflare Streams
type Downloader struct {
	httpClient  *httpcloak.Client
	concurrency int
}

// NewDownloader creates a new Cloudflare Stream downloader with Chrome-like fingerprinting
func NewDownloader() *Downloader {
	return &Downloader{
		httpClient:  httpcloak.New("firefox-133"),
		concurrency: 10,
	}
}

// Close releases resources used by the downloader
func (d *Downloader) Close() {
	if d.httpClient != nil {
		d.httpClient.Close()
	}
}

// Resolution represents an available video resolution
type Resolution struct {
	Name        string
	Resolution  string
	Bandwidth   uint32
	ManifestURL string
}

// DownloadVideo downloads a video from a Cloudflare Stream manifest URL
func (d *Downloader) DownloadVideo(ctx context.Context, manifestURL, outputPath string, preferredQuality string) error {
	// Extract base URL and video UID
	baseURL, videoUID, err := extractBaseURLAndUID(manifestURL)
	if err != nil {
		return fmt.Errorf("failed to parse manifest URL: %w", err)
	}

	// Fetch master playlist
	masterPlaylist, err := d.fetchMasterPlaylist(ctx, manifestURL)
	if err != nil {
		return fmt.Errorf("failed to fetch master playlist: %w", err)
	}

	variant := selectBestVariant(masterPlaylist.Variants, preferredQuality)
	if variant == nil {
		return fmt.Errorf("no suitable video quality found")
	}

	// Build the manifest URL for the selected quality
	qualityManifestURL := buildManifestURL(baseURL, videoUID, variant.URI)

	// Create temp directory for segments
	outputDir := filepath.Dir(outputPath)
	baseName := filepath.Base(outputPath)
	segmentDir := filepath.Join(outputDir, fmt.Sprintf(".segments_%s", baseName))
	if err := os.MkdirAll(segmentDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Download audio if available
	var audioPath string
	for _, alt := range variant.Alternatives {
		if alt.Type == "AUDIO" && alt.URI != "" {
			audioManifestURL := buildManifestURL(baseURL, videoUID, alt.URI)
			audioSegments, err := d.downloadSegments(ctx, audioManifestURL, baseURL, segmentDir, true)
			if err != nil {
				fmt.Printf("Warning: failed to download audio: %v\n", err)
				continue
			}
			audioPath = filepath.Join(segmentDir, "audio.mp4")
			if err := concatenateSegments(audioSegments, audioPath); err != nil {
				fmt.Printf("Warning: failed to concatenate audio: %v\n", err)
				continue
			}
			break
		}
	}

	// Download video segments
	videoSegments, err := d.downloadSegments(ctx, qualityManifestURL, baseURL, segmentDir, false)
	if err != nil {
		return fmt.Errorf("failed to download video segments: %w", err)
	}

	// Concatenate video segments
	videoPath := filepath.Join(segmentDir, "video.mp4")
	if err := concatenateSegments(videoSegments, videoPath); err != nil {
		return fmt.Errorf("failed to concatenate video: %w", err)
	}

	// Merge audio and video if we have both
	if audioPath != "" {
		if err := mergeAudioVideo(videoPath, audioPath, outputPath); err != nil {
			return fmt.Errorf("failed to merge audio/video: %w", err)
		}
	} else {
		// Just rename video to output
		if err := os.Rename(videoPath, outputPath); err != nil {
			// If rename fails (cross-device), copy instead
			if err := copyFile(videoPath, outputPath); err != nil {
				return fmt.Errorf("failed to move video to output: %w", err)
			}
		}
	}

	// Clean up segments only on success
	os.RemoveAll(segmentDir)

	return nil
}

// GetAvailableResolutions returns all available video resolutions
func (d *Downloader) GetAvailableResolutions(ctx context.Context, manifestURL string) ([]Resolution, error) {
	masterPlaylist, err := d.fetchMasterPlaylist(ctx, manifestURL)
	if err != nil {
		return nil, err
	}

	resolutions := make([]Resolution, 0, len(masterPlaylist.Variants))
	for _, v := range masterPlaylist.Variants {
		if v == nil {
			continue
		}
		resolutions = append(resolutions, Resolution{
			Name:        v.Resolution,
			Resolution:  v.Resolution,
			Bandwidth:   v.Bandwidth,
			ManifestURL: v.URI,
		})
	}

	return resolutions, nil
}

func (d *Downloader) fetchMasterPlaylist(ctx context.Context, urlStr string) (*m3u8.MasterPlaylist, error) {
	resp, err := d.httpClient.Get(ctx, urlStr)
	if err != nil {
		return nil, err
	}
	defer resp.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d fetching playlist", resp.StatusCode)
	}

	body, err := resp.Bytes()
	if err != nil {
		return nil, err
	}

	playlist, listType, err := m3u8.Decode(*bytes.NewBuffer(body), false)
	if err != nil {
		return nil, err
	}

	if listType != m3u8.MASTER {
		return nil, fmt.Errorf("expected master playlist, got media playlist")
	}

	return playlist.(*m3u8.MasterPlaylist), nil
}

func (d *Downloader) downloadSegments(ctx context.Context, manifestURL, baseURL, outputDir string, isAudio bool) ([]string, error) {
	resp, err := d.httpClient.Get(ctx, manifestURL)
	if err != nil {
		return nil, err
	}
	defer resp.Close()

	body, err := resp.Bytes()
	if err != nil {
		return nil, err
	}

	playlist, listType, err := m3u8.Decode(*bytes.NewBuffer(body), false)
	if err != nil {
		return nil, err
	}

	if listType != m3u8.MEDIA {
		return nil, fmt.Errorf("expected media playlist")
	}

	mediaPlaylist := playlist.(*m3u8.MediaPlaylist)

	segmentCount := 0
	for _, seg := range mediaPlaylist.Segments {
		if seg != nil {
			segmentCount++
		}
	}

	typeStr := "video"
	if isAudio {
		typeStr = "audio"
	}

	var downloaded int32
	var localPaths []string
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, d.concurrency)
	errChan := make(chan error, 1)

	// Handle init segment if present
	if mediaPlaylist.Map != nil {
		initURL := resolveSegmentURL(baseURL, mediaPlaylist.Map.URI)
		initPath := filepath.Join(outputDir, fmt.Sprintf("%s_init.mp4", typeStr))
		if err := d.downloadFile(ctx, initURL, initPath); err != nil {
			return nil, fmt.Errorf("failed to download init segment: %w", err)
		}
		localPaths = append(localPaths, initPath)
	}

	paths := make([]string, len(mediaPlaylist.Segments))
	
	// Download all segments
	for i, segment := range mediaPlaylist.Segments {
		if segment == nil {
			continue
		}

		wg.Add(1)
		sem <- struct{}{}

		go func(idx int, seg *m3u8.MediaSegment) {
			defer wg.Done()
			defer func() { <-sem }()

			segURL := resolveSegmentURL(baseURL, seg.URI)
			localPath := filepath.Join(outputDir, fmt.Sprintf("%s_seg_%05d.ts", typeStr, idx))

			if err := d.downloadFile(ctx, segURL, localPath); err != nil {
				select {
				case errChan <- err:
				default:
				}
				return
			}

			mu.Lock()
			paths[idx] = localPath
			mu.Unlock()
			
			downloadedCount := atomic.AddInt32(&downloaded, 1)
			if downloadedCount%10 == 0 || int(downloadedCount) == segmentCount {
				fmt.Printf("\r    [%s] Progress: %d/%d segments (%.1f%%)", typeStr, downloadedCount, segmentCount, float64(downloadedCount)/float64(segmentCount)*100)
			}
		}(i, segment)
	}

	wg.Wait()
	fmt.Println() // Add a newline after progress bar completes
	close(errChan)

	if err := <-errChan; err != nil {
		return nil, err
	}

	// Assemble final ordered paths
	var finalPaths []string
	if mediaPlaylist.Map != nil {
		initPath := filepath.Join(outputDir, fmt.Sprintf("%s_init.mp4", typeStr))
		finalPaths = append(finalPaths, initPath)
	}
	for _, p := range paths {
		if p != "" {
			finalPaths = append(finalPaths, p)
		}
	}

	return finalPaths, nil
}

func (d *Downloader) downloadFile(ctx context.Context, urlStr, path string) error {
	// Check if the file already exists and has a non-zero size
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		return nil
	}

	resp, err := d.httpClient.Get(ctx, urlStr)
	if err != nil {
		return err
	}
	defer resp.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d downloading %s", resp.StatusCode, urlStr)
	}

	out, err := os.Create(path)
	if err != nil {
		// Retry creating directory and file
		os.MkdirAll(filepath.Dir(path), 0755)
		out, err = os.Create(path)
		if err != nil {
			return fmt.Errorf("failed to create file %s: %w", path, err)
		}
	}
	
	_, err = io.Copy(out, resp.Body)
	out.Close()
	
	// If copy failed, remove the partial file so it can be retried properly next time
	if err != nil {
		os.Remove(path)
	}
	return err
}

func extractBaseURLAndUID(manifestURL string) (baseURL, uid string, err error) {
	re := regexp.MustCompile(`^(https?://[^/]+)/([^/]+)/manifest/video\.m3u8`)
	matches := re.FindStringSubmatch(manifestURL)
	if len(matches) == 3 {
		return matches[1], matches[2], nil
	}
	return "", "", fmt.Errorf("could not parse manifest URL: %s", manifestURL)
}

func buildManifestURL(baseURL, videoUID, relativeURI string) string {
	relativeURI = strings.TrimPrefix(relativeURI, "../")
	return fmt.Sprintf("%s/%s/manifest/%s", baseURL, videoUID, relativeURI)
}

func resolveSegmentURL(baseURL, segmentURI string) string {
	for strings.HasPrefix(segmentURI, "../") {
		segmentURI = strings.TrimPrefix(segmentURI, "../")
	}

	if strings.HasPrefix(segmentURI, "http://") || strings.HasPrefix(segmentURI, "https://") {
		return segmentURI
	}

	parsedBase, err := url.Parse(baseURL)
	if err != nil {
		return baseURL + "/" + segmentURI
	}

	return fmt.Sprintf("%s://%s/%s", parsedBase.Scheme, parsedBase.Host, segmentURI)
}

func selectBestVariant(variants []*m3u8.Variant, preferredQuality string) *m3u8.Variant {
	if len(variants) == 0 {
		return nil
	}

	if preferredQuality != "" {
		for _, v := range variants {
			if v != nil && v.Resolution == preferredQuality {
				return v
			}
		}
	}

	var best *m3u8.Variant
	for _, v := range variants {
		if v == nil {
			continue
		}
		if best == nil || v.Bandwidth > best.Bandwidth {
			best = v
		}
	}

	return best
}

func concatenateSegments(segments []string, outputPath string) error {
	if len(segments) == 0 {
		return fmt.Errorf("no segments to concatenate")
	}

	output, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer output.Close()

	for _, segPath := range segments {
		input, err := os.Open(segPath)
		if err != nil {
			return err
		}
		_, err = io.Copy(output, input)
		input.Close()
		if err != nil {
			return err
		}
	}

	return nil
}

func mergeAudioVideo(videoPath, audioPath, outputPath string) error {
	cmd := exec.Command("ffmpeg", "-y",
		"-i", videoPath,
		"-i", audioPath,
		"-c:v", "copy",
		"-c:a", "copy",
		outputPath,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg error: %v\n%s", err, string(output))
	}

	return nil
}

func copyFile(src, dst string) error {
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()

	destination, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destination.Close()

	_, err = io.Copy(destination, source)
	return err
}

// ExtractVideoUID attempts to extract a Cloudflare video UID from various URL formats
func ExtractVideoUID(urlStr string) (string, string, error) {
	patterns := []string{
		`(customer-[^.]+\.cloudflarestream\.com)/([a-f0-9]{32})`,
		`(videodelivery\.net)/([a-f0-9]{32})`,
		`(bytehighway\.net)/([a-f0-9]{32})`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindStringSubmatch(urlStr)
		if len(matches) == 3 {
			domain := matches[1]
			uid := matches[2]
			return domain, uid, nil
		}
	}

	hexRe := regexp.MustCompile(`[a-f0-9]{32}`)
	if match := hexRe.FindString(urlStr); match != "" {
		return "videodelivery.net", match, nil
	}

	return "", "", fmt.Errorf("could not extract video UID from: %s", urlStr)
}

// BuildManifestURLFromUID constructs a manifest URL from a video UID
func BuildManifestURLFromUID(domain, uid string) string {
	if domain == "" {
		domain = "videodelivery.net"
	}
	return fmt.Sprintf("https://%s/%s/manifest/video.m3u8", domain, uid)
}

// SafeFilename creates a safe filename from a title
func SafeFilename(title string) string {
	replacer := strings.NewReplacer(
		"/", "-",
		"\\", "-",
		":", "-",
		"*", "",
		"?", "",
		"\"", "",
		"<", "",
		">", "",
		"|", "",
		"\n", " ",
		"\r", " ",
	)
	safe := replacer.Replace(title)
	safe = strings.TrimSpace(safe)
	if len(safe) > 200 {
		safe = safe[:200]
	}
	return safe
}
