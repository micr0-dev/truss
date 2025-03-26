package bluesky

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	defaultPDS = "https://bsky.social"
)

type ClientConfig struct {
	PDS        string // Default: https://bsky.social
	Identifier string // Username or email
	Password   string // App password
}

type Client struct {
	pds        string
	identifier string
	password   string
	accessJwt  string
	refreshJwt string
	did        string
	expiresAt  time.Time
	httpClient *http.Client
}

func NewClient(config ClientConfig) (*Client, error) {
	pds := config.PDS
	if pds == "" {
		pds = defaultPDS
	}

	c := &Client{
		pds:        pds,
		identifier: config.Identifier,
		password:   config.Password,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}

	// We'll authenticate on first use
	return c, nil
}

func (c *Client) ensureAuth(ctx context.Context) error {
	// If we have a valid token, no need to authenticate
	if c.accessJwt != "" && time.Now().Before(c.expiresAt) {
		return nil
	}

	// Need to authenticate
	req := map[string]string{
		"identifier": c.identifier,
		"password":   c.password,
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshaling auth request: %w", err)
	}

	url := c.pds + "/xrpc/com.atproto.server.createSession"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("creating auth request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("performing auth request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("authentication failed with status %d: %s", resp.StatusCode, body)
	}

	var authResp struct {
		AccessJwt  string `json:"accessJwt"`
		RefreshJwt string `json:"refreshJwt"`
		Did        string `json:"did"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		return fmt.Errorf("decoding auth response: %w", err)
	}

	c.accessJwt = authResp.AccessJwt
	c.refreshJwt = authResp.RefreshJwt
	c.did = authResp.Did
	// Tokens typically expire after 2 hours, but let's be conservative
	c.expiresAt = time.Now().Add(1 * time.Hour)

	return nil
}
func (c *Client) CreateReply(ctx context.Context, text string, parentCid string, parentUri string) (string, error) {
	if err := c.ensureAuth(ctx); err != nil {
		return "", fmt.Errorf("authentication failed: %w", err)
	}

	// Create reply record
	record := map[string]interface{}{
		"$type":     "app.bsky.feed.post",
		"text":      text,
		"createdAt": time.Now().Format(time.RFC3339),
		"reply": map[string]interface{}{
			"root": map[string]interface{}{
				"cid": parentCid,
				"uri": parentUri,
			},
			"parent": map[string]interface{}{
				"cid": parentCid,
				"uri": parentUri,
			},
		},
	}

	req := map[string]interface{}{
		"repo":       c.did,
		"collection": "app.bsky.feed.post",
		"record":     record,
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshaling reply request: %w", err)
	}

	url := c.pds + "/xrpc/com.atproto.repo.createRecord"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("creating reply request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.accessJwt)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("performing reply request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("reply creation failed with status %d: %s", resp.StatusCode, body)
	}

	var postResp struct {
		Uri string `json:"uri"`
		Cid string `json:"cid"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&postResp); err != nil {
		return "", fmt.Errorf("decoding reply response: %w", err)
	}

	// Return the complete response instead of just the ID
	return postResp.Uri + "|" + postResp.Cid, nil
}

