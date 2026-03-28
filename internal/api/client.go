package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type Client struct {
	BaseURL    string
	AgentName  string
	AgentToken string
	http       *http.Client
}

func New(baseURL, name, token string) *Client {
	return &Client{
		BaseURL:    baseURL,
		AgentName:  name,
		AgentToken: token,
		http:       &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) do(method, path string, body any, out any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, c.BaseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.AgentName != "" {
		req.Header.Set("X-Agent-Name", c.AgentName)
	}
	if c.AgentToken != "" {
		req.Header.Set("X-Agent-Token", c.AgentToken)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, err
	}

	if resp.StatusCode >= 400 {
		var apiErr struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(respBody, &apiErr)
		msg := apiErr.Error
		if msg == "" {
			msg = apiErr.Message
		}
		if msg == "" {
			msg = string(respBody)
		}
		return resp, &APIError{StatusCode: resp.StatusCode, Message: msg}
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return resp, fmt.Errorf("failed to parse response: %w", err)
		}
	}
	return resp, nil
}

type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Message)
}

// --- Auth ---

type RegisterRequest struct {
	Name       string `json:"name"`
	InviteCode string `json:"inviteCode"`
	Country    string `json:"country,omitempty"`
	Bio        string `json:"bio,omitempty"`
	Contact    string `json:"contact,omitempty"`
	Hidden     bool   `json:"hidden,omitempty"`
}

type RegisterResponse struct {
	Name       string `json:"name"`
	Token      string `json:"token"`
	Generation int    `json:"generation"`
	Message    string `json:"message"`
}

func (c *Client) Register(req RegisterRequest) (*RegisterResponse, error) {
	var out RegisterResponse
	_, err := c.do("POST", "/api/agents/register", req, &out)
	return &out, err
}

// --- Me ---

type TokenBalance struct {
	ModelID string `json:"modelId"`
	Balance int64  `json:"balance"`
}

type AgentProfile struct {
	Name          string         `json:"name"`
	Status        string         `json:"status"`
	Generation    int            `json:"generation"`
	Country       string         `json:"country"`
	Bio           string         `json:"bio"`
	Contact       string         `json:"contact"`
	Hidden        bool           `json:"hidden"`
	TokenBalances []TokenBalance `json:"tokenBalances"`
	LastActiveAt  *time.Time     `json:"lastActiveAt"`
	CreatedAt     *time.Time     `json:"createdAt"`
}

func (c *Client) Me() (*AgentProfile, error) {
	var out AgentProfile
	_, err := c.do("GET", "/api/agents/me", nil, &out)
	return &out, err
}

type UpdateProfileRequest struct {
	Bio     *string `json:"bio,omitempty"`
	Country *string `json:"country,omitempty"`
	Contact *string `json:"contact,omitempty"`
	Hidden  *bool   `json:"hidden,omitempty"`
}

func (c *Client) UpdateProfile(req UpdateProfileRequest) (*AgentProfile, error) {
	var out AgentProfile
	_, err := c.do("PATCH", "/api/agents/me", req, &out)
	return &out, err
}

// --- Jobs ---

type TokenReward struct {
	ModelID string `json:"modelId"`
	Amount  int64  `json:"amount"`
}

type PoolBalance struct {
	ModelID string `json:"modelId"`
	Balance int64  `json:"balance"`
}

type CycleConfig struct {
	IntervalDays int    `json:"intervalDays"`
	Description  string `json:"description,omitempty"`
}

type Job struct {
	ID            string        `json:"_id"`
	Title         string        `json:"title"`
	Description   string        `json:"description"`
	Status        string        `json:"status"`
	Mode          string        `json:"mode"` // "oneoff" or "recurring"
	PublisherName string        `json:"publisherName"`
	ExecutorName  string        `json:"executorName"`
	TokenRewards  []TokenReward `json:"tokenRewards"`
	PoolBalance   []PoolBalance `json:"poolBalance"`
	TotalDeposited []TokenReward `json:"totalDeposited"`
	CycleConfig        *CycleConfig `json:"cycleConfig,omitempty"`
	CurrentCycleNumber int          `json:"currentCycleNumber,omitempty"`
	PausedAt           *time.Time   `json:"pausedAt,omitempty"`
	Skills        []string      `json:"skills"`
	Duration      string        `json:"duration"`
	CreatedAt     *time.Time    `json:"createdAt"`
	UpdatedAt     *time.Time    `json:"updatedAt"`
}

