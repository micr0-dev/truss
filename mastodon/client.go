package mastodon

import (
	"context"
	"fmt"
	"html"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/mattn/go-mastodon"
	"github.com/microcosm-cc/bluemonday"
)

type ClientConfig struct {
	Server       string
	ClientID     string
	ClientSecret string
	AccessToken  string
}

type Client struct {
	client *mastodon.Client
}

type Post struct {
	ID          string
	Content     string
	Reblog      *Post
	Visibility  string
	CreatedAt   time.Time
	InReplyToID string
	Hashtags    []string
	EditedAt    time.Time
	OriginalID  string
}

func NewClient(config ClientConfig) (*Client, error) {
	if config.Server == "" {
		return nil, fmt.Errorf("mastodon server URL is required")
	}

	if config.AccessToken == "" {
		return nil, fmt.Errorf("mastodon access token is required")
	}

	// Ensure the server URL has a protocol
	if !strings.HasPrefix(config.Server, "http") {
		config.Server = "https://" + config.Server
	}

	c := mastodon.NewClient(&mastodon.Config{
		Server:       config.Server,
		ClientID:     config.ClientID,
		ClientSecret: config.ClientSecret,
		AccessToken:  config.AccessToken,
	})

	return &Client{client: c}, nil
}

func (c *Client) GetNewPosts(ctx context.Context, sinceID string, sinceTime time.Time) ([]*Post, error) {
	// Get current user account
	account, err := c.client.GetAccountCurrentUser(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting current user: %w", err)
	}

	// Set up pagination
	pg := &mastodon.Pagination{}
	if sinceID != "" {
		pg.SinceID = mastodon.ID(sinceID)
	}

	// Get user's statuses
	timeline, err := c.client.GetAccountStatuses(ctx, account.ID, pg)
	if err != nil {
		return nil, fmt.Errorf("getting timeline: %w", err)
	}

	var posts []*Post
	for _, status := range timeline {
		// Only include posts created after the given time
		if !sinceTime.IsZero() && status.CreatedAt.Before(sinceTime) {
			continue
		}

		// Only include public posts
		if status.Visibility != "public" {
			log.Printf("Skipping non-public post %s with visibility %s", status.ID, status.Visibility)
			continue
		}

		// Extract hashtags
		var hashtags []string
		for _, tag := range status.Tags {
			hashtags = append(hashtags, tag.Name)
		}

		post := &Post{
			ID:         string(status.ID),
			Content:    cleanHTML(status.Content, hashtags),
			Visibility: status.Visibility,
			CreatedAt:  status.CreatedAt,
			InReplyToID: func() string {
				if status.InReplyToID != nil {
					if id, ok := status.InReplyToID.(string); ok {
						return id
					}
				}
				return ""
			}(),
			Hashtags: hashtags,
			EditedAt: status.EditedAt,
		}

		// Check if this is an edit
		if !status.EditedAt.IsZero() {
			post.OriginalID = string(status.ID)
		}

		if status.Reblog != nil {
			reblogHashtags := []string{}
			for _, tag := range status.Reblog.Tags {
				reblogHashtags = append(reblogHashtags, tag.Name)
			}

			post.Reblog = &Post{
				ID:         string(status.Reblog.ID),
				Content:    cleanHTML(status.Reblog.Content, reblogHashtags),
				Visibility: status.Reblog.Visibility,
				CreatedAt:  status.Reblog.CreatedAt,
				InReplyToID: func() string {
					if status.Reblog.InReplyToID != nil {
						if id, ok := status.Reblog.InReplyToID.(string); ok {
							return id
						}
					}
					return ""
				}(),
				Hashtags: reblogHashtags,
			}
		}

		posts = append(posts, post)
	}

	return posts, nil
}

// cleanHTML removes HTML tags and converts HTML entities
func cleanHTML(input string, hashtags []string) string {
	// Use bluemonday to strip HTML tags safely
	p := bluemonday.StripTagsPolicy()

	// Replace common HTML elements with appropriate text replacements
	input = strings.ReplaceAll(input, "<br>", "\n")
	input = strings.ReplaceAll(input, "<br/>", "\n")
	input = strings.ReplaceAll(input, "<br />", "\n")
	input = strings.ReplaceAll(input, "</p><p>", "\n\n")

	// Strip all remaining HTML tags
	clean := p.Sanitize(input)

	// Replace HTML entities
	clean = html.UnescapeString(clean)

	// Remove hashtags that were used for filtering
	for _, tag := range hashtags {
		// Remove the hashtag with various surrounding patterns
		// This handles hashtags at start, middle, or end of text
		clean = strings.ReplaceAll(clean, " #"+tag+" ", " ")
		clean = strings.ReplaceAll(clean, " #"+tag+"\n", " \n")
		clean = strings.ReplaceAll(clean, "\n#"+tag+" ", "\n")
		clean = strings.ReplaceAll(clean, "\n#"+tag+"\n", "\n")
		clean = strings.ReplaceAll(clean, " #"+tag, " ")
		clean = strings.ReplaceAll(clean, "\n#"+tag, "\n")

		// Handle case where the hashtag is alone on the line
		clean = strings.ReplaceAll(clean, "#"+tag, "")
	}

	// Clean up multiple newlines
	re := regexp.MustCompile(`\n{3,}`)
	clean = re.ReplaceAllString(clean, "\n\n")

	// Trim whitespace
	clean = strings.TrimSpace(clean)

	return clean
}

func (c *Client) GetAccount(ctx context.Context) (*mastodon.Account, error) {
	// For debugging
	log.Printf("Using Mastodon server: %s", c.client.Config.Server)

	// Try to get current user account
	account, err := c.client.GetAccountCurrentUser(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting current user: %w", err)
	}

	return account, nil
}

func (c *Client) CheckForEdits(ctx context.Context, postIDs []string, since time.Time) (map[string]*Post, error) {
	edits := make(map[string]*Post)

	// For each post we've already bridged, check if it's been edited
	for _, id := range postIDs {
		status, err := c.client.GetStatus(ctx, mastodon.ID(id))
		if err != nil {
			log.Printf("Could not check status %s for edits: %v", id, err)
			continue
		}

		// If the post has been edited since we last checked
		if !status.EditedAt.IsZero() && status.EditedAt.After(since) {
			// This post has been edited
			var hashtags []string
			for _, tag := range status.Tags {
				hashtags = append(hashtags, tag.Name)
			}

			post := &Post{
				ID:         string(status.ID),
				Content:    cleanHTML(status.Content, hashtags),
				Visibility: status.Visibility,
				CreatedAt:  status.CreatedAt,
				InReplyToID: func() string {
					if status.InReplyToID != nil {
						if id, ok := status.InReplyToID.(string); ok {
							return id
						}
					}
					return ""
				}(),
				Hashtags:   hashtags,
				EditedAt:   status.EditedAt,
				OriginalID: string(status.ID), // Same ID for edits in Mastodon
			}

			edits[string(status.ID)] = post
		}
	}

	return edits, nil
}