// Update the CreatePost method to also return the URI and CID
func (c *Client) CreatePost(ctx context.Context, text string) (string, error) {
	if err := c.ensureAuth(ctx); err != nil {
		return "", fmt.Errorf("authentication failed: %w", err)
	}

	// Create record
	record := map[string]interface{}{
		"$type":     "app.bsky.feed.post",
		"text":      text,
		"createdAt": time.Now().Format(time.RFC3339),
	}

	req := map[string]interface{}{
		"repo":       c.did,
		"collection": "app.bsky.feed.post",
		"record":     record,
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshaling post request: %w", err)
	}

	url := c.pds + "/xrpc/com.atproto.repo.createRecord"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("creating post request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.accessJwt)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("performing post request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("post creation failed with status %d: %s", resp.StatusCode, body)
	}

	var postResp struct {
		Uri string `json:"uri"`
		Cid string `json:"cid"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&postResp); err != nil {
		return "", fmt.Errorf("decoding post response: %w", err)
	}

	// Return both URI and CID
	return postResp.Uri + "|" + postResp.Cid, nil
}

// DeletePost deletes a post on Bluesky
func (c *Client) DeletePost(ctx context.Context, recordID string) error {
	if err := c.ensureAuth(ctx); err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	// Extract the record ID from the different possible formats
	// Format 1: URI|CID
	// Format 2: at://did:plc:xxx/app.bsky.feed.post/xxx
	// Format 3: just the record ID

	// Check if it contains a pipe (Format 1)
	if strings.Contains(recordID, "|") {
		parts := strings.Split(recordID, "|")
		if len(parts) >= 1 {
			uriParts := strings.Split(parts[0], "/")
			if len(uriParts) >= 4 {
				recordID = uriParts[len(uriParts)-1]
			}
		}
	} else if strings.HasPrefix(recordID, "at://") {
		// Format 2: Full URI
		parts := strings.Split(recordID, "/")
		if len(parts) >= 4 {
			recordID = parts[len(parts)-1]
		}
	}
	// Format 3: already just the record ID, no need to change

	req := map[string]interface{}{
		"repo":       c.did,
		"collection": "app.bsky.feed.post",
		"rkey":       recordID,
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshaling delete request: %w", err)
	}

	url := c.pds + "/xrpc/com.atproto.repo.deleteRecord"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("creating delete request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.accessJwt)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("performing delete request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("post deletion failed with status %d: %s", resp.StatusCode, body)
	}

	return nil
}

func (c *Client) GetDID() string {
	// Ensure we're authenticated
	err := c.ensureAuth(context.Background())
	if err != nil {
		log.Printf("Failed to authenticate with Bluesky: %v", err)
		return ""
	}
	return c.did
}

// TestAuth tests authentication with Bluesky
func (c *Client) TestAuth(ctx context.Context) error {
	return c.ensureAuth(ctx)
}

func (c *Client) LookupBridgyFedPost(ctx context.Context, mastodonUser string, mastodonInstance string, mastodonPostID string) (string, string, error) {
	if err := c.ensureAuth(ctx); err != nil {
		return "", "", fmt.Errorf("authentication failed: %w", err)
	}

	// Convert Mastodon user@instance to Bridgy Fed handle format
	bridgyHandle := fmt.Sprintf("%s.%s.ap.brid.gy", mastodonUser, mastodonInstance)
	log.Printf("Looking for post from Bridgy Fed user: %s", bridgyHandle)

	// First, look up the DID for this handle
	url := c.pds + "/xrpc/com.atproto.identity.resolveHandle"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", "", fmt.Errorf("creating handle resolve request: %w", err)
	}

	q := req.URL.Query()
	q.Add("handle", bridgyHandle)
	req.URL.RawQuery = q.Encode()

	req.Header.Set("Authorization", "Bearer "+c.accessJwt)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("performing handle resolve request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("handle resolution failed with status %d: %s", resp.StatusCode, body)
	}

	var resolveResp struct {
		Did string `json:"did"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&resolveResp); err != nil {
		return "", "", fmt.Errorf("decoding handle resolution response: %w", err)
	}

	did := resolveResp.Did
	if did == "" {
		return "", "", fmt.Errorf("could not resolve handle %s", bridgyHandle)
	}

	log.Printf("Resolved handle %s to DID: %s", bridgyHandle, did)

	// Now get the user's recent posts
	url = c.pds + "/xrpc/app.bsky.feed.getAuthorFeed"
	req, err = http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", "", fmt.Errorf("creating author feed request: %w", err)
	}

	q = req.URL.Query()
	q.Add("actor", did)
	q.Add("limit", "100") // Get a decent number of posts to search through
	req.URL.RawQuery = q.Encode()

	req.Header.Set("Authorization", "Bearer "+c.accessJwt)

	resp, err = c.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("performing author feed request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("author feed request failed with status %d: %s", resp.StatusCode, body)
	}

	var feedResp struct {
		Feed []struct {
			Post struct {
				Uri    string `json:"uri"`
				Cid    string `json:"cid"`
				Record struct {
					Text        string `json:"text"`
					ExternalUrl string `json:"external"`
				} `json:"record"`
			} `json:"post"`
		} `json:"feed"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&feedResp); err != nil {
		return "", "", fmt.Errorf("decoding author feed response: %w", err)
	}

	// Look for a post that references the original Mastodon post ID in its external URL
	for _, item := range feedResp.Feed {
		if strings.Contains(item.Post.Record.ExternalUrl, mastodonPostID) {
			log.Printf("Found matching Bridgy Fed post: %s", item.Post.Uri)
			return item.Post.Uri, item.Post.Cid, nil
		}
	}

	return "", "", fmt.Errorf("no matching post found for Mastodon ID %s", mastodonPostID)
}

// bluesky/client.go
// Add this function to search for posts by content and display name
func (c *Client) LookupBridgedMastodonPost(ctx context.Context, mastodonPostID string,
	mastodonUser string, mastodonInstance string,
	postContent string, displayName string,
	postDate time.Time) (string, string, error) {
	if err := c.ensureAuth(ctx); err != nil {
		return "", "", fmt.Errorf("authentication failed: %w", err)
	}

	// Try existing methods first
	// Generate possible Bridgy Fed handles
	possibleHandles := []string{
		// Standard Bridgy Fed format
		fmt.Sprintf("%s.%s.ap.brid.gy", mastodonUser, mastodonInstance),

		// Alternative formats some users might use
		fmt.Sprintf("%s.%s.ap.bridgy.fed", mastodonUser, mastodonInstance),
		fmt.Sprintf("%s_%s.ap.brid.gy", mastodonUser, mastodonInstance),
		fmt.Sprintf("%s-%s.ap.brid.gy", mastodonUser, mastodonInstance),
	}

	// Try each possible handle
	for _, handle := range possibleHandles {
		log.Printf("Trying to find post via handle: %s", handle)

		// Try to resolve the handle to a DID
		did, err := c.resolveHandle(ctx, handle)
		if err != nil {
			log.Printf("Could not resolve handle %s: %v", handle, err)
			continue
		}

		// Try to find the post in this user's feed
		uri, cid, err := c.findPostInUserFeed(ctx, did, mastodonPostID)
		if err == nil && uri != "" && cid != "" {
			return uri, cid, nil
		}
	}

	// If we haven't found it yet, try using Bluesky's search functionality
	// Look for posts that might contain links to the original Mastodon post
	searchTerm := fmt.Sprintf("%s/%s", mastodonInstance, mastodonPostID)
	log.Printf("Trying to find post via search term: %s", searchTerm)

	uri, cid, err := c.searchForPost(ctx, searchTerm, mastodonPostID)
	if err == nil && uri != "" && cid != "" {
		return uri, cid, nil
	}

	// Last line of defense: search for posts with similar content and display name
	if postContent != "" {
		log.Printf("Trying to find post via content matching")

		// Use the first few words of the post (up to 30 chars) as a search term
		// This increases the chance of finding it while limiting false positives
		searchContent := postContent
		if len(searchContent) > 30 {
			words := strings.Fields(searchContent)
			searchContent = ""
			for _, word := range words {
				if len(searchContent)+len(word)+1 <= 30 {
					if searchContent != "" {
						searchContent += " "
					}
					searchContent += word
				} else {
					break
				}
			}
		}

		log.Printf("Searching for content: '%s'", searchContent)

		uri, cid, err := c.findPostByContentAndName(ctx, searchContent, displayName, postDate)
		if err == nil && uri != "" && cid != "" {
			return uri, cid, nil
		}
	}

	// If all else fails, return not found
	return "", "", fmt.Errorf("could not find Mastodon post %s on Bluesky", mastodonPostID)
}

// Helper to find a post by content and display name
func (c *Client) findPostByContentAndName(ctx context.Context, content string, displayName string, postDate time.Time) (string, string, error) {
	url := c.pds + "/xrpc/app.bsky.feed.searchPosts"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", "", fmt.Errorf("creating search request: %w", err)
	}

	q := req.URL.Query()
	q.Add("q", content)
	q.Add("limit", "30") // Get more results to increase chances of finding a match
	req.URL.RawQuery = q.Encode()

	req.Header.Set("Authorization", "Bearer "+c.accessJwt)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("performing search request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("search request failed with status %d: %s", resp.StatusCode, body)
	}

	var searchResp struct {
		Posts []struct {
			Uri    string `json:"uri"`
			Cid    string `json:"cid"`
			Author struct {
				DisplayName string `json:"displayName"`
			} `json:"author"`
			Record struct {
				Text      string `json:"text"`
				CreatedAt string `json:"createdAt"`
			} `json:"record"`
			IndexedAt string `json:"indexedAt"`
		} `json:"posts"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return "", "", fmt.Errorf("decoding search response: %w", err)
	}

	for _, post := range searchResp.Posts {
		// Check if display name matches
		if post.Author.DisplayName == displayName ||
			strings.Contains(post.Author.DisplayName, displayName) ||
			strings.Contains(displayName, post.Author.DisplayName) {

			// Check if content is similar (might have been truncated)
			if strings.Contains(post.Record.Text, content) ||
				strings.Contains(content, post.Record.Text) {

				// Check if the post date is close (within 1 day)
				postCreatedAt, err := time.Parse(time.RFC3339, post.Record.CreatedAt)
				if err != nil {
					log.Printf("Error parsing post date: %v", err)
					continue
				}

				timeDiff := postCreatedAt.Sub(postDate)
				if timeDiff < 24*time.Hour && timeDiff > -24*time.Hour {
					log.Printf("Found post with matching content, display name, and timestamp: %s", post.Uri)
					return post.Uri, post.Cid, nil
				}
			}
		}
	}

	return "", "", fmt.Errorf("no matching post found by content and display name")
}

