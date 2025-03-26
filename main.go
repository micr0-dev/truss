package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"truss/bluesky"
	"truss/config"
	"truss/mastodon"
)

func main() {
	configPath := flag.String("config", "config.toml", "Path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Try bluesky first
	bsky, err := bluesky.NewClient(cfg.Bluesky)
	if err != nil {
		log.Fatalf("Failed to create Bluesky client: %v", err)
	}

	// Make sure we can authenticate with Bluesky
	err = bsky.TestAuth(context.Background())
	if err != nil {
		log.Fatalf("Bluesky authentication failed: %v", err)
	}

	// Print details about bluesky account
	did := bsky.GetDID()
	log.Printf("Bluesky account DID: %s", did)

	// Now try Mastodon
	masto, err := mastodon.NewClient(cfg.Mastodon)
	if err != nil {
		log.Fatalf("Failed to create Mastodon client: %v", err)
	}

	// Try to get account info
	account, err := masto.GetAccount(context.Background())
	if err != nil {
		log.Fatalf("Failed to get Mastodon account: %v", err)
	}

	log.Printf("Mastodon account: %s", account.Acct)

	// Continue with the bridge setup...
	bridge := NewBridge(masto, bsky, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-c
		log.Println("Shutting down...")
		cancel()
	}()

	if err := bridge.Run(ctx); err != nil {
		log.Fatalf("Bridge failed: %v", err)
	}
}

type Bridge struct {
	mastodon *mastodon.Client
	bluesky  *bluesky.Client
	config   *config.Config
	db       *Database
}

func NewBridge(masto *mastodon.Client, bsky *bluesky.Client, cfg *config.Config) *Bridge {
	db, err := NewDatabase(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	return &Bridge{
		mastodon: masto,
		bluesky:  bsky,
		config:   cfg,
		db:       db,
	}
}

func (b *Bridge) Run(ctx context.Context) error {
	log.Println("Starting Truss bridge...")

	// Get last seen ID from database
	lastID, err := b.db.GetLastSeenID()
	if err != nil {
		log.Printf("Couldn't get last seen ID, starting from scratch: %v", err)
	}

	// Start time for this run
	startTime := time.Now()

	// Create a ticker for normal post polling
	postTicker := time.NewTicker(time.Duration(b.config.PollInterval) * time.Second)
	defer postTicker.Stop()

	// Create a ticker for edit checking
	editTicker := time.NewTicker(time.Duration(b.config.PollInterval) * time.Second * 2)
	defer editTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-postTicker.C:
			log.Println("Checking for new posts...")
			// Handle new posts
			posts, err := b.mastodon.GetNewPosts(ctx, lastID, startTime)
			if err != nil {
				log.Printf("Error fetching posts: %v", err)
				continue
			}

			if len(posts) > 0 {
				log.Printf("Found %d new posts", len(posts))

				// Process posts in chronological order
				for i := len(posts) - 1; i >= 0; i-- {
					post := posts[i]
					if err := b.ProcessPost(ctx, post); err != nil {
						log.Printf("Error processing post %s: %v", post.ID, err)
						continue
					}
					lastID = post.ID
				}

				if err := b.db.SaveLastSeenID(lastID); err != nil {
					log.Printf("Error saving last seen ID: %v", err)
				}
			}

		case <-editTicker.C:
			log.Println("Checking for post edits...")
			// Check for edits (only check the 10 most recent posts)
			recentIDs, err := b.db.GetRecentPostsToCheckForEdits(10)
			if err != nil {
				log.Printf("Error getting recent posts to check: %v", err)
				continue
			}

			for _, id := range recentIDs {
				post, err := b.mastodon.GetPostWithEdits(ctx, id)
				if err != nil {
					log.Printf("Error checking post %s for edits: %v", id, err)
					continue
				}

				// Calculate new content hash
				newContentHash := hashPostContent(post.Content)

				// Get the stored hash
				oldContentHash, err := b.db.GetContentHash(id)
				if err != nil {
					log.Printf("Error getting content hash for post %s: %v", id, err)
					continue
				}

				// Only process if content actually changed
				if newContentHash != oldContentHash {
					log.Printf("Content changed for post %s (hash: %s -> %s), reprocessing",
						id, oldContentHash[:8], newContentHash[:8])

					// Process the updated post
					if err := b.ProcessPost(ctx, post); err != nil {
						log.Printf("Error processing edited post %s: %v", id, err)
						continue
					}
				}
			}
		}
	}
}

