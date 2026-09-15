package dnsprovider

import (
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

// cloudflareAPI is the base URL of the Cloudflare v4 API, overridable in tests.
var cloudflareAPI = "https://api.cloudflare.com/client/v4"

func init() {
	Register(Descriptor{
		Name:  "cloudflare",
		Label: "Cloudflare",
		Description: "Uses a scoped API token. Create one with Zone:DNS:Edit " +
			"permission on the zones you want certificates for.",
		Fields: []Field{{
			Key:      "apiToken",
			Label:    "API token",
			Required: true,
			Secret:   true,
			Help:     "A scoped token, not the global API key.",
		}},
	}, newCloudflare)
}

type cloudflareCredentials struct {
	APIToken string `json:"apiToken"`
}

type cloudflare struct {
	token  string
	client *http.Client
}

func newCloudflare(raw json.RawMessage) (Provider, error) {
	var creds cloudflareCredentials
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, fmt.Errorf("cloudflare credentials: %w", err)
	}
	if strings.TrimSpace(creds.APIToken) == "" {
		return nil, fmt.Errorf("cloudflare: an API token is required")
	}
	return &cloudflare{
		token:  strings.TrimSpace(creds.APIToken),
		client: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (c *cloudflare) Present(ctx context.Context, name, value string) error {
	zoneID, err := c.zoneFor(ctx, name)
	if err != nil {
		return err
	}
	body := map[string]any{
		"type":    "TXT",
		"name":    name,
		"content": value,
		// The shortest TTL Cloudflare accepts, so a failed validation can
		// be retried without waiting out a long cache.
		"ttl": 60,
	}
	_, err = c.do(ctx, http.MethodPost, "/zones/"+zoneID+"/dns_records", body)
	return err
}

func (c *cloudflare) CleanUp(ctx context.Context, name, value string) error {
	zoneID, err := c.zoneFor(ctx, name)
	if err != nil {
		return err
	}

	q := url.Values{"type": {"TXT"}, "name": {name}, "content": {value}}
	raw, err := c.do(ctx, http.MethodGet, "/zones/"+zoneID+"/dns_records?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	var listed struct {
		Result []struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return fmt.Errorf("cloudflare: decode record list: %w", err)
	}

	for _, rec := range listed.Result {
		if _, err := c.do(ctx, http.MethodDelete,
			"/zones/"+zoneID+"/dns_records/"+rec.ID, nil); err != nil {
			return err
		}
	}
	return nil
}

// zoneFor finds the zone that owns a record name by walking up the labels,
// so "_acme-challenge.a.b.example.com" resolves to the "example.com" zone
// without the operator having to say which zone that is.
func (c *cloudflare) zoneFor(ctx context.Context, recordName string) (string, error) {
	labels := strings.Split(strings.Trim(recordName, "."), ".")

	for i := 0; i+1 < len(labels); i++ {
		candidate := strings.Join(labels[i:], ".")
		raw, err := c.do(ctx, http.MethodGet,
			"/zones?"+url.Values{"name": {candidate}}.Encode(), nil)
		if err != nil {
			return "", err
		}
		var listed struct {
			Result []struct {
				ID string `json:"id"`
			} `json:"result"`
		}
		if err := json.Unmarshal(raw, &listed); err != nil {
			return "", fmt.Errorf("cloudflare: decode zone list: %w", err)
		}
		if len(listed.Result) > 0 {
			return listed.Result[0].ID, nil
		}
	}
	return "", fmt.Errorf("cloudflare: no zone in this account covers %q", recordName)
}

// do performs one API call and unwraps Cloudflare's envelope, which reports
// failures inside a 200 response as often as through the status code.
func (c *cloudflare) do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, cloudflareAPI+path, payload)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloudflare: %w", err)
	}
	defer resp.Body.Close()

	// Responses are small; the cap only guards against a misbehaving peer.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("cloudflare: read response: %w", err)
	}

	var envelope struct {
		Success bool            `json:"success"`
		Errors  []cloudflareErr `json:"errors"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("cloudflare: HTTP %d with an unreadable body", resp.StatusCode)
	}
	if !envelope.Success {
		return nil, fmt.Errorf("cloudflare: %s", formatErrors(envelope.Errors, resp.StatusCode))
	}
	return raw, nil
}

type cloudflareErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func formatErrors(errs []cloudflareErr, status int) string {
	if len(errs) == 0 {
		return fmt.Sprintf("request failed with HTTP %d", status)
	}
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		parts = append(parts, fmt.Sprintf("%s (code %d)", e.Message, e.Code))
	}
	return strings.Join(parts, "; ")
}
