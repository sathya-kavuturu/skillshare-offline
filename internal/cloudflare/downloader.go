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
func (d *Downloader) DownloadVideo(manifestURL, outputPath string, preferredQuality string) error {
	// Extract base URL and video UID
	baseURL, videoUID, err := extractBaseURLAndUID(manifestURL)
	if err != nil {
		return fmt.Errorf("failed to parse manifest URL: %w", err)
	}

	// Fetch master playlist
	masterPlaylist, err := d.fetchMasterPlaylist(manifestURL)
	if err != nil {
		return fmt.Errorf("failed to fetch master playlist: %w", err)
	}

	// Select quality
	/*
		fmt.Printf("    Available resolutions: ")
		for i, v := range masterPlaylist.Variants {
			if v != nil {
				if i > 0 {
					fmt.Printf(", ")
				}
				fmt.Printf("%s (%d kbps)", v.Resolution, v.Bandwidth/1000)
			}
		}
		fmt.Println()
	*/

	variant := selectBestVariant(masterPlaylist.Variants, preferredQuality)
	if variant == nil {
		return fmt.Errorf("no suitable video quality found")
	}
	// fmt.Printf("    Selected: %s (%d kbps)\n", variant.Resolution, variant.Bandwidth/1000)

	// Build the manifest URL for the selected quality
	qualityManifestURL := buildManifestURL(baseURL, videoUID, variant.URI)

	// Create temp directory for segments - make unique to avoid race conditions in parallel mode
	outputDir := filepath.Dir(outputPath)
	baseName := filepath.Base(outputPath)
	segmentDir := filepath.Join(outputDir, fmt.Sprintf(".segments_%s", baseName))
	if err := os.MkdirAll(segmentDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}
	// Ensure cleanup happens even on errors
	defer os.RemoveAll(segmentDir)

	// Download audio if available
	var audioPath string
	for _, alt := range variant.Alternatives {
		if alt.Type == "AUDIO" && alt.URI != "" {
			audioManifestURL := buildManifestURL(baseURL, videoUID, alt.URI)
			audioSegments, err := d.downloadSegments(audioManifestURL, baseURL, segmentDir, true)
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
	videoSegments, err := d.downloadSegments(qualityManifestURL, baseURL, segmentDir, false)
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

	return nil
}

// GetAvailableResolutions returns all available video resolutions
func (d *Downloader) GetAvailableResolutions(manifestURL string) ([]Resolution, error) {
	masterPlaylist, err := d.fetchMasterPlaylist(manifestURL)
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

func (d *Downloader) fetchMasterPlaylist(urlStr string) (*m3u8.MasterPlaylist, error) {
	resp, err := d.httpClient.Get(context.Background(), urlStr)
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

func (d *Downloader) downloadSegments(manifestURL, baseURL, outputDir string, isAudio bool) ([]string, error) {
	resp, err := d.httpClient.Get(context.Background(), manifestURL)
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

	// Count segments
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

	// Simple progress counter
	var downloaded int32
	// printProgress := func() {
	// 	fmt.Printf("\r    Downloading %s: %d/%d", typeStr, atomic.LoadInt32(&downloaded), segmentCount)
	// }

	var localPaths []string
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, d.concurrency)
	errChan := make(chan error, 1)

	// Handle init segment if present
	if mediaPlaylist.Map != nil {
		initURL := resolveSegmentURL(baseURL, mediaPlaylist.Map.URI)
		initPath := filepath.Join(outputDir, fmt.Sprintf("%s_init.mp4", typeStr))
		if err := d.downloadFile(initURL, initPath); err != nil {
			return nil, fmt.Errorf("failed to download init segment: %w", err)
		}
		localPaths = append(localPaths, initPath)
	}

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

			if err := d.downloadFile(segURL, localPath); err != nil {
				select {
				case errChan <- err:
				default:
				}
				return
			}

			mu.Lock()
			localPaths = append(localPaths, localPath)
			mu.Unlock()
			atomic.AddInt32(&downloaded, 1)
			// printProgress()
		}(i, segment)
	}

	wg.Wait()
	close(errChan)

	if err := <-errChan; err != nil {
		return nil, err
	}

	// fmt.Println() // New line after progress

	return localPaths, nil
}

func (d *Downloader) downloadFile(urlStr, path string) error {
	resp, err := d.httpClient.Get(context.Background(), urlStr)
	if err != nil {
		return err
	}
	defer resp.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d downloading %s", resp.StatusCode, urlStr)
	}

	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

func extractBaseURLAndUID(manifestURL string) (baseURL, uid string, err error) {
	// Match URLs like:
	// https://videodelivery.net/abc123/manifest/video.m3u8
	// https://customer-xxx.cloudflarestream.com/abc123/manifest/video.m3u8
	// https://customer-xxx.cloudflarestream.com/<JWT>/manifest/video.m3u8
	// The UID can be either a 32-char hex string or a JWT token (three dot-separated base64 strings)

	// Parse the URL to extract components
	re := regexp.MustCompile(`^(https?://[^/]+)/([^/]+)/manifest/video\.m3u8`)
	matches := re.FindStringSubmatch(manifestURL)
	if len(matches) == 3 {
		return matches[1], matches[2], nil
	}
	return "", "", fmt.Errorf("could not parse manifest URL: %s", manifestURL)
}

func buildManifestURL(baseURL, videoUID, relativeURI string) string {
	// Handle relative URIs
	relativeURI = strings.TrimPrefix(relativeURI, "../")
	return fmt.Sprintf("%s/%s/manifest/%s", baseURL, videoUID, relativeURI)
}

func resolveSegmentURL(baseURL, segmentURI string) string {
	// Clean up relative path
	for strings.HasPrefix(segmentURI, "../") {
		segmentURI = strings.TrimPrefix(segmentURI, "../")
	}

	// Check if it's already an absolute URL
	if strings.HasPrefix(segmentURI, "http://") || strings.HasPrefix(segmentURI, "https://") {
		return segmentURI
	}

	// Build absolute URL
	parsedBase, err := url.Parse(baseURL)
	if err != nil {
		return baseURL + "/" + segmentURI
	}

	// Join with base path
	return fmt.Sprintf("%s://%s/%s", parsedBase.Scheme, parsedBase.Host, segmentURI)
}

func selectBestVariant(variants []*m3u8.Variant, preferredQuality string) *m3u8.Variant {
	if len(variants) == 0 {
		return nil
	}

	// If specific quality requested, try to match
	if preferredQuality != "" {
		for _, v := range variants {
			if v != nil && v.Resolution == preferredQuality {
				return v
			}
		}
	}

	// Default to highest bandwidth
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

	// Create output file
	output, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer output.Close()

	// Concatenate all segments
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
		// Clean up segment file
		os.Remove(segPath)
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
	// Try common Cloudflare Stream URL patterns
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

	// Check if the URL contains a 32-char hex string anywhere
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
	// Replace problematic characters
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
