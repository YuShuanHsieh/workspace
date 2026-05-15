package pcs

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

// Decision is the resolved PCS outcome.
type Decision int

const (
	DecisionUnknown Decision = iota
	DecisionAllow
	DecisionDeny
)

// CheckRequest is the input to Check; fields map 1:1 onto phase-1-request-contract.md §3.
type CheckRequest struct {
	ObjectID   string
	ObjectType string
	Permission string
	SSOToken   string
	RequestID  string // optional
}

// Client calls the Permission Checking Service.
type Client struct {
	endpoint string
	http     *http.Client
}

// NewClient returns a Client targeting endpoint+"/permission-check/v1/check".
// timeout bounds the per-call HTTP timeout; callers may also cancel via ctx.
func NewClient(endpoint string, timeout time.Duration) *Client {
	return &Client{
		endpoint: strings.TrimRight(endpoint, "/"),
		http:     &http.Client{Timeout: timeout},
	}
}

type checkBody struct {
	ObjectID   string `json:"objectId"`
	ObjectType string `json:"objectType"`
	Permission string `json:"permission"`
}

type checkResp struct {
	Allowed bool `json:"allowed"`
}

// Check performs POST /permission-check/v1/check.
// Returns (DecisionAllow|DecisionDeny, nil) on a 2xx with a parsable body.
// Returns (DecisionUnknown, err) on transport error, timeout, non-2xx, or JSON failure.
// Callers treat any error as fail-closed (return 403) per PV1-009.
func (c *Client) Check(ctx context.Context, req CheckRequest) (Decision, error) {
	payload, err := json.Marshal(checkBody{ObjectID: req.ObjectID, ObjectType: req.ObjectType, Permission: req.Permission})
	if err != nil {
		return DecisionUnknown, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/permission-check/v1/check", bytes.NewReader(payload))
	if err != nil {
		return DecisionUnknown, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+req.SSOToken)
	if req.RequestID != "" {
		httpReq.Header.Set("X-Request-Id", req.RequestID)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return DecisionUnknown, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return DecisionUnknown, err
	}
	if resp.StatusCode/100 != 2 {
		return DecisionUnknown, fmt.Errorf("pcs: status %d body=%q", resp.StatusCode, truncate(body, 256))
	}
	var cr checkResp
	if err := json.Unmarshal(body, &cr); err != nil {
		return DecisionUnknown, fmt.Errorf("pcs: decode response: %w", err)
	}
	if cr.Allowed {
		return DecisionAllow, nil
	}
	return DecisionDeny, nil
}

func truncate(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[:n]
}
