package skillshare

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Client handles communication with the Skillshare API
type Client struct {
	httpClient *http.Client
	cookies    string
}

// NewClient creates a new Skillshare API client
func NewClient(cookies string) *Client {
	return &Client{
		httpClient: &http.Client{},
		cookies:    cookies,
	}
}

// ParseCourseURL extracts the class ID from a Skillshare course URL
func ParseCourseURL(url string) (int, error) {
	// Match URLs like:
	// https://www.skillshare.com/en/classes/class-name/1234567
	// https://www.skillshare.com/classes/class-name/1234567
	re := regexp.MustCompile(`skillshare\.com/(?:en/)?classes/[^/]+/(\d+)`)
	matches := re.FindStringSubmatch(url)
	if len(matches) < 2 {
		return 0, fmt.Errorf("could not extract class ID from URL: %s", url)
	}

	id, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, fmt.Errorf("invalid class ID: %s", matches[1])
	}

	return id, nil
}

// FetchClassData retrieves class/course data from the Skillshare API
func (c *Client) FetchClassData(classID int) (*ClassData, error) {
	url := fmt.Sprintf("https://api.skillshare.com/classes/%d", classID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header = http.Header{
		"Accept":     {"application/vnd.skillshare.class+json;,version=0.8"},
		"User-Agent": {"Skillshare/5.3.0; Android 9.0.1"},
		"Host":       {"api.skillshare.com"},
		"Referer":    {"https://www.skillshare.com/"},
		"Cookie":     {c.cookies},
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("class not found (ID: %d)", classID)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("authentication failed - check your cookies")
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var classData ClassData
	if err := json.Unmarshal(body, &classData); err != nil {
		return nil, fmt.Errorf("failed to parse API response: %w", err)
	}

	return &classData, nil
}

// FetchClassResources retrieves downloadable resources/attachments for a class
func (c *Client) FetchClassResources(classID int) (*ClassResources, error) {
	url := fmt.Sprintf("https://api.skillshare.com/classes/%d/attachments", classID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header = http.Header{
		"Accept":     {"application/json"},
		"User-Agent": {"Skillshare/5.3.0; Android 9.0.1"},
		"Host":       {"api.skillshare.com"},
		"Referer":    {"https://www.skillshare.com/"},
		"Cookie":     {c.cookies},
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch resources: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var resources ClassResources
	if err := json.Unmarshal(body, &resources); err != nil {
		return nil, fmt.Errorf("failed to parse resources response: %w", err)
	}

	return &resources, nil
}

// FetchProjects retrieves student projects for a class
func (c *Client) FetchProjects(classSlug string, classSKU int) (*ProjectsResponse, error) {
	url := fmt.Sprintf("https://www.skillshare.com/en/classes/%s/%d/projects?format=json", classSlug, classSKU)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header = http.Header{
		"Accept":           {"application/json, text/javascript, */*; q=0.01"},
		"User-Agent":       {"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36"},
		"Referer":          {fmt.Sprintf("https://www.skillshare.com/en/classes/%s/%d/projects", classSlug, classSKU)},
		"Cookie":           {c.cookies},
		"X-Requested-With": {"XMLHttpRequest"},
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch projects: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var projects ProjectsResponse
	if err := json.Unmarshal(body, &projects); err != nil {
		return nil, fmt.Errorf("failed to parse projects response: %w", err)
	}

	return &projects, nil
}

// ExtractCloudflareUID extracts the Cloudflare Stream video UID from the stream link
func ExtractCloudflareUID(streamHref string) string {
	// Stream href looks like: https://api.skillshare.com/sessions/{id}/stream
	// We need to call this endpoint to get the actual Cloudflare stream URL
	// For now, we'll extract from the video_hashed_id which contains the CF UID
	return streamHref
}

// GetVideoStreamURL fetches the actual video stream URL from the session stream endpoint
func (c *Client) GetVideoStreamURL(streamHref string) (string, error) {
	if streamHref == "" {
		return "", fmt.Errorf("no stream URL available")
	}

	// Handle relative URLs by prepending the API base
	if strings.HasPrefix(streamHref, "/") {
		streamHref = "https://api.skillshare.com" + streamHref
	}

	req, err := http.NewRequest("GET", streamHref, nil)
	if err != nil {
		return "", err
	}

	req.Header = http.Header{
		"Accept":     {"application/json"},
		"User-Agent": {"Skillshare/5.3.0; Android 9.0.1"},
		"Host":       {"api.skillshare.com"},
		"Referer":    {"https://www.skillshare.com/"},
		"Cookie":     {c.cookies},
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to get stream URL: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	// The stream endpoint returns a JSON with the video URL
	// Try multiple possible response formats
	var streamResp struct {
		URL      string `json:"url"`
		VideoURL string `json:"video_url"`
		HLS      string `json:"hls"`
		Stream   string `json:"stream"`
		Source   string `json:"source"`
	}
	if err := json.Unmarshal(body, &streamResp); err == nil {
		// Check all possible URL fields
		for _, url := range []string{streamResp.URL, streamResp.VideoURL, streamResp.HLS, streamResp.Stream, streamResp.Source} {
			if url != "" {
				return url, nil
			}
		}
	}

	// Try to find a cloudflare stream URL in the response
	bodyStr := string(body)

	// Look for cloudflarestream.com or videodelivery.net URLs
	cfRe := regexp.MustCompile(`https?://(?:customer-[^"'\s]+\.cloudflarestream\.com|videodelivery\.net)/([a-f0-9]{32})/manifest/video\.m3u8[^"'\s]*`)
	matches := cfRe.FindStringSubmatch(bodyStr)
	if len(matches) >= 1 {
		return matches[0], nil
	}

	// Look for any HLS manifest URL
	hlsRe := regexp.MustCompile(`https?://[^"'\s]+\.m3u8[^"'\s]*`)
	hlsMatches := hlsRe.FindString(bodyStr)
	if hlsMatches != "" {
		return hlsMatches, nil
	}

	// Also try to find just the video UID and construct URL
	uidRe := regexp.MustCompile(`[a-f0-9]{32}`)
	uidMatches := uidRe.FindString(bodyStr)
	if uidMatches != "" {
		return fmt.Sprintf("https://videodelivery.net/%s/manifest/video.m3u8", uidMatches), nil
	}

	return "", fmt.Errorf("could not find video URL in response: %s", bodyStr[:min(len(bodyStr), 500)])
}

// ParseCookiesFromFile reads cookies from a cookies file
// Supports:
// - Raw cookie format: name=value; name2=value2 (possibly with expiry and other attributes)
// - Netscape format: domain\tflag\tpath\tsecure\texpiration\tname\tvalue
func ParseCookiesFromFile(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}

	// Check if it looks like Netscape format (tab-separated)
	lines := strings.Split(content, "\n")
	if len(lines) > 0 {
		firstLine := strings.TrimSpace(lines[0])

		// If the first non-comment line has tabs, treat as Netscape format
		if strings.Contains(firstLine, "\t") && !strings.HasPrefix(firstLine, "#") {
			return parseNetscapeCookies(lines)
		}
	}

	// Otherwise treat as raw cookie format
	return parseRawCookies(content)
}

// parseNetscapeCookies parses Netscape cookie format
func parseNetscapeCookies(lines []string) string {
	var cookies []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Netscape cookie format: domain, flag, path, secure, expiration, name, value
		fields := strings.Split(line, "\t")
		if len(fields) >= 7 {
			name := fields[5]
			value := fields[6]
			cookies = append(cookies, fmt.Sprintf("%s=%s", name, value))
		}
	}

	return strings.Join(cookies, "; ")
}

// parseRawCookies parses raw cookie format (name=value; name2=value2)
func parseRawCookies(content string) string {
	// The content might be a single line with cookie data including attributes
	// We need to extract just the name=value pairs, excluding attributes
	// like expires, Max-Age, path, domain, secure, HttpOnly, SameSite

	var cookies []string
	knownAttributes := map[string]bool{
		"expires":  true,
		"max-age":  true,
		"path":     true,
		"domain":   true,
		"secure":   true,
		"httponly": true,
		"samesite": true,
	}

	// Split by semicolon
	parts := strings.Split(content, ";")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Check if it's an attribute (no = or known attribute name)
		if !strings.Contains(part, "=") {
			continue
		}

		// Split by first =
		idx := strings.Index(part, "=")
		if idx == -1 {
			continue
		}

		name := strings.TrimSpace(part[:idx])
		nameLower := strings.ToLower(name)

		// Skip known attributes
		if knownAttributes[nameLower] {
			continue
		}

		// This is a cookie
		cookies = append(cookies, part)
	}

	return strings.Join(cookies, "; ")
}

// FetchProjectGuide retrieves the project guide (instructions + attachments) from the web endpoint
func (c *Client) FetchProjectGuide(webURL string) (*ProjectGuideResponse, error) {
	// Ensure URL is clean of query params
	baseURL := strings.Split(webURL, "?")[0]
	url := fmt.Sprintf("%s/projects?format=json", baseURL)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header = http.Header{
		"Accept":     {"application/json"},
		"User-Agent": {"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"},
		"Referer":    {baseURL},
		"Cookie":     {c.cookies},
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d fetching project guide", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var guide ProjectGuideResponse
	if err := json.Unmarshal(body, &guide); err != nil {
		return nil, fmt.Errorf("failed to parse project guide: %w", err)
	}

	return &guide, nil
}
