package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
	Requirements  string        `json:"requirements,omitempty"`
	Input         string        `json:"input,omitempty"`
	Output        string        `json:"output,omitempty"`
	Status        string        `json:"status"`
	Mode          string        `json:"mode"` // "oneoff" or "recurring"
	PublisherName string        `json:"publisherName"`
	ExecutorName  string        `json:"executorName"`
	TokenRewards  []TokenReward `json:"tokenRewards"`
	PoolBalance   []PoolBalance `json:"poolBalance"`
	TotalDeposited []TokenReward `json:"totalDeposited"`
	BidCount           int          `json:"bidCount"`
	CycleConfig        *CycleConfig `json:"cycleConfig,omitempty"`
	CurrentCycleNumber int          `json:"currentCycleNumber,omitempty"`
	PausedAt           *time.Time   `json:"pausedAt,omitempty"`
	Skills        []string      `json:"skills"`
	Duration      string        `json:"duration"`
	SubmittedAt         *time.Time `json:"submittedAt,omitempty"`
	AcceptedAt          *time.Time `json:"acceptedAt,omitempty"`
	CompletedAt         *time.Time `json:"completedAt,omitempty"`
	CancelledAt         *time.Time `json:"cancelledAt,omitempty"`
	RevisionRequestedAt *time.Time `json:"revisionRequestedAt,omitempty"`
	CreatedAt     *time.Time    `json:"createdAt"`
	UpdatedAt     *time.Time    `json:"updatedAt"`
}

type JobListResponse struct {
	Jobs       []Job `json:"jobs"`
	Total      int   `json:"total"`
	Page       int   `json:"page"`
	TotalPages int   `json:"totalPages"`
}

type CreateJobRequest struct {
	Title        string        `json:"title"`
	Description  string        `json:"description"`
	Mode         string        `json:"mode,omitempty"`
	TokenRewards []TokenReward `json:"tokenRewards"`
	Requirements string        `json:"requirements,omitempty"`
	Input        string        `json:"input,omitempty"`
	Output       string        `json:"output,omitempty"`
	Duration     string        `json:"duration,omitempty"`
	Skills       []string      `json:"skills,omitempty"`
	CycleConfig  *CycleConfig  `json:"cycleConfig,omitempty"`
	PoolDeposit  []TokenReward `json:"poolDeposit,omitempty"`
}

func (c *Client) CreateJob(req CreateJobRequest) (*Job, error) {
	var out Job
	_, err := c.do("POST", "/api/jobs", req, &out)
	return &out, err
}

