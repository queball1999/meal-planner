// Package homeassistant syncs a household's shopping list with a Home Assistant
// to-do entity (default todo.shopping_list). It talks to HA's REST API with a
// long-lived access token.
//
// Requires Home Assistant 2024.7+ for the todo.get_items service. The legacy
// shopping_list integration is not supported.
package homeassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client calls one Home Assistant instance. Its own http.Client is used
// deliberately: HA lives on a private LAN address that safefetch would reject.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient returns nil when baseURL or token is blank.
func NewClient(baseURL, token string) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	token = strings.TrimSpace(token)
	if baseURL == "" || token == "" {
		return nil
	}
	return &Client{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{Timeout: 12 * time.Second},
	}
}

// TodoItem is one entry in a HA to-do list.
type TodoItem struct {
	UID     string `json:"uid"`
	Summary string `json:"summary"`
	Status  string `json:"status"` // "needs_action" | "completed"
}

func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("home assistant unreachable: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("home assistant rejected the token (401)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("home assistant %s: %d %s", path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// Ping verifies the base URL and token by hitting GET /api/.
func (c *Client) Ping(ctx context.Context) error {
	data, err := c.do(ctx, http.MethodGet, "/api/", nil)
	if err != nil {
		return err
	}
	var msg struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &msg) == nil && strings.Contains(strings.ToLower(msg.Message), "api running") {
		return nil
	}
	return fmt.Errorf("unexpected response from home assistant: %s", strings.TrimSpace(string(data)))
}

// AddItem appends a new item to the to-do list.
func (c *Client) AddItem(ctx context.Context, entity, item string) error {
	_, err := c.do(ctx, http.MethodPost, "/api/services/todo/add_item", map[string]string{
		"entity_id": entity, "item": item,
	})
	return err
}

// UpdateItem changes an existing item's status ("completed"|"needs_action").
// ref is the item's uid when known, else its exact summary text.
func (c *Client) UpdateItem(ctx context.Context, entity, ref, status string) error {
	_, err := c.do(ctx, http.MethodPost, "/api/services/todo/update_item", map[string]string{
		"entity_id": entity, "item": ref, "status": status,
	})
	return err
}

// RemoveItem deletes an item by uid or exact summary.
func (c *Client) RemoveItem(ctx context.Context, entity, ref string) error {
	_, err := c.do(ctx, http.MethodPost, "/api/services/todo/remove_item", map[string]string{
		"entity_id": entity, "item": ref,
	})
	return err
}

// TodoEntity is one todo.* entity exposed by Home Assistant.
type TodoEntity struct {
	EntityID string `json:"entity_id"`
	Name     string `json:"name"`
}

// ListTodoEntities returns every todo.* entity via GET /api/states, for the
// setup dialog's "find lists" button.
func (c *Client) ListTodoEntities(ctx context.Context) ([]TodoEntity, error) {
	data, err := c.do(ctx, http.MethodGet, "/api/states", nil)
	if err != nil {
		return nil, err
	}
	var states []struct {
		EntityID   string `json:"entity_id"`
		Attributes struct {
			FriendlyName string `json:"friendly_name"`
		} `json:"attributes"`
	}
	if err := json.Unmarshal(data, &states); err != nil {
		return nil, fmt.Errorf("home assistant states: bad response: %w", err)
	}
	var out []TodoEntity
	for _, st := range states {
		if !strings.HasPrefix(st.EntityID, "todo.") {
			continue
		}
		name := st.Attributes.FriendlyName
		if name == "" {
			name = st.EntityID
		}
		out = append(out, TodoEntity{EntityID: st.EntityID, Name: name})
	}
	return out, nil
}

// GetItems returns every item on the to-do list via todo.get_items. HA's
// service defaults "status" to ["needs_action"] only, so without passing it
// explicitly every completed item is invisible to us - PullList could never
// see a completed status to reconcile, and PushList's mirrored-state check
// would spuriously think a still-completed item needed re-pushing (and worse,
// would think a completed item to un-check couldn't be found). Request both.
func (c *Client) GetItems(ctx context.Context, entity string) ([]TodoItem, error) {
	data, err := c.do(ctx, http.MethodPost,
		"/api/services/todo/get_items?return_response=true",
		map[string]any{"entity_id": entity, "status": []string{"needs_action", "completed"}})
	if err != nil {
		return nil, err
	}
	var wrap struct {
		ServiceResponse map[string]struct {
			Items []TodoItem `json:"items"`
		} `json:"service_response"`
	}
	if err := json.Unmarshal(data, &wrap); err != nil {
		return nil, fmt.Errorf("home assistant get_items: bad response: %w", err)
	}
	if r, ok := wrap.ServiceResponse[entity]; ok {
		return r.Items, nil
	}
	// Some HA versions key the response differently; take the first list.
	for _, r := range wrap.ServiceResponse {
		return r.Items, nil
	}
	return nil, nil
}
