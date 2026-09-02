package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/cristalhq/acmd"
	"github.com/xenking/skillshare-offline/internal/cloudflare"
	"github.com/xenking/skillshare-offline/internal/skillshare"
)

type Config struct {
	CourseURL     string
	URLFile       string
	CookieFile    string
	OutputDir     string
	Quality       string
	Verbose       bool
	SkipVideos    bool
	SkipResources bool
	Concurrency   int
}

var verbose bool

func (c *Config) Flags() *flag.FlagSet {
	fs := flag.NewFlagSet("download", flag.ContinueOnError)
	fs.StringVar(&c.CourseURL, "url", "", "Skillshare course URL")
	fs.StringVar(&c.CourseURL, "c", "", "Skillshare course URL (shorthand)")
	fs.StringVar(&c.URLFile, "url-file", "", "Path to a text file containing Skillshare course URLs (one per line)")
	fs.StringVar(&c.URLFile, "f", "", "Path to a text file containing Skillshare course URLs (shorthand)")
	fs.StringVar(&c.CookieFile, "cookie-file", "cookies.txt", "Path to cookies.txt file")
	fs.StringVar(&c.OutputDir, "output-dir", "./downloaded", "Output directory")
	fs.StringVar(&c.OutputDir, "o", "./downloaded", "Output directory (shorthand)")
	fs.StringVar(&c.Quality, "quality", "", "Preferred quality (e.g., 1920x1080)")
	fs.StringVar(&c.Quality, "q", "", "Preferred quality (shorthand)")
	fs.BoolVar(&c.Verbose, "verbose", false, "enable verbose output")
	fs.BoolVar(&c.Verbose, "v", false, "enable verbose output (shorthand)")
	fs.BoolVar(&c.SkipVideos, "skip-videos", false, "Skip video downloads")
	fs.BoolVar(&c.SkipResources, "skip-resources", false, "Skip resource/attachment downloads")
	fs.IntVar(&c.Concurrency, "concurrency", 1, "Number of concurrent video downloads")
	return fs
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("skillshare-dl: ")

	cmds := []acmd.Command{
		{
			Name:        "download",
			Alias:       "dl",
			Description: "Download a skillshare class",
			ExecFunc: func(ctx context.Context, args []string) error {
				var cfg Config
				if err := cfg.Flags().Parse(args); err != nil {
					return err
				}

				if cfg.CourseURL == "" && cfg.URLFile == "" {
					return fmt.Errorf("course URL (--url) or URL file (--url-file) is required")
				}

				var urls []string
				if cfg.CourseURL != "" {
					urls = append(urls, cfg.CourseURL)
				}

				if cfg.URLFile != "" {
					data, err := os.ReadFile(cfg.URLFile)
					if err != nil {
						return fmt.Errorf("failed to read URL file: %w", err)
					}
					for _, line := range strings.Split(string(data), "\n") {
						line = strings.TrimSpace(line)
						if line != "" {
							urls = append(urls, line)
						}
					}
				}

				if len(urls) == 0 {
					return fmt.Errorf("no URLs found")
				}

				verbose = cfg.Verbose

				var lastErr error
				for i, u := range urls {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					if i > 0 {
						printInfo("─────────────────────────────────────────")
					}
					cfg.CourseURL = u
					if err := runDownload(ctx, cfg); err != nil {
						log.Printf("Error downloading %s: %v", u, err)
						lastErr = err
					}
				}

				return lastErr
			},
		},
	}

	r := acmd.RunnerOf(cmds, acmd.Config{
		AppName:        "skillshare-dl",
		AppDescription: "Skillshare Offline Downloader",
		Version:        "1.0.0",
	})

	if err := r.Run(); err != nil {
		log.Printf("Error: %v", err)
		os.Exit(1)
	}
}