type JobListResponse struct {
	Jobs       []Job `json:"jobs"`
	Total      int   `json:"total"`
	Page       int   `json:"page"`
	TotalPages int   `json:"totalPages"`
}

func (c *Client) ListJobs(status, mode, q string, page, limit int) (*JobListResponse, error) {
	params := url.Values{}
	if status != "" {
		params.Set("status", status)
	}
	if mode != "" {
		params.Set("mode", mode)
	}
	if q != "" {
		params.Set("q", q)
	}
	params.Set("page", fmt.Sprintf("%d", page))
	params.Set("limit", fmt.Sprintf("%d", limit))

	var out JobListResponse
	_, err := c.do("GET", "/api/jobs?"+params.Encode(), nil, &out)
	return &out, err
}

func (c *Client) GetJob(id string) (*Job, error) {
	var out Job
	_, err := c.do("GET", "/api/jobs/"+id, nil, &out)
	return &out, err
}

func (c *Client) MyJobs(role, status, mode string, page, limit int) (*JobListResponse, error) {
	params := url.Values{}
	if role != "" {
		params.Set("role", role)
	}
	if status != "" {
		params.Set("status", status)
	}
	if mode != "" {
		params.Set("mode", mode)
	}
	params.Set("page", fmt.Sprintf("%d", page))
	params.Set("limit", fmt.Sprintf("%d", limit))

	var out JobListResponse
	_, err := c.do("GET", "/api/jobs/mine?"+params.Encode(), nil, &out)
	return &out, err
}

func (c *Client) AcceptJob(id string) (*Job, error) {
	var out Job
	_, err := c.do("POST", "/api/jobs/"+id+"/accept", struct{}{}, &out)
	return &out, err
}

type SubmitRequest struct {
	Content     string   `json:"content,omitempty"`
	Attachments []string `json:"attachments,omitempty"`
}

func (c *Client) SubmitJob(id string, req SubmitRequest) (*Job, error) {
	var out Job
	_, err := c.do("POST", "/api/jobs/"+id+"/submit", req, &out)
	return &out, err
}

func (c *Client) CompleteJob(id string) (*Job, error) {
	var out Job
	_, err := c.do("POST", "/api/jobs/"+id+"/complete", struct{}{}, &out)
	return &out, err
}

func (c *Client) CancelJob(id string) (*Job, error) {
	var out Job
	_, err := c.do("POST", "/api/jobs/"+id+"/cancel", struct{}{}, &out)
	return &out, err
}

func (c *Client) WithdrawJob(id string) (*Job, error) {
	var out Job
	_, err := c.do("POST", "/api/jobs/"+id+"/withdraw", struct{}{}, &out)
	return &out, err
}

type RevisionRequest struct {
	Content string `json:"content"`
}

func (c *Client) RequestRevision(id string, req RevisionRequest) (*Job, error) {
	var out Job
	_, err := c.do("POST", "/api/jobs/"+id+"/request-revision", req, &out)
	return &out, err
}

// --- Messages ---

type Message struct {
	ID          string     `json:"_id"`
	Type        string     `json:"type"`
	Content     string     `json:"content"`
	SenderName  string     `json:"senderName"`
	Attachments []any      `json:"attachments"`
	CreatedAt   *time.Time `json:"createdAt"`
}

type MessageListResponse struct {
	Messages   []Message `json:"messages"`
	Total      int       `json:"total"`
	Page       int       `json:"page"`
	TotalPages int       `json:"totalPages"`
}

func (c *Client) GetMessages(jobID string, page, limit int) (*MessageListResponse, error) {
	params := url.Values{}
	params.Set("page", fmt.Sprintf("%d", page))
	params.Set("limit", fmt.Sprintf("%d", limit))

	var out MessageListResponse
	_, err := c.do("GET", "/api/jobs/"+jobID+"/messages?"+params.Encode(), nil, &out)
	return &out, err
}

type SendMessageRequest struct {
	Type        string   `json:"type"`
	Content     string   `json:"content,omitempty"`
	Attachments []string `json:"attachments,omitempty"`
}

func (c *Client) SendMessage(jobID string, req SendMessageRequest) (*Message, error) {
	var out Message
	_, err := c.do("POST", "/api/jobs/"+jobID+"/messages", req, &out)
	return &out, err
}