func (c *Client) ListJobs(status, mode, q, skill string, page, limit int) (*JobListResponse, error) {
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
	if skill != "" {
		params.Set("skill", skill)
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

// --- Bids ---

type Bid struct {
	ID         string     `json:"_id"`
	JobID      string     `json:"jobId"`
	BidderID   string     `json:"bidderId"`
	BidderName string     `json:"bidderName"`
	Message    string     `json:"message,omitempty"`
	Status     string     `json:"status"` // pending/selected/rejected/withdrawn
	CreatedAt  *time.Time `json:"createdAt"`
	UpdatedAt  *time.Time `json:"updatedAt"`
}

type BidListResponse struct {
	Bids       []Bid `json:"bids"`
	Total      int   `json:"total"`
	Page       int   `json:"page"`
	TotalPages int   `json:"totalPages"`
}

type PlaceBidRequest struct {
	Message string `json:"message"`
}

func (c *Client) PlaceBid(jobID, message string) (*Bid, error) {
	var out Bid
	_, err := c.do("POST", "/api/jobs/"+jobID+"/bids", PlaceBidRequest{Message: message}, &out)
	return &out, err
}

func (c *Client) ListBids(jobID, status string, page, limit int) (*BidListResponse, error) {
	params := url.Values{}
	if status != "" {
		params.Set("status", status)
	}
	params.Set("page", fmt.Sprintf("%d", page))
	params.Set("limit", fmt.Sprintf("%d", limit))
	var out BidListResponse
	_, err := c.do("GET", "/api/jobs/"+jobID+"/bids?"+params.Encode(), nil, &out)
	return &out, err
}

func (c *Client) SelectBid(jobID, bidID string) (*Job, error) {
	var out Job
	_, err := c.do("POST", "/api/jobs/"+jobID+"/bids/"+bidID+"/select", struct{}{}, &out)
	return &out, err
}

func (c *Client) RejectBid(jobID, bidID string) error {
	_, err := c.do("POST", "/api/jobs/"+jobID+"/bids/"+bidID+"/reject", struct{}{}, nil)
	return err
}

func (c *Client) WithdrawBid(jobID, bidID string) error {
	_, err := c.do("POST", "/api/jobs/"+jobID+"/bids/"+bidID+"/withdraw", struct{}{}, nil)
	return err
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

// MessageAttachment is what the platform populates onto a message's
// attachments array (see lib/models/job-message.ts + populate selector in
// /api/jobs/[id]/messages). The platform may also return raw ObjectId
// strings on routes that don't populate; consumers should be tolerant.
type MessageAttachment struct {
	ID           string     `json:"_id"`
	OriginalName string     `json:"originalName"`
	MimeType     string     `json:"mimeType,omitempty"`
	Size         int64      `json:"size,omitempty"`
	CreatedAt    *time.Time `json:"createdAt,omitempty"`
}

type Message struct {
	ID          string              `json:"_id"`
	Type        string              `json:"type"`
	Content     string              `json:"content"`
	SenderName  string              `json:"senderName"`
	Attachments []MessageAttachment `json:"attachments"`
	CycleNumber int                 `json:"cycleNumber,omitempty"`
	CreatedAt   *time.Time          `json:"createdAt"`
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
//
// Platform Transaction (lib/models/transaction.ts):
//   • amount is signed: positive = credit, negative = debit
//   • types: pool_deposit (debit, negative), settlement (credit), pool_refund
//     (credit), grant (admin top-up). Legacy "escrow" / "refund" still appear
//     in older records.
//   • description holds the human-readable note (NOT "note" — that was the
//     pre-pool name and remains a CLI bug to be wary of in the future).

type Transaction struct {
	ID             string     `json:"_id"`
	Type           string     `json:"type"`
	UserID         string     `json:"userId,omitempty"`
	CounterpartyID string     `json:"counterpartyId,omitempty"`
	ModelID        string     `json:"modelId"`
	Amount         int64      `json:"amount"`
	Balance        int64      `json:"balance,omitempty"`
	JobID          string     `json:"jobId,omitempty"`
	Description    string     `json:"description"`
	CreatedAt      *time.Time `json:"createdAt"`
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

// CycleResponse is the wrapped envelope returned by the three cycle-mutating
// endpoints (submit / complete / request-revision). The platform always
// returns { job, cycle } so callers can update both views in a single round
// trip.
type CycleResponse struct {
	Job   *Job      `json:"job"`
	Cycle *JobCycle `json:"cycle"`
}

func (c *Client) SubmitCycle(jobID string, req SubmitRequest) (*CycleResponse, error) {
	var out CycleResponse
	_, err := c.do("POST", "/api/jobs/"+jobID+"/cycles/current/submit", req, &out)
	return &out, err
}

func (c *Client) CompleteCycle(jobID string) (*CycleResponse, error) {
	var out CycleResponse
	_, err := c.do("POST", "/api/jobs/"+jobID+"/cycles/current/complete", struct{}{}, &out)
	return &out, err
}

func (c *Client) RequestCycleRevision(jobID string, req RevisionRequest) (*CycleResponse, error) {
	var out CycleResponse
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

// --- Files (three-step upload) ---
//
// The platform uses a pre-signed URL flow for file uploads:
//
//  1. POST /api/files/presign-upload     → returns {fileId, uploadUrl, expiresIn}
//  2. PUT <uploadUrl>                    → upload raw file bytes to R2
//  3. POST /api/files/{fileId}/confirm   → finalize; returns the File record
//
// Consumers attach the resulting fileId to jobs/messages via their
// `attachments` field. Platform contract (see agentsworkhub/app/api/files/...):
//   • presign request uses `mimeType` (not `contentType`) and requires `size`.
//   • File record uses `originalName` + `mimeType` in JSON.

type File struct {
	ID           string     `json:"_id"`
	OriginalName string     `json:"originalName"`
	MimeType     string     `json:"mimeType,omitempty"`
	Size         int64      `json:"size,omitempty"`
	Confirmed    bool       `json:"confirmed,omitempty"`
	CreatedAt    *time.Time `json:"createdAt,omitempty"`
}

type PresignUploadRequest struct {
	Filename string `json:"filename"`
	MimeType string `json:"mimeType"`
	Size     int64  `json:"size"`
}

type PresignUploadResponse struct {
	FileID    string `json:"fileId"`
	UploadURL string `json:"uploadUrl"`
	ExpiresIn int    `json:"expiresIn,omitempty"`
}

// PresignUpload requests a pre-signed PUT URL for uploading a file. All three
// fields are required by the platform; size must be a positive byte count and
// is capped at 500 MB server-side.
func (c *Client) PresignUpload(filename, mimeType string, size int64) (*PresignUploadResponse, error) {
	var out PresignUploadResponse
	_, err := c.do("POST", "/api/files/presign-upload", PresignUploadRequest{
		Filename: filename,
		MimeType: mimeType,
		Size:     size,
	}, &out)
	return &out, err
}

// UploadToPresignedURL PUTs the local file at filePath to uploadURL with the
// given contentType. It is a package-level function (not bound to a Client)
// because the pre-signed URL already embeds the auth credentials — no
// X-Agent-* headers should be attached.
func UploadToPresignedURL(uploadURL, filePath, contentType string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat file: %w", err)
	}

	req, err := http.NewRequest(http.MethodPut, uploadURL, f)
	if err != nil {
		return fmt.Errorf("build upload request: %w", err)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = stat.Size()

	httpClient := &http.Client{Timeout: 10 * time.Minute}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// ConfirmUpload finalizes a presigned upload. The platform verifies the object
// has been stored and returns the canonical File record.
func (c *Client) ConfirmUpload(fileID string) (*File, error) {
	var out File
	_, err := c.do("POST", "/api/files/"+fileID+"/confirm", struct{}{}, &out)
	return &out, err
}

// DownloadFile streams the file behind fileID into out and returns the
// canonical filename reported by the server (Content-Disposition or fallback).
//
// The platform's GET /api/files/{id} responds with a 302 redirect to a
// presigned R2 URL. We must NOT forward the X-Agent-* headers to R2 (they
// would either be ignored or, worse, cause the presigned URL signature to
// fail), so the redirect is followed manually with a fresh, header-less
// request.
func (c *Client) DownloadFile(fileID string, out io.Writer) (string, error) {
	// Step 1: hit the API with auth, but DO NOT follow the redirect.
	apiClient := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequest(http.MethodGet, c.BaseURL+"/api/files/"+fileID, nil)
	if err != nil {
		return "", err
	}
	if c.AgentName != "" {
		req.Header.Set("X-Agent-Name", c.AgentName)
	}
	if c.AgentToken != "" {
		req.Header.Set("X-Agent-Token", c.AgentToken)
	}
	resp, err := apiClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request file metadata: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		var apiErr struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &apiErr)
		msg := apiErr.Error
		if msg == "" {
			msg = apiErr.Message
		}
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		return "", &APIError{StatusCode: resp.StatusCode, Message: msg}
	}
	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusMovedPermanently &&
		resp.StatusCode != http.StatusTemporaryRedirect && resp.StatusCode != http.StatusPermanentRedirect {
		return "", fmt.Errorf("unexpected status %d (expected 302 redirect)", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("server returned %d but no Location header", resp.StatusCode)
	}

	// Step 2: download from the presigned URL with no extra headers.
	dlClient := &http.Client{Timeout: 10 * time.Minute}
	dlResp, err := dlClient.Get(loc)
	if err != nil {
		return "", fmt.Errorf("download presigned url: %w", err)
	}
	defer dlResp.Body.Close()
	if dlResp.StatusCode >= 400 {
		body, _ := io.ReadAll(dlResp.Body)
		return "", fmt.Errorf("download failed: HTTP %d: %s", dlResp.StatusCode, strings.TrimSpace(string(body)))
	}
	if _, err := io.Copy(out, dlResp.Body); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}

	filename := filenameFromContentDisposition(dlResp.Header.Get("Content-Disposition"))
	return filename, nil
}

// filenameFromContentDisposition extracts the filename from a
// Content-Disposition header value. Returns "" if none parsed.
func filenameFromContentDisposition(cd string) string {
	if cd == "" {
		return ""
	}
	if _, params, err := mime.ParseMediaType(cd); err == nil {
		if v, ok := params["filename"]; ok && v != "" {
			return v
		}
		if v, ok := params["filename*"]; ok && v != "" {
			return v
		}
	}
	return ""
}

// DetectContentType returns a best-effort MIME type for a local file path.
// Falls back to "application/octet-stream".
func DetectContentType(filePath string) string {
	if ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(filePath))); ct != "" {
		return ct
	}
	return "application/octet-stream"
}
