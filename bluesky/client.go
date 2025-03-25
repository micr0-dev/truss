package bluesky

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
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
		body, _ := ioutil.ReadAll(resp.Body)
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
		body, _ := ioutil.ReadAll(resp.Body)
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
		body, _ := ioutil.ReadAll(resp.Body)
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
		body, _ := ioutil.ReadAll(resp.Body)
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