func runDownload(ctx context.Context, cfg Config) error {
	printInfo(fmt.Sprintf("Starting download for %s", cfg.CourseURL))

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Parse course URL
	classID, err := skillshare.ParseCourseURL(cfg.CourseURL)
	if err != nil {
		return fmt.Errorf("invalid course URL: %w", err)
	}
	printInfo(fmt.Sprintf("Class ID: %d", classID))

	// Load cookies
	cookieData, err := os.ReadFile(cfg.CookieFile)
	if err != nil {
		return fmt.Errorf("failed to read cookie file: %w", err)
	}
	cookies := skillshare.ParseCookiesFromFile(string(cookieData))
	if cookies == "" {
		return fmt.Errorf("no cookies found in %s", cfg.CookieFile)
	}
	printInfo("Cookies loaded")

	// Create Skillshare client
	client := skillshare.NewClient(cookies)

	// Fetch class data
	printInfo("Fetching course data...")
	classData, err := client.FetchClassData(ctx, classID)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("operation cancelled")
		}
		return fmt.Errorf("failed to fetch class data: %w", err)
	}

	printInfo(fmt.Sprintf("Course: %s by %s", classData.Title, classData.Embedded.Teacher.FullName))
	printInfo(fmt.Sprintf("Videos: %d | Duration: %s", classData.NumVideos, classData.TotalVideosDuration))

	// Output directory
	absOutputDir, err := filepath.Abs(cfg.OutputDir)
	if err != nil {
		absOutputDir = cfg.OutputDir
	}
	safeName := cloudflare.SafeFilename(classData.Title)
	classDirName := fmt.Sprintf("[%d] %s", classID, safeName)
	classDir := filepath.Join(absOutputDir, classDirName)

	if err := os.MkdirAll(classDir, 0o755); err != nil {
		return fmt.Errorf("failed to create course directory: %w", err)
	}
	printInfo(fmt.Sprintf("Output Dir: %s", classDir))

	// Download resources
	if !cfg.SkipResources {
		resourceDir := filepath.Join(classDir, "resources")
		os.MkdirAll(resourceDir, 0o755)

		targetURL := classData.WebURL
		if targetURL == "" {
			targetURL = cfg.CourseURL
		}

		printInfo("Fetching resources...")
		downloadResources(ctx, client, targetURL, resourceDir, cookies)

		if empty, _ := os.ReadDir(resourceDir); len(empty) == 0 {
			os.Remove(resourceDir)
		}
	}

	if cfg.SkipVideos {
		printInfo("Skipping video downloads as requested")
		return nil
	}

	// Create cloudflare downloader
	downloader := cloudflare.NewDownloader()
	defer downloader.Close()

	lessons := classData.ToLessons()
	totalLessons := len(lessons)

	printInfo(fmt.Sprintf("Downloading %d videos...", totalLessons))

	var successCount int32
	var failCount int32
	var wg sync.WaitGroup
	sem := make(chan struct{}, cfg.Concurrency)

	for i, lesson := range lessons {
		select {
		case sem <- struct{}{}: // Acquire semaphore BEFORE spawning to preserve order
		case <-ctx.Done():
			printInfo("Download cancelled by user")
			goto CancelExit
		}

		wg.Add(1)
		go func(i int, lesson skillshare.Lesson) {
			defer wg.Done()
			defer func() { <-sem }() // Release when done

			if ctx.Err() != nil {
				return
			}

			if verbose {
				log.Printf("[%d/%d] Starting: %s", i+1, totalLessons, lesson.Title)
			}

			if lesson.StreamURL == "" {
				log.Printf("WARN: [%d] No stream URL available", i+1)
				atomic.AddInt32(&failCount, 1)
				return
			}

			videoURL, err := client.GetVideoStreamURL(ctx, lesson.StreamURL)
			if err != nil {
				log.Printf("WARN: [%d] Failed to get video URL: %v", i+1, err)
				atomic.AddInt32(&failCount, 1)
				return
			}

			safeTitle := cloudflare.SafeFilename(lesson.Title)
			outputFile := filepath.Join(classDir, fmt.Sprintf("%02d. %s.mp4", i+1, safeTitle))

			if _, err := os.Stat(outputFile); err == nil {
				if verbose {
					log.Printf("[%d/%d] Exists: %s", i+1, totalLessons, lesson.Title)
				}
				atomic.AddInt32(&successCount, 1)
				return
			}

			maxRetries := 2
			var downloadErr error
			
			for attempt := 1; attempt <= maxRetries; attempt++ {
				if attempt > 1 {
					printInfo(fmt.Sprintf("Retrying [%d/%d]: %s (Attempt %d/%d)", i+1, totalLessons, lesson.Title, attempt, maxRetries))
				} else {
					printInfo(fmt.Sprintf("Downloading [%d/%d]: %s", i+1, totalLessons, lesson.Title))
				}
				
				downloadErr = downloader.DownloadVideo(ctx, videoURL, outputFile, cfg.Quality)
				if downloadErr == nil {
					break // Success
				}
				
				if ctx.Err() != nil {
					break // Context cancelled, don't retry
				}
				
				log.Printf("ERROR: [%d] Download failed on attempt %d: %v", i+1, attempt, downloadErr)
				// Don't remove outputFile yet because segments are kept and will be reused on next retry!
			}

			if downloadErr != nil && ctx.Err() == nil {
				atomic.AddInt32(&failCount, 1)
				os.Remove(outputFile) // Only remove the final corrupted output file if it totally failed
				return
			} else if ctx.Err() != nil {
				return
			}


			atomic.AddInt32(&successCount, 1)
		}(i, lesson)
	}

	wg.Wait()