// Helper to resolve a handle to a DID
func (c *Client) resolveHandle(ctx context.Context, handle string) (string, error) {
	url := c.pds + "/xrpc/com.atproto.identity.resolveHandle"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("creating handle resolve request: %w", err)
	}

	q := req.URL.Query()
	q.Add("handle", handle)
	req.URL.RawQuery = q.Encode()

	req.Header.Set("Authorization", "Bearer "+c.accessJwt)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("performing handle resolve request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("handle resolution failed with status %d: %s", resp.StatusCode, body)
	}

	var resolveResp struct {
		Did string `json:"did"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&resolveResp); err != nil {
		return "", fmt.Errorf("decoding handle resolution response: %w", err)
	}

	return resolveResp.Did, nil
}

// Helper to find a specific Mastodon post in a user's Bluesky feed
func (c *Client) findPostInUserFeed(ctx context.Context, did string, mastodonPostID string) (string, string, error) {
	url := c.pds + "/xrpc/app.bsky.feed.getAuthorFeed"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", "", fmt.Errorf("creating author feed request: %w", err)
	}

	q := req.URL.Query()
	q.Add("actor", did)
	q.Add("limit", "100") // Get a decent number of posts to search through
	req.URL.RawQuery = q.Encode()

	req.Header.Set("Authorization", "Bearer "+c.accessJwt)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("performing author feed request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("author feed request failed with status %d: %s", resp.StatusCode, body)
	}

	var feedResp struct {
		Feed []struct {
			Post struct {
				Uri    string `json:"uri"`
				Cid    string `json:"cid"`
				Record struct {
					Text        string `json:"text"`
					ExternalUrl string `json:"external"`
				} `json:"record"`
			} `json:"post"`
		} `json:"feed"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&feedResp); err != nil {
		return "", "", fmt.Errorf("decoding author feed response: %w", err)
	}

	// Look for a post that references the original Mastodon post ID
	for _, item := range feedResp.Feed {
		if strings.Contains(item.Post.Record.ExternalUrl, mastodonPostID) ||
			strings.Contains(item.Post.Record.Text, mastodonPostID) {
			return item.Post.Uri, item.Post.Cid, nil
		}
	}

	return "", "", fmt.Errorf("no matching post found")
}

