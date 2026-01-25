package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/spf13/cobra"
	"github.com/xenking/skillshare-offline/internal/cloudflare"
	"github.com/xenking/skillshare-offline/internal/skillshare"
)

var (
	courseURL     string
	cookieFile    string
	outputDir     string
	quality       string
	verbose       bool
	skipVideos    bool
	skipResources bool
	skipProjects  bool
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "skillshare-dl",
		Short: "Download Skillshare courses for offline viewing",
		Long: `Skillshare Offline Downloader

Download Skillshare courses for offline viewing. Supports the new 
Cloudflare Streams video format.

Example:
  skillshare-dl -c "https://www.skillshare.com/en/classes/course-name/123456" \
    --cookie-file cookies.txt \
    --output-dir ./downloaded`,
		RunE: runDownload,
	}

	rootCmd.Flags().StringVarP(&courseURL, "url", "c", "", "Skillshare course URL (required)")
	rootCmd.Flags().StringVar(&cookieFile, "cookie-file", "cookies.txt", "Path to cookies.txt file")
	rootCmd.Flags().StringVarP(&outputDir, "output-dir", "o", "./downloaded", "Output directory")
	rootCmd.Flags().StringVarP(&quality, "quality", "q", "", "Preferred quality (e.g., 1920x1080, empty for best)")
	rootCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Verbose output")
	rootCmd.Flags().BoolVar(&skipVideos, "skip-videos", false, "Skip video downloads")
	rootCmd.Flags().BoolVar(&skipResources, "skip-resources", false, "Skip resource/attachment downloads")
	rootCmd.Flags().BoolVar(&skipProjects, "skip-projects", false, "Skip project downloads")

	rootCmd.MarkFlagRequired("url")

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runDownload(cmd *cobra.Command, args []string) error {
	printBanner()

	// Parse course URL
	classID, err := skillshare.ParseCourseURL(courseURL)
	if err != nil {
		return fmt.Errorf("invalid course URL: %w", err)
	}
	fmt.Printf("📚 Class ID: %d\n\n", classID)

	// Load cookies
	cookieData, err := os.ReadFile(cookieFile)
	if err != nil {
		return fmt.Errorf("failed to read cookie file: %w", err)
	}
	cookies := skillshare.ParseCookiesFromFile(string(cookieData))
	if cookies == "" {
		return fmt.Errorf("no cookies found in %s", cookieFile)
	}
	fmt.Println("🍪 Cookies loaded")

	// Create Skillshare client
	client := skillshare.NewClient(cookies)

	// Fetch class data
	fmt.Print("📥 Fetching course data... ")
	classData, err := client.FetchClassData(classID)
	if err != nil {
		return fmt.Errorf("\n❌ Failed to fetch class data: %w", err)
	}
	fmt.Println("✓")

	// Print class info
	fmt.Printf("\n📖 %s\n", classData.Title)
	fmt.Printf("   by %s\n", classData.Embedded.Teacher.FullName)
	fmt.Printf("   %d videos | %s\n\n", classData.NumVideos, classData.TotalVideosDuration)

	// Create output directory - simplified structure with videos directly in course folder
	safeName := cloudflare.SafeFilename(classData.Title)
	classDirName := fmt.Sprintf("[%d] %s", classID, safeName)
	classDir := filepath.Join(outputDir, classDirName)

	if err := os.MkdirAll(classDir, 0o755); err != nil {
		return fmt.Errorf("failed to create course directory: %w", err)
	}

	// Download resources (instructions + attachments)
	if !skipResources {
		// Use "resources" folder for both instructions and attachments
		resourceDir := filepath.Join(classDir, "resources")
		os.MkdirAll(resourceDir, 0o755)

		// Use class URL to fetch project guide
		targetURL := classData.WebURL
		if targetURL == "" {
			targetURL = courseURL
		}

		downloadResources(client, targetURL, resourceDir, cookies)

		// Remove empty directory if nothing was downloaded
		if empty, _ := os.ReadDir(resourceDir); len(empty) == 0 {
			os.Remove(resourceDir)
		}
	}

	// Student projects download disabled as per request
	// if !skipProjects { ... }

	// Create cloudflare downloader
	downloader := cloudflare.NewDownloader()
	defer downloader.Close()

	// Get lessons
	lessons := classData.ToLessons()
	totalLessons := len(lessons)

	fmt.Printf("📹 Downloading %d videos...\n\n", totalLessons)

	// Download each lesson
	var successCount int32
	var failCount int32
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4) // Limit to 4 parallel downloads

	for i, lesson := range lessons {
		wg.Add(1)
		go func(i int, lesson skillshare.Lesson) {
			defer wg.Done()
			sem <- struct{}{}        // Acquire token
			defer func() { <-sem }() // Release token

			fmt.Printf("[%d/%d] Starting: %s\n", i+1, totalLessons, lesson.Title)

			// Get the stream URL
			if lesson.StreamURL == "" {
				fmt.Printf("    ⚠️  [%d] No stream URL available\n", i+1)
				atomic.AddInt32(&failCount, 1)
				return
			}

			// Fetch the actual video URL from the stream endpoint
			videoURL, err := client.GetVideoStreamURL(lesson.StreamURL)
			if err != nil {
				fmt.Printf("    ⚠️  [%d] Failed to get video URL: %v\n", i+1, err)
				atomic.AddInt32(&failCount, 1)
				return
			}

			if verbose {
				fmt.Printf("    [%d] Stream URL: %s\n", i+1, videoURL)
			}

			// Build output filename - use lesson title directly in course folder
			safeTitle := cloudflare.SafeFilename(lesson.Title)
			outputFile := filepath.Join(classDir, fmt.Sprintf("%02d. %s.mp4", i+1, safeTitle))

			// Skip if already downloaded
			if _, err := os.Stat(outputFile); err == nil {
				fmt.Printf("    ✓ [%d] Already downloaded\n", i+1)
				atomic.AddInt32(&successCount, 1)
				return
			}

			// Download video
			if err := downloader.DownloadVideo(videoURL, outputFile, quality); err != nil {
				fmt.Printf("    ❌ [%d] Download failed: %v\n", i+1, err)
				atomic.AddInt32(&failCount, 1)
				return
			}

			fmt.Printf("    ✓ [%d] Downloaded: %s\n", i+1, lesson.Title)
			atomic.AddInt32(&successCount, 1)
		}(i, lesson)
	}

	wg.Wait()

	// Summary
	fmt.Println("─────────────────────────────────────────")
	fmt.Printf("✅ Completed: %d/%d videos\n", successCount, totalLessons)
	if failCount > 0 {
		fmt.Printf("⚠️  Failed: %d videos\n", failCount)
	}
	fmt.Printf("📁 Output: %s\n", classDir)

	return nil
}

