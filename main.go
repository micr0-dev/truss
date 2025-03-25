package main

import (
	"context"
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

	// Get last time we checked for edits
	lastEditCheck, err := b.db.GetLastCheckTime()
	if err != nil {
		log.Printf("Couldn't get last edit check time: %v", err)
		lastEditCheck = time.Now()
	}

	// Start time for this run
	startTime := time.Now()

	newPostTicker := time.NewTicker(time.Duration(b.config.PollInterval) * time.Second)
	defer newPostTicker.Stop()

	// Check for edits less frequently (e.g., every 5 minutes)
	editCheckTicker := time.NewTicker(5 * time.Minute)
	defer editCheckTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-newPostTicker.C:
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

		case <-editCheckTicker.C:
			// Check for edits to existing posts
			log.Println("Checking for edits to existing posts...")

			// Get all posts we've bridged
			bridgedIDs, err := b.db.GetBridgedPostIDs()
			if err != nil {
				log.Printf("Error getting bridged post IDs: %v", err)
				continue
			}

			if len(bridgedIDs) == 0 {
				continue
			}

			// Check for edits
			edits, err := b.mastodon.CheckForEdits(ctx, bridgedIDs, lastEditCheck)
			if err != nil {
				log.Printf("Error checking for edits: %v", err)
				continue
			}

			if len(edits) > 0 {
				log.Printf("Found %d edited posts", len(edits))

				for _, post := range edits {
					log.Printf("Processing edit for post %s (edited at %s)",
						post.ID, post.EditedAt.Format(time.RFC3339))
					if err := b.ProcessPost(ctx, post); err != nil {
						log.Printf("Error processing edited post %s: %v", post.ID, err)
					}
				}
			}

			// Update the last check time
			if err := b.db.SaveLastCheckTime(time.Now()); err != nil {
				log.Printf("Error saving last edit check time: %v", err)
			}

			lastEditCheck = time.Now()
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

	// Handle reply to our own post
	var parentUri, parentCid string

	if post.InReplyToID != "" {
		// Check if we've bridged the parent post
		parentBskyIDs, err := b.db.GetBlueskyIDsForMastodonPost(post.InReplyToID)
		if err == nil && len(parentBskyIDs) > 0 {
			// We found the parent post, this is a reply to our own post
			log.Printf("Post %s is a reply to our own post %s", post.ID, post.InReplyToID)

			// Get the last part of the parent thread
			lastParentID := parentBskyIDs[len(parentBskyIDs)-1]
			parts := strings.Split(lastParentID, "|")
			if len(parts) == 2 {
				parentUri = parts[0]
				parentCid = parts[1]
			}
		}
	}

	// Check if this is an edit
	origPostID, isEdit := b.db.CheckIfEdit(post.ID, post.OriginalID)
	if isEdit {
		log.Printf("Post %s is an edit of %s, deleting previous posts", post.ID, origPostID)

		// Get all Bluesky posts for the original Mastodon post
		bskyIDs, err := b.db.GetBlueskyIDsForMastodonPost(origPostID)
		if err != nil {
			log.Printf("Error getting Bluesky IDs for edited post: %v", err)
		} else {
			// Delete all previous Bluesky posts for this Mastodon post
			for _, id := range bskyIDs {
				parsedID := id
				parts := strings.Split(id, "|")
				if len(parts) > 0 {
					// Extract the URI from the combined ID
					uriParts := strings.Split(parts[0], "/")
					if len(uriParts) >= 4 {
						parsedID = uriParts[len(uriParts)-1]
					}
				}

				if err := b.bluesky.DeletePost(ctx, parsedID); err != nil {
					log.Printf("Error deleting Bluesky post %s: %v", parsedID, err)
				}
			}
		}

		// We'll create new posts below with the updated content
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
	// For edits, we store the mapping under the new post ID
	if err := b.db.SavePostMapping(post.ID, bskyIDs); err != nil {
		log.Printf("Error saving post mapping: %v", err)
	}

	// If this was an edit, also update the mapping for the original post
	if isEdit && origPostID != post.ID {
		if err := b.db.SavePostMapping(origPostID, bskyIDs); err != nil {
			log.Printf("Error updating original post mapping: %v", err)
		}
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
