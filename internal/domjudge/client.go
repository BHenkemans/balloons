package domjudge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL   string
	user      string
	pass      string
	contestID string
	hc        *http.Client
}

func New(baseURL, user, pass, contestID string) *Client {
	return &Client{
		baseURL:   baseURL,
		user:      user,
		pass:      pass,
		contestID: contestID,
		hc:        &http.Client{Timeout: 10 * time.Second},
	}
}

type Balloon struct {
	BalloonID      int64          `json:"balloonid"`
	ContestProblem ContestProblem `json:"contestproblem"`
	Team           string         `json:"team"`
	TeamID         string         `json:"teamid"`
	Done           bool           `json:"done"`
}

type ContestProblem struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	RGB   string `json:"rgb"`
}

type Award struct {
	ID      string   `json:"id"`
	TeamIDs []string `json:"team_ids"`
}

func (c *Client) ListBalloons(ctx context.Context) ([]Balloon, error) {
	var out []Balloon
	if err := c.get(ctx, fmt.Sprintf("/api/v4/contests/%s/balloons", url.PathEscape(c.contestID)), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ListAwards(ctx context.Context) ([]Award, error) {
	var out []Award
	if err := c.get(ctx, fmt.Sprintf("/api/v4/contests/%s/awards", url.PathEscape(c.contestID)), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) MarkDone(ctx context.Context, balloonID int64) error {
	path := fmt.Sprintf("/api/v4/contests/%s/balloons/%d/done", url.PathEscape(c.contestID), balloonID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.user, c.pass)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("domjudge POST %s: %s: %s", path, resp.Status, string(body))
	}
	return nil
}

// StreamEvents subscribes to the DOMjudge event-feed. Long-lived: returns only
// when ctx is canceled or the stream errors. fn is called for every JSON line.
func (c *Client) StreamEvents(ctx context.Context, types []string, fn func(line []byte) error) error {
	q := url.Values{}
	q.Set("stream", "true")
	if len(types) > 0 {
		q.Set("types", strings.Join(types, ","))
	}
	path := fmt.Sprintf("/api/v4/contests/%s/event-feed?%s", url.PathEscape(c.contestID), q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.user, c.pass)

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("domjudge GET %s: %s: %s", path, resp.Status, string(body))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := fn(line); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.user, c.pass)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("domjudge GET %s: %s: %s", path, resp.Status, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
