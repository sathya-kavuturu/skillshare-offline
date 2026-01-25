package skillshare

import (
	"regexp"
)

// ClassData represents the response from the Skillshare class API
type ClassData struct {
	ID                         int    `json:"id"`
	Title                      string `json:"title"`
	ProjectTitle               string `json:"project_title"`
	ImageHuge                  string `json:"image_huge"`
	ImageSmall                 string `json:"image_small"`
	ImageThumbnail             string `json:"image_thumbnail"`
	WebURL                     string `json:"web_url"`
	Category                   string `json:"category"`
	NumVideos                  int    `json:"num_videos"`
	TotalVideosDuration        string `json:"total_videos_duration"`
	TotalVideosDurationSeconds int    `json:"total_videos_duration_seconds"`
	Embedded                   struct {
		Teacher  Teacher  `json:"teacher"`
		Sessions Sessions `json:"sessions"`
	} `json:"_embedded"`
}

// Teacher represents the class teacher/instructor
type Teacher struct {
	ID        int    `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	FullName  string `json:"full_name"`
	Headline  string `json:"headline"`
}

// Sessions wraps the list of video sessions
type Sessions struct {
	Embedded struct {
		Sessions []Session `json:"sessions"`
	} `json:"_embedded"`
}

// Session represents a single video lesson
type Session struct {
	ID                   int    `json:"id"`
	Index                int    `json:"index"`
	Title                string `json:"title"`
	Rank                 int    `json:"rank"`
	VideoHashedID        string `json:"video_hashed_id"`
	VideoDuration        string `json:"video_duration"`
	VideoDurationSeconds int    `json:"video_duration_seconds"`
	VideoThumbnailURL    string `json:"video_thumbnail_url"`
	IsCloudflareReady    bool   `json:"is_cloudflare_ready"`
	Links                struct {
		Self     Link `json:"self"`
		Download Link `json:"download"`
		Stream   Link `json:"stream"`
	} `json:"_links"`
}

// Link represents an API link with href
type Link struct {
	Href  string `json:"href"`
	Title string `json:"title,omitempty"`
}

// GetSessions returns all sessions/lessons from the class data
func (c *ClassData) GetSessions() []Session {
	return c.Embedded.Sessions.Embedded.Sessions
}

// Lesson is a simplified representation for downloading
type Lesson struct {
	Index         int
	Title         string
	Duration      int
	StreamURL     string // Cloudflare Stream HLS manifest URL
	ThumbnailURL  string
	IsCloudflare  bool
	VideoHashedID string
}

// ToLessons converts class data sessions to simplified Lesson structs
func (c *ClassData) ToLessons() []Lesson {
	sessions := c.GetSessions()
	lessons := make([]Lesson, 0, len(sessions))

	for _, s := range sessions {
		lessons = append(lessons, Lesson{
			Index:         s.Rank,
			Title:         s.Title,
			Duration:      s.VideoDurationSeconds,
			StreamURL:     s.Links.Stream.Href,
			ThumbnailURL:  s.VideoThumbnailURL,
			IsCloudflare:  s.IsCloudflareReady,
			VideoHashedID: s.VideoHashedID,
		})
	}

	return lessons
}

// ClassResources represents downloadable resources for a class
type ClassResources struct {
	Embedded struct {
		Attachments []Attachment `json:"attachments"`
	} `json:"_embedded"`
}

// Attachment represents a downloadable file attachment
type Attachment struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	URL         string `json:"url"`
	Links       struct {
		Download Link `json:"download"`
	} `json:"_links"`
}

// ProjectsResponse represents the response from the projects API
type ProjectsResponse struct {
	Collection struct {
		Projects []Project `json:"projects"`
	} `json:"collection"`
}

// Project represents a student project
type Project struct {
	ID           int    `json:"id"`
	Title        string `json:"title"`
	URL          string `json:"url"`
	Image        string `json:"image"`
	CoverFull    string `json:"cover_full"`
	ProjectBody  string `json:"projectBody"`
	ProjectCover string `json:"projectCover"`
	Author       struct {
		FullName string `json:"fullName"`
		Username any    `json:"username"` // Can be int or string
	} `json:"author"`
}

// GetImageURLs extracts all image URLs from a project's body HTML
func (p *Project) GetImageURLs() []string {
	var urls []string

	// Add cover images if present
	if p.Image != "" && p.Image != "https://static.skillshare.com/assets/images/projects/project-cover-image.webp" {
		urls = append(urls, p.Image)
	}
	if p.CoverFull != "" && p.CoverFull != p.Image {
		urls = append(urls, p.CoverFull)
	}

	// Extract images from projectBody HTML
	// Look for src attributes in img tags
	if p.ProjectBody != "" {
		// Simple regex to find image URLs in the HTML
		imgPattern := `src="(https://static\.skillshare\.com/uploads/project/[^"]+)"`
		re := regexp.MustCompile(imgPattern)
		matches := re.FindAllStringSubmatch(p.ProjectBody, -1)
		for _, match := range matches {
			if len(match) > 1 {
				urls = append(urls, match[1])
			}
		}
	}

	return urls
}

// ProjectGuideResponse represents the JSON response from the class projects page
type ProjectGuideResponse struct {
	ProjectGuide ProjectGuide `json:"projectGuide"`
}

// ProjectGuide contains instructions and attachments
type ProjectGuide struct {
	Attachments      []ProjectAttachment `json:"attachments"`
	ProjectGuideHTML string              `json:"project_guide"`
}

// ProjectAttachment represents a downloadable resource from the project guide
type ProjectAttachment struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	Size  string `json:"size"`
}