func printBanner() {
	banner := `
    +=─────────────────────────────────────────────────────────────────────────=+
    
     ███████╗██╗  ██╗██╗██╗     ██╗     ███████╗██╗  ██╗ █████╗ ██████╗ ███████╗
     ██╔════╝██║ ██╔╝██║██║     ██║     ██╔════╝██║  ██║██╔══██╗██╔══██╗██╔════╝
     ███████╗█████╔╝ ██║██║     ██║     ███████╗███████║███████║██████╔╝█████╗  
     ╚════██║██╔═██╗ ██║██║     ██║     ╚════██║██╔══██║██╔══██║██╔══██╗██╔══╝  
     ███████║██║  ██╗██║███████╗███████╗███████║██║  ██║██║  ██║██║  ██║███████╗
     ╚══════╝╚═╝  ╚═╝╚═╝╚══════╝╚══════╝╚══════╝╚═╝  ╚═╝╚═╝  ╚═╝╚═╝  ╚═╝╚══════╝
                                                        Offline Downloader v1.0

    +=─────────────────────────────────────────────────────────────────────────=+
`
	fmt.Println(banner)
}

func toSnakeCase(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, "-", "_")
	// Remove consecutive underscores
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}
	s = strings.Trim(s, "_")
	return s
}

func extractSlugFromURL(courseURL string) string {
	// Extract class slug from URL like:
	// https://www.skillshare.com/en/classes/class-name-here/123456
	parts := strings.Split(courseURL, "/classes/")
	if len(parts) < 2 {
		return ""
	}
	slugParts := strings.Split(parts[1], "/")
	if len(slugParts) > 0 {
		return slugParts[0]
	}
	return ""
}

func downloadResources(client *skillshare.Client, webURL, resourceDir string, cookies string) {
	fmt.Print("📎 Fetching project guide (resources & instructions)... ")
	guide, err := client.FetchProjectGuide(webURL)
	if err != nil {
		fmt.Printf("⚠️ %v\n\n", err)
		return
	}

	attachments := guide.ProjectGuide.Attachments
	instructions := guide.ProjectGuide.ProjectGuideHTML

	if len(attachments) == 0 && instructions == "" {
		fmt.Println("No resources or instructions available")
		return
	}

	fmt.Printf("Found %d attachments and instructions\n", len(attachments))

	// Save instructions if present
	if instructions != "" {
		instPath := filepath.Join(resourceDir, "Project_Instructions.html")
		// Wrap in simple HTML for readability
		htmlContent := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <title>Project Instructions</title>
    <style>body { font-family: sans-serif; max-width: 800px; margin: 2em auto; padding: 0 1em; line-height: 1.6; } img { max-width: 100%%; }</style>
</head>
<body>
    <h1>Project Instructions</h1>
    %s
</body>
</html>`, instructions)

		if err := os.WriteFile(instPath, []byte(htmlContent), 0o644); err == nil {
			fmt.Printf("    ✓ Saved Project_Instructions.html\n")
		} else {
			fmt.Printf("    ⚠️ Failed to save instructions: %v\n", err)
		}
	}

	// Download attachments
	for _, att := range attachments {
		// Get download URL
		downloadURL := att.URL
		if downloadURL == "" {
			continue
		}

		// Clean title for filename
		filename := cloudflare.SafeFilename(att.Title)
		// If title doesn't have extension but URL does, try to preserve it
		if filepath.Ext(filename) == "" {
			ext := filepath.Ext(att.URL)
			// Remove query params from ext
			ext = strings.Split(ext, "?")[0]
			filename += ext
		}

		outputPath := filepath.Join(resourceDir, filename)

		// Skip if already exists
		if _, err := os.Stat(outputPath); err == nil {
			fmt.Printf("    ✓ %s (exists)\n", filename)
			continue
		}

		fmt.Printf("    ⬇ Downloading %s (%s)... ", filename, att.Size)
		if err := downloadFile(downloadURL, outputPath, cookies); err != nil {
			fmt.Printf("❌ %v\n", err)
			continue
		}
		fmt.Println("✓")
	}
	fmt.Println()
}

// downloadProjects is disabled/removed

func downloadFile(url, outputPath, cookies string) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")
	if cookies != "" {
		req.Header.Set("Cookie", cookies)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	out, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}