CancelExit:
	if ctx.Err() != nil {
		printInfo("Process interrupted. Exiting gracefully...")
		return ctx.Err()
	}

	printInfo("─────────────────────────────────────────")
	printInfo(fmt.Sprintf("Completed: %d/%d videos", successCount, totalLessons))
	if failCount > 0 {
		printInfo(fmt.Sprintf("Failed: %d videos", failCount))
	}

	return nil
}

func printInfo(msg string) {
	log.Println(msg)
}

func downloadResources(ctx context.Context, client *skillshare.Client, webURL, resourceDir string, cookies string) {
	guide, err := client.FetchProjectGuide(ctx, webURL)
	if err != nil {
		log.Printf("WARN: Fetching project guide failed: %v", err)
		return
	}

	attachments := guide.ProjectGuide.Attachments
	instructions := guide.ProjectGuide.ProjectGuideHTML

	if len(attachments) == 0 && instructions == "" {
		if verbose {
			log.Println("No resources available")
		}
		return
	}

	if instructions != "" {
		instPath := filepath.Join(resourceDir, "Project_Instructions.html")
		htmlContent := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"><title>Project Instructions</title>
<style>body { font-family: sans-serif; max-width: 800px; margin: 2em auto; padding: 0 1em; line-height: 1.6; } img { max-width: 100%%; }</style>
</head><body><h1>Project Instructions</h1>%s</body></html>`, instructions)

		if err := os.WriteFile(instPath, []byte(htmlContent), 0o644); err == nil {
			if verbose {
				log.Println("Saved Project_Instructions.html")
			}
		}
	}

	for _, att := range attachments {
		if ctx.Err() != nil {
			return
		}

		if att.URL == "" {
			continue
		}
		filename := cloudflare.SafeFilename(att.Title)
		if filepath.Ext(filename) == "" {
			ext := filepath.Ext(att.URL)
			ext = strings.Split(ext, "?")[0]
			filename += ext
		}

		outputPath := filepath.Join(resourceDir, filename)
		if _, err := os.Stat(outputPath); err == nil {
			continue
		}

		printInfo(fmt.Sprintf("Downloading Resource: %s (%s)", filename, att.Size))

		if err := downloadFile(ctx, att.URL, outputPath, cookies); err != nil {
			log.Printf("ERROR: Failed resource %s: %v", filename, err)
		}
	}
}

func downloadFile(ctx context.Context, urlStr, outputPath, cookies string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
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

	if resp.StatusCode != 200 {
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