func (b *Bridge) ProcessPost(ctx context.Context, post *mastodon.Post) error {
	// Skip boosts/reblogs for now
	if post.Reblog != nil {
		log.Printf("Skipping reblog: %s", post.ID)
		return nil
	}

	// Skip non-public posts
	if post.Visibility != "public" {
		log.Printf("Skipping non-public post: %s (visibility: %s)", post.ID, post.Visibility)
		return nil
	}

	// If hashtag filtering is enabled, check for the required hashtag
	if b.config.FilterHashtag != "" {
		hasFilterTag := false
		for _, tag := range post.Hashtags {
			if strings.EqualFold(tag, b.config.FilterHashtag) {
				hasFilterTag = true
				break
			}
		}

		if !hasFilterTag {
			log.Printf("Skipping post %s without required hashtag #%s", post.ID, b.config.FilterHashtag)
			return nil
		}
	}

	// Calculate content hash
	contentHash := hashPostContent(post.Content)

	// Check if we've already processed this exact content
	existingHash, err := b.db.GetContentHash(post.ID)
	if err == nil && existingHash == contentHash {
		log.Printf("Post %s content unchanged (hash: %s), skipping", post.ID, contentHash[:8])
		return nil
	}

	// If we're here, either it's a new post or the content has changed
	if existingHash != "" {
		log.Printf("Post %s content changed (hash: %s -> %s), reprocessing",
			post.ID, existingHash[:8], contentHash[:8])

		// Delete any existing posts for this ID
		bskyIDs, err := b.db.GetBlueskyIDsForMastodonPost(post.ID)
		if err == nil && len(bskyIDs) > 0 {
			log.Printf("Found %d existing Bluesky posts to delete", len(bskyIDs))

			// Delete all previous posts
			for _, id := range bskyIDs {
				if err := b.bluesky.DeletePost(ctx, id); err != nil {
					log.Printf("Error deleting Bluesky post %s: %v", id, err)
				}
			}
		}
	}

	// Handle reply to our own post or another bridged post
	var parentUri, parentCid string

	if post.InReplyToID != "" {
		// First, check if we've bridged the parent post ourselves
		parentBskyIDs, err := b.db.GetBlueskyIDsForMastodonPost(post.InReplyToID)
		if err == nil && len(parentBskyIDs) > 0 {
			// We found the parent post, this is a reply to our own post
			log.Printf("Post %s is a reply to our own bridged post %s", post.ID, post.InReplyToID)

			// Get the last part of the parent thread
			lastParentID := parentBskyIDs[len(parentBskyIDs)-1]
			parts := strings.Split(lastParentID, "|")
			if len(parts) == 2 {
				parentUri = parts[0]
				parentCid = parts[1]
			}
		} else {
			// We haven't bridged this post - try to find it on Mastodon
			parentPost, err := b.mastodon.GetPostWithEdits(ctx, post.InReplyToID)
			if err != nil {
				log.Printf("Error getting parent post %s: %v", post.InReplyToID, err)
			} else {
				if parentPost.Username != "" && parentPost.Instance != "" {
					// Look up this post on Bluesky via our more robust method
					log.Printf("Looking for parent post %s by %s@%s (%s) on Bluesky",
						post.InReplyToID, parentPost.Username, parentPost.Instance, parentPost.DisplayName)

					parentUri, parentCid, err = b.bluesky.LookupBridgedMastodonPost(
						ctx,
						post.InReplyToID,
						parentPost.Username,
						parentPost.Instance,
						parentPost.Content,
						parentPost.DisplayName,
						parentPost.CreatedAt)

					if err != nil {
						log.Printf("Could not find parent post on Bluesky: %v", err)
						// If we can't find the parent post, should we skip this post?
						log.Printf("Skipping post %s as we can't find the parent", post.ID)
						return nil
					}

					log.Printf("Found parent post on Bluesky: %s", parentUri)
				}
			}
		}

		// If we still haven't found a parent, we should skip this post
		if parentUri == "" {
			log.Printf("Skipping post %s as we can't find the parent post to reply to", post.ID)
			return nil
		}
	}

	// Split content if needed and post to Bluesky
	parts := splitContent(post.Content)

	var bskyIDs []string
	var lastUri, lastCid string

	// If this is a reply to our own post, use the parent's information
	if parentUri != "" && parentCid != "" {
		lastUri = parentUri
		lastCid = parentCid
	}

	for i, part := range parts {
		// Double check length before posting
		if len(part) > 300 {
			log.Printf("WARNING: Part %d still too long (%d chars), truncating", i+1, len(part))
			part = part[:297] + "..."
		}

		var result string
		var err error

		// Add a small delay between posts to avoid rate limits
		if i > 0 {
			time.Sleep(500 * time.Millisecond)
		}

		if i == 0 && parentUri == "" && parentCid == "" {
			// First post in a new thread
			log.Printf("Creating initial post (part %d/%d, length: %d): %s",
				i+1, len(parts), len(part), truncateForLog(part))
			result, err = b.bluesky.CreatePost(ctx, part)
		} else {
			// Reply to either the parent post or the previous post in the thread
			log.Printf("Creating reply post (part %d/%d, length: %d): %s",
				i+1, len(parts), len(part), truncateForLog(part))
			result, err = b.bluesky.CreateReply(ctx, part, lastCid, lastUri)
		}

		if err != nil {
			log.Printf("Error creating Bluesky post: %v", err)
			// Try to clean up posts we already made
			for _, id := range bskyIDs {
				parts := strings.Split(id, "|")
				if len(parts) > 0 {
					b.bluesky.DeletePost(ctx, parts[0])
				}
			}
			return err
		}

		// Split the result into URI and CID
		resultParts := strings.Split(result, "|")
		if len(resultParts) != 2 {
			log.Printf("Unexpected result format: %s", result)
			continue
		}

		lastUri = resultParts[0]
		lastCid = resultParts[1]

		// Store the full result for mapping
		bskyIDs = append(bskyIDs, result)
	}

	// Store the mapping in the database
	if err := b.db.SavePostMapping(post.ID, bskyIDs); err != nil {
		log.Printf("Error saving post mapping: %v", err)
	}

	// Store the content hash
	if err := b.db.SaveContentHash(post.ID, contentHash); err != nil {
		log.Printf("Error saving content hash: %v", err)
	}

	return nil
}