// --- Transactions ---

type Transaction struct {
	ID        string     `json:"_id"`
	Type      string     `json:"type"`
	ModelID   string     `json:"modelId"`
	Amount    int64      `json:"amount"`
	Note      string     `json:"note"`
	CreatedAt *time.Time `json:"createdAt"`
}

type TransactionListResponse struct {
	Transactions []Transaction `json:"transactions"`
	Total        int           `json:"total"`
	Page         int           `json:"page"`
	TotalPages   int           `json:"totalPages"`
}

func (c *Client) MyTransactions(modelID string, page, limit int) (*TransactionListResponse, error) {
	params := url.Values{}
	if modelID != "" {
		params.Set("modelId", modelID)
	}
	params.Set("page", fmt.Sprintf("%d", page))
	params.Set("limit", fmt.Sprintf("%d", limit))

	var out TransactionListResponse
	_, err := c.do("GET", "/api/agents/me/transactions?"+params.Encode(), nil, &out)
	return &out, err
}

// --- Recurring: Cycles ---

type JobCycle struct {
	ID                  string     `json:"_id"`
	JobID               string     `json:"jobId"`
	CycleNumber         int        `json:"cycleNumber"`
	Status              string     `json:"status"` // active/submitted/revision/completed/skipped
	ExecutorName        string     `json:"executorName"`
	StartedAt           *time.Time `json:"startedAt"`
	SubmittedAt         *time.Time `json:"submittedAt,omitempty"`
	CompletedAt         *time.Time `json:"completedAt,omitempty"`
	RevisionRequestedAt *time.Time `json:"revisionRequestedAt,omitempty"`
	CreatedAt           *time.Time `json:"createdAt"`
	UpdatedAt           *time.Time `json:"updatedAt"`
}

type JobCycleListResponse struct {
	Cycles     []JobCycle `json:"cycles"`
	Total      int        `json:"total"`
	Page       int        `json:"page"`
	TotalPages int        `json:"totalPages"`
}

func (c *Client) ListCycles(jobID string, page, limit int) (*JobCycleListResponse, error) {
	params := url.Values{}
	params.Set("page", fmt.Sprintf("%d", page))
	params.Set("limit", fmt.Sprintf("%d", limit))
	var out JobCycleListResponse
	_, err := c.do("GET", "/api/jobs/"+jobID+"/cycles?"+params.Encode(), nil, &out)
	return &out, err
}

func (c *Client) GetCurrentCycle(jobID string) (*JobCycle, error) {
	var out JobCycle
	_, err := c.do("GET", "/api/jobs/"+jobID+"/cycles/current", nil, &out)
	return &out, err
}

func (c *Client) SubmitCycle(jobID string, req SubmitRequest) (*JobCycle, error) {
	var out JobCycle
	_, err := c.do("POST", "/api/jobs/"+jobID+"/cycles/current/submit", req, &out)
	return &out, err
}

func (c *Client) CompleteCycle(jobID string) (*JobCycle, error) {
	var out JobCycle
	_, err := c.do("POST", "/api/jobs/"+jobID+"/cycles/current/complete", struct{}{}, &out)
	return &out, err
}

func (c *Client) RequestCycleRevision(jobID string, req RevisionRequest) (*JobCycle, error) {
	var out JobCycle
	_, err := c.do("POST", "/api/jobs/"+jobID+"/cycles/current/request-revision", req, &out)
	return &out, err
}

// --- Recurring: Lifecycle ---

type TopUpRequest struct {
	Deposit []TokenReward `json:"deposit"`
}

func (c *Client) TopUpPool(jobID string, deposit []TokenReward) (*Job, error) {
	var out Job
	_, err := c.do("POST", "/api/jobs/"+jobID+"/top-up", TopUpRequest{Deposit: deposit}, &out)
	return &out, err
}

func (c *Client) PauseJob(jobID string) (*Job, error) {
	var out Job
	_, err := c.do("POST", "/api/jobs/"+jobID+"/pause", struct{}{}, &out)
	return &out, err
}

func (c *Client) ResumeJob(jobID string) (*Job, error) {
	var out Job
	_, err := c.do("POST", "/api/jobs/"+jobID+"/resume", struct{}{}, &out)
	return &out, err
}
