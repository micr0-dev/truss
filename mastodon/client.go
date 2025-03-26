package mastodon

import (
	"context"
	"fmt"
	"html"
	"log"
	"regexp"
	"strings"
	"time"
	"unicode"

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
	Username    string
	Instance    string
	DisplayName string
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

		isReply := status.InReplyToID != ""

		post := &Post{
			ID:         string(status.ID),
			Content:    cleanHTML(status.Content, hashtags, isReply),
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

			reblogIsReply := status.Reblog.InReplyToID != ""

			post.Reblog = &Post{
				ID:         string(status.Reblog.ID),
				Content:    cleanHTML(status.Reblog.Content, reblogHashtags, reblogIsReply),
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
func cleanHTML(input string, hashtags []string, isReply bool) string {
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

	// If this is a reply, remove leading mentions
	if isReply {
		// Split by lines to handle each paragraph
		lines := strings.Split(clean, "\n")
		for i, line := range lines {
			// Only apply to lines that start with mentions
			trimmedLine := strings.TrimSpace(line)
			if strings.HasPrefix(trimmedLine, "@") {
				// Handle leading mentions, possibly with punctuation
				pattern := regexp.MustCompile(`^(@[\w\.-]+(?:@[\w\.-]+)?[\s,:;]?)+\s*`)
				lines[i] = pattern.ReplaceAllString(line, "")

				// Capitalize first letter of remaining text if needed
				if len(lines[i]) > 0 {
					runes := []rune(lines[i])
					if unicode.IsLetter(runes[0]) && unicode.IsLower(runes[0]) {
						runes[0] = unicode.ToUpper(runes[0])
						lines[i] = string(runes)
					}
				}
			}
		}
		clean = strings.Join(lines, "\n")
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

func (c *Client) GetPostWithEdits(ctx context.Context, postID string) (*Post, error) {
	status, err := c.client.GetStatus(ctx, mastodon.ID(postID))
	if err != nil {
		return nil, fmt.Errorf("getting status: %w", err)
	}

	var hashtags []string
	for _, tag := range status.Tags {
		hashtags = append(hashtags, tag.Name)
	}

	// Extract username and instance from account
	username := status.Account.Username
	instance := extractInstanceFromAcct(status.Account.Acct, c.client.Config.Server)
	displayName := status.Account.DisplayName

	// Check if this is a reply
	isReply := status.InReplyToID != ""

	post := &Post{
		ID:         string(status.ID),
		Content:    cleanHTML(status.Content, hashtags, isReply),
		Visibility: status.Visibility,
		CreatedAt:  status.CreatedAt,
		InReplyToID: func() string {
			if id, ok := status.InReplyToID.(string); ok {
				return id
			}
			return ""
		}(),
		Hashtags:    hashtags,
		Username:    username,
		Instance:    instance,
		DisplayName: displayName,
	}

	// Rest of the function remains the same
	return post, nil
}

func extractInstanceFromAcct(acct string, defaultServer string) string {
	// If it contains @, it's likely a remote account
	if strings.Contains(acct, "@") {
		parts := strings.Split(acct, "@")
		if len(parts) >= 2 {
			return parts[1]
		}
	}

	// For local accounts, extract from the server URL
	server := defaultServer
	if strings.HasPrefix(server, "https://") {
		server = server[8:]
	} else if strings.HasPrefix(server, "http://") {
		server = server[7:]
	}

	// Remove any path
	if slash := strings.IndexByte(server, '/'); slash != -1 {
		server = server[:slash]
	}

	return server
}