// Helper to search for posts containing a specific term
func (c *Client) searchForPost(ctx context.Context, searchTerm, mastodonPostID string) (string, string, error) {
	// Note: Bluesky's search API might change, so this is a tentative implementation
	url := c.pds + "/xrpc/app.bsky.feed.searchPosts"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", "", fmt.Errorf("creating search request: %w", err)
	}

	q := req.URL.Query()
	q.Add("q", searchTerm)
	q.Add("limit", "20")
	req.URL.RawQuery = q.Encode()

	req.Header.Set("Authorization", "Bearer "+c.accessJwt)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("performing search request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("search request failed with status %d: %s", resp.StatusCode, body)
	}

	var searchResp struct {
		Posts []struct {
			Uri    string `json:"uri"`
			Cid    string `json:"cid"`
			Record struct {
				Text        string `json:"text"`
				ExternalUrl string `json:"external"`
			} `json:"record"`
		} `json:"posts"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return "", "", fmt.Errorf("decoding search response: %w", err)
	}

	for _, post := range searchResp.Posts {
		if strings.Contains(post.Record.ExternalUrl, mastodonPostID) ||
			strings.Contains(post.Record.Text, mastodonPostID) {
			return post.Uri, post.Cid, nil
		}
	}

	return "", "", fmt.Errorf("no matching post found in search results")
}

func (c *Client) CreateRepost(ctx context.Context, uri string, cid string) (string, error) {
	if err := c.ensureAuth(ctx); err != nil {
		return "", fmt.Errorf("authentication failed: %w", err)
	}

	// Create repost record
	record := map[string]interface{}{
		"$type": "app.bsky.feed.repost",
		"subject": map[string]interface{}{
			"cid": cid,
			"uri": uri,
		},
		"createdAt": time.Now().Format(time.RFC3339),
	}

	req := map[string]interface{}{
		"repo":       c.did,
		"collection": "app.bsky.feed.repost",
		"record":     record,
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshaling repost request: %w", err)
	}

	url := c.pds + "/xrpc/com.atproto.repo.createRecord"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("creating repost request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.accessJwt)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("performing repost request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("repost creation failed with status %d: %s", resp.StatusCode, body)
	}

	var repostResp struct {
		Uri string `json:"uri"`
		Cid string `json:"cid"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&repostResp); err != nil {
		return "", fmt.Errorf("decoding repost response: %w", err)
	}

	return repostResp.Uri + "|" + repostResp.Cid, nil
}