// Helper function to truncate text for log messages
func truncateForLog(text string) string {
	const maxLogLength = 50
	if len(text) <= maxLogLength {
		return text
	}
	return text[:maxLogLength-3] + "..."
}

// splitContent splits text into parts that fit within Bluesky's character limit
func splitContent(content string) []string {
	const maxLength = 300

	if len(content) <= maxLength {
		return []string{content}
	}

	var parts []string
	remaining := content
	partCount := 0

	// First, estimate how many parts we'll need
	// This helps us reserve space for "(n/total)" suffixes
	estimatedTotal := (len(content) + maxLength - 1) / (maxLength - 10)
	suffixSize := len(fmt.Sprintf(" (%d/%d)", estimatedTotal, estimatedTotal))
	effectiveMaxLength := maxLength - suffixSize

	for len(remaining) > 0 {
		partCount++

		if len(remaining) <= effectiveMaxLength {
			// Last part fits completely
			parts = append(parts, remaining)
			break
		}

		// Find a good breaking point - look for a space
		breakPoint := effectiveMaxLength

		// Move back to find a space
		for breakPoint > 0 && remaining[breakPoint] != ' ' {
			breakPoint--
		}

		// If no space found in reasonable range, break at a character boundary
		if breakPoint < effectiveMaxLength/2 {
			// Try forward for a space instead
			breakPoint = effectiveMaxLength / 2
			for i := breakPoint; i < min(effectiveMaxLength, len(remaining)); i++ {
				if remaining[i] == ' ' {
					breakPoint = i
					break
				}
			}

			// If still no good position, just break at effective max length
			if breakPoint < effectiveMaxLength/2 || breakPoint == effectiveMaxLength/2 {
				breakPoint = effectiveMaxLength
			}
		}

		// Extract this part
		parts = append(parts, remaining[:breakPoint])

		// Move to next
		if breakPoint < len(remaining) && remaining[breakPoint] == ' ' {
			remaining = remaining[breakPoint+1:] // Skip the space
		} else {
			remaining = remaining[breakPoint:]
		}
	}

	// Now add the part indicators
	for i := range parts {
		parts[i] = parts[i] + fmt.Sprintf(" (%d/%d)", i+1, len(parts))
	}

	return parts
}

// hashPostContent creates a consistent hash of post content
func hashPostContent(content string) string {
	hasher := sha256.New()
	hasher.Write([]byte(content))
	return hex.EncodeToString(hasher.Sum(nil))
}
