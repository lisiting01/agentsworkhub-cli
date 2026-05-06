package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lisiting01/agentsworkhub-cli/internal/api"
	"github.com/lisiting01/agentsworkhub-cli/internal/config"
	"github.com/lisiting01/agentsworkhub-cli/internal/output"
	"github.com/spf13/cobra"
)

// resolveContent expands shorthand notations for `-c` / `--content` style
// flags so long messages don't have to live on the command line:
//
//   - "-"            → read all of stdin
//   - "@/path/to.md" → read the file's contents
//   - anything else  → returned verbatim
//
// "@" prefix matches what gh / curl use; the bare "-" mirrors most Unix tools.
func resolveContent(raw string) (string, error) {
	if raw == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return strings.TrimRight(string(data), "\r\n"), nil
	}
	if strings.HasPrefix(raw, "@") {
		path := strings.TrimPrefix(raw, "@")
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read content file %s: %w", path, err)
		}
		return strings.TrimRight(string(data), "\r\n"), nil
	}
	return raw, nil
}

// hexID24 matches a 24-char hex string — the shape of a Mongo ObjectID and
// thus of a platform fileId. Used to decide whether an --attachment value is
// already an ID or should be treated as a local file path.
var hexID24 = regexp.MustCompile(`^[0-9a-fA-F]{24}$`)

// localPathHints matches content strings that look like local file paths
// (Windows drive letters, *nix absolute home/Users/home/var, or UNC).
// Triggers a warning when found inside a -c / --content body.
var localPathHints = regexp.MustCompile(`(?m)(?:^|[\s"'(\[])(?:[A-Za-z]:[\\/]|/(?:home|Users|var|tmp|etc)/|\\\\[^\\/]+\\)`)

// resolveAttachments walks the raw values passed via --attachment. For each
// entry that looks like a local file that exists on disk, it runs the
// three-step presign → PUT → confirm flow and returns the resulting fileId.
// Entries that already look like a fileId (24-hex) or a non-existent path
// (user clearly meant an ID) are passed through untouched.
func resolveAttachments(client *api.Client, raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	resolved := make([]string, 0, len(raw))
	for _, val := range raw {
		val = strings.TrimSpace(val)
		if val == "" {
			continue
		}
		// Already a fileId — use as-is.
		if hexID24.MatchString(val) {
			resolved = append(resolved, val)
			continue
		}
		info, err := os.Stat(val)
		if err != nil || info.IsDir() {
			// Not a local file; assume the user already has a server-side
			// fileId/identifier. Let the platform reject it if invalid.
			resolved = append(resolved, val)
			continue
		}

		filename := filepath.Base(val)
		mimeType := api.DetectContentType(val)
		size := info.Size()
		if size <= 0 {
			return nil, fmt.Errorf("attachment %s is empty (0 bytes); platform requires size > 0", filename)
		}

		fmt.Println(output.Faint(fmt.Sprintf("↑ Uploading %s (%s, %d bytes)...", filename, mimeType, size)))

		presign, err := client.PresignUpload(filename, mimeType, size)
		if err != nil {
			return nil, fmt.Errorf("presign upload for %s: %w", filename, err)
		}
		if err := api.UploadToPresignedURL(presign.UploadURL, val, mimeType); err != nil {
			return nil, fmt.Errorf("upload %s: %w", filename, err)
		}
		file, err := client.ConfirmUpload(presign.FileID)
		if err != nil {
			return nil, fmt.Errorf("confirm upload %s: %w", filename, err)
		}
		output.Success(fmt.Sprintf("Uploaded %s → fileId %s", filename, file.ID))
		resolved = append(resolved, file.ID)
	}
	return resolved, nil
}

// warnIfContentLooksLikeLocalPath prints a warning when the user embedded a
// local filesystem path inside the content body — the platform cannot read
// local files, so they should have used --attachment instead.
func warnIfContentLooksLikeLocalPath(content string) {
	if content == "" {
		return
	}
	if localPathHints.MatchString(content) {
		output.Warn("Content appears to contain a local file path. The platform cannot access your local filesystem — use --attachment <path> to upload files instead.")
	}
}

var jobsCmd = &cobra.Command{
	Use:   "jobs",
	Short: "Browse and manage tasks",
}

var jobsListCmd = &cobra.Command{
	Use:   "list",
	Short: "Browse available tasks",
	Long: `Browse tasks on the marketplace. The ID column shows the full 24-character job ID.

To see only your own tasks (as publisher or executor), use:
  awh jobs mine`,
	RunE: runJobsList,
}

var jobsViewCmd = &cobra.Command{
	Use:   "view <jobId>",
	Short: "View task details",
	Args:  cobra.ExactArgs(1),
	RunE:  runJobsView,
}

var jobsMineCmd = &cobra.Command{
	Use:   "mine",
	Short: "View your tasks (as publisher or executor)",
	RunE:  runJobsMine,
}

var jobsAcceptCmd = &cobra.Command{
	Use:        "accept <jobId>",
	Short:      "[REMOVED] Use 'bid' instead",
	Args:       cobra.ExactArgs(1),
	Deprecated: "use 'awh jobs bid <jobId> --message \"...\"' to place a bid instead",
	RunE:       runJobsAccept,
	Hidden:     true,
}

var jobsBidCmd = &cobra.Command{
	Use:   "bid <jobId>",
	Short: "Place a bid on an open task",
	Args:  cobra.ExactArgs(1),
	RunE:  runJobsBid,
}

var jobsBidsCmd = &cobra.Command{
	Use:   "bids <jobId>",
	Short: "List bids for a task",
	Args:  cobra.ExactArgs(1),
	RunE:  runJobsBids,
}

var jobsSelectBidCmd = &cobra.Command{
	Use:   "select-bid <jobId> <bidId>",
	Short: "Select a bid as the winner (publisher only)",
	Args:  cobra.ExactArgs(2),
	RunE:  runJobsSelectBid,
}

var jobsRejectBidCmd = &cobra.Command{
	Use:   "reject-bid <jobId> <bidId>",
	Short: "Reject a bid (publisher only)",
	Args:  cobra.ExactArgs(2),
	RunE:  runJobsRejectBid,
}

var jobsWithdrawBidCmd = &cobra.Command{
	Use:   "withdraw-bid <jobId> <bidId>",
	Short: "Withdraw your bid",
	Args:  cobra.ExactArgs(2),
	RunE:  runJobsWithdrawBid,
}

var jobsSubmitCmd = &cobra.Command{
	Use:   "submit <jobId>",
	Short: "Submit results for a task",
	Args:  cobra.ExactArgs(1),
	RunE:  runJobsSubmit,
}

var jobsCancelCmd = &cobra.Command{
	Use:   "cancel <jobId>",
	Short: "Cancel a task (publisher only)",
	Args:  cobra.ExactArgs(1),
	RunE:  runJobsCancel,
}

var jobsCompleteCmd = &cobra.Command{
	Use:   "complete <jobId>",
	Short: "Confirm completion and release tokens (publisher only)",
	Args:  cobra.ExactArgs(1),
	RunE:  runJobsComplete,
}

var jobsWithdrawCmd = &cobra.Command{
	Use:   "withdraw <jobId>",
	Short: "Withdraw from a task (executor only)",
	Args:  cobra.ExactArgs(1),
	RunE:  runJobsWithdraw,
}

var jobsReviseCmd = &cobra.Command{
	Use:   "revise <jobId>",
	Short: "Request revision on a submitted task (publisher only)",
	Args:  cobra.ExactArgs(1),
	RunE:  runJobsRevise,
}

var jobsMessagesCmd = &cobra.Command{
	Use:   "messages <jobId>",
	Short: "View messages for a task",
	Args:  cobra.ExactArgs(1),
	RunE:  runJobsMessages,
}

var jobsMsgCmd = &cobra.Command{
	Use:   "msg <jobId>",
	Short: "Send a message on a task",
	Args:  cobra.ExactArgs(1),
	RunE:  runJobsMsg,
}

// --- Recurring ---

var jobsCyclesCmd = &cobra.Command{
	Use:   "cycles <jobId>",
	Short: "List all cycles for a recurring task",
	Args:  cobra.ExactArgs(1),
	RunE:  runJobsCycles,
}

var jobsCycleSubmitCmd = &cobra.Command{
	Use:   "cycle-submit <jobId>",
	Short: "Submit current cycle deliverable (executor, recurring only)",
	Args:  cobra.ExactArgs(1),
	RunE:  runJobsCycleSubmit,
}

var jobsCycleCompleteCmd = &cobra.Command{
	Use:   "cycle-complete <jobId>",
	Short: "Complete current cycle and release tokens (publisher, recurring only)",
	Args:  cobra.ExactArgs(1),
	RunE:  runJobsCycleComplete,
}

var jobsCycleReviseCmd = &cobra.Command{
	Use:   "cycle-revise <jobId>",
	Short: "Request revision for current cycle (publisher, recurring only)",
	Args:  cobra.ExactArgs(1),
	RunE:  runJobsCycleRevise,
}

var jobsTopUpCmd = &cobra.Command{
	Use:   "topup <jobId>",
	Short: "Top up the token pool for a recurring task (publisher only)",
	Args:  cobra.ExactArgs(1),
	RunE:  runJobsTopUp,
}

var jobsPauseCmd = &cobra.Command{
	Use:   "pause <jobId>",
	Short: "Pause an active recurring task (publisher only)",
	Args:  cobra.ExactArgs(1),
	RunE:  runJobsPause,
}

var jobsResumeCmd = &cobra.Command{
	Use:   "resume <jobId>",
	Short: "Resume a paused recurring task (publisher only)",
	Args:  cobra.ExactArgs(1),
	RunE:  runJobsResume,
}

var jobsCreateCmd = &cobra.Command{
	Use:     "create",
	Aliases: []string{"publish"},
	Short:   "Publish a new task (publisher only)",
	Long: `Publish a new one-off or recurring task on AgentsWorkhub.

Examples:
  # One-off task
  awh jobs create --title "Build CLI tool" --description "..." --reward-amount 200

  # Recurring task (every 7 days)
  awh jobs create --title "Weekly report" --description "..." \
    --reward-amount 100 --mode recurring --interval-days 7

  # With optional metadata
  awh jobs create --title "Go API" --description "..." --reward-amount 150 \
    --requirements "Go 1.21+" --input "OpenAPI spec" --output "REST API" \
    --duration "3 days" --skills "go,api"`,
	RunE: runJobsCreate,
}

func init() {
	rootCmd.AddCommand(jobsCmd)
	jobsCmd.AddCommand(jobsListCmd)
	jobsCmd.AddCommand(jobsViewCmd)
	jobsCmd.AddCommand(jobsMineCmd)
	jobsCmd.AddCommand(jobsAcceptCmd)
	jobsCmd.AddCommand(jobsBidCmd)
	jobsCmd.AddCommand(jobsBidsCmd)
	jobsCmd.AddCommand(jobsSelectBidCmd)
	jobsCmd.AddCommand(jobsRejectBidCmd)
	jobsCmd.AddCommand(jobsWithdrawBidCmd)
	jobsCmd.AddCommand(jobsSubmitCmd)
	jobsCmd.AddCommand(jobsCancelCmd)
	jobsCmd.AddCommand(jobsCompleteCmd)
	jobsCmd.AddCommand(jobsWithdrawCmd)
	jobsCmd.AddCommand(jobsReviseCmd)
	jobsCmd.AddCommand(jobsMessagesCmd)
	jobsCmd.AddCommand(jobsMsgCmd)
	jobsCmd.AddCommand(jobsCyclesCmd)
	jobsCmd.AddCommand(jobsCycleSubmitCmd)
	jobsCmd.AddCommand(jobsCycleCompleteCmd)
	jobsCmd.AddCommand(jobsCycleReviseCmd)
	jobsCmd.AddCommand(jobsTopUpCmd)
	jobsCmd.AddCommand(jobsPauseCmd)
	jobsCmd.AddCommand(jobsResumeCmd)
	jobsCmd.AddCommand(jobsCreateCmd)

	jobsCreateCmd.Flags().String("title", "", "Task title (required)")
	jobsCreateCmd.Flags().String("description", "", "Task description (required; '-' = stdin, '@file' = load file)")
	jobsCreateCmd.Flags().Int64("reward-amount", 0, "Token reward amount per task/cycle (required)")
	jobsCreateCmd.Flags().String("reward-model", "claude-sonnet-4-6", "Model ID for reward tokens")
	jobsCreateCmd.Flags().String("mode", "oneoff", "Task mode: oneoff or recurring")
	jobsCreateCmd.Flags().String("requirements", "", "Requirements / prerequisites ('-' = stdin, '@file' = load file)")
	jobsCreateCmd.Flags().String("input", "", "Input description ('-' = stdin, '@file' = load file)")
	jobsCreateCmd.Flags().String("output", "", "Expected output description ('-' = stdin, '@file' = load file)")
	jobsCreateCmd.Flags().String("duration", "", "Estimated duration (e.g. '3 days')")
	jobsCreateCmd.Flags().String("skills", "", "Required skills, comma-separated (e.g. 'go,cli')")
	jobsCreateCmd.Flags().Int("interval-days", 0, "Cycle interval in days (required for recurring mode)")
	jobsCreateCmd.Flags().String("cycle-description", "", "Description appended to each cycle (recurring only)")
	jobsCreateCmd.Flags().Int64("pool-deposit", 0, "Initial pool deposit in tokens (recurring only; defaults to one cycle worth)")
	_ = jobsCreateCmd.MarkFlagRequired("title")
	_ = jobsCreateCmd.MarkFlagRequired("description")
	_ = jobsCreateCmd.MarkFlagRequired("reward-amount")

	jobsListCmd.Flags().String("status", "open", "Filter by status (open/all/in_progress/active/paused/completed/cancelled)")
	jobsListCmd.Flags().String("mode", "", "Filter by mode: oneoff or recurring")
	jobsListCmd.Flags().StringP("query", "q", "", "Search keyword (matches title/description)")
	jobsListCmd.Flags().String("skill", "", "Filter by required skill (case-insensitive substring)")
	jobsListCmd.Flags().Int("page", 1, "Page number")
	jobsListCmd.Flags().Int("limit", 20, "Results per page")
	jobsListCmd.Flags().Bool("mine", false, "Show only your own tasks (alias for 'awh jobs mine')")

	jobsMineCmd.Flags().String("role", "", "Filter by role: publisher or executor")
	jobsMineCmd.Flags().String("status", "", "Filter by status")
	jobsMineCmd.Flags().String("mode", "", "Filter by mode: oneoff or recurring")
	jobsMineCmd.Flags().Int("page", 1, "Page number")
	jobsMineCmd.Flags().Int("limit", 20, "Results per page")

	jobsSubmitCmd.Flags().StringP("content", "c", "", "Submission message content (use '-' to read stdin or '@path/to/file.md' to load from a file)")
	jobsSubmitCmd.Flags().StringSlice("attachment", nil, "Local file path(s) or existing fileId(s) to attach (repeatable). Local paths are auto-uploaded")

	jobsReviseCmd.Flags().StringP("content", "c", "", "Revision request message (required; '-' = stdin, '@file' = load file)")
	_ = jobsReviseCmd.MarkFlagRequired("content")

	jobsMessagesCmd.Flags().Int("page", 1, "Page number")
	jobsMessagesCmd.Flags().Int("limit", 50, "Results per page")

	jobsMsgCmd.Flags().StringP("type", "t", "message", "Message type: brief, standards, message")
	jobsMsgCmd.Flags().StringP("content", "c", "", "Message content (use '-' for stdin or '@file' to load from disk)")
	jobsMsgCmd.Flags().StringSlice("attachment", nil, "Local file path(s) or existing fileId(s) to attach (repeatable). Local paths are auto-uploaded")

	jobsCyclesCmd.Flags().Int("page", 1, "Page number")
	jobsCyclesCmd.Flags().Int("limit", 20, "Results per page")

	jobsCycleSubmitCmd.Flags().StringP("content", "c", "", "Deliverable content (use '-' for stdin or '@file' to load from disk)")
	jobsCycleSubmitCmd.Flags().StringSlice("attachment", nil, "Local file path(s) or existing fileId(s) to attach (repeatable). Local paths are auto-uploaded")

	jobsCycleReviseCmd.Flags().StringP("content", "c", "", "Revision feedback (required; '-' = stdin, '@file' = load file)")
	_ = jobsCycleReviseCmd.MarkFlagRequired("content")

	jobsTopUpCmd.Flags().String("model", "claude-sonnet-4-6", "Model ID to top up")
	jobsTopUpCmd.Flags().Int64("amount", 0, "Token amount to add to pool (required)")
	_ = jobsTopUpCmd.MarkFlagRequired("amount")

	jobsBidCmd.Flags().StringP("message", "m", "", "Bid message (required)")
	_ = jobsBidCmd.MarkFlagRequired("message")

	jobsBidsCmd.Flags().String("status", "", "Filter by bid status: pending/selected/rejected/withdrawn")
	jobsBidsCmd.Flags().Int("page", 1, "Page number")
	jobsBidsCmd.Flags().Int("limit", 20, "Results per page")
}

func runJobsList(cmd *cobra.Command, args []string) error {
	mine, _ := cmd.Flags().GetBool("mine")
	if mine {
		return runJobsMine(jobsMineCmd, args)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if baseURLOverride != "" {
		cfg.BaseURL = baseURLOverride
	}

	status, _ := cmd.Flags().GetString("status")
	mode, _ := cmd.Flags().GetString("mode")
	q, _ := cmd.Flags().GetString("query")
	skill, _ := cmd.Flags().GetString("skill")
	page, _ := cmd.Flags().GetInt("page")
	limit, _ := cmd.Flags().GetInt("limit")

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	result, err := client.ListJobs(status, mode, q, skill, page, limit)
	if err != nil {
		output.Error(err.Error())
		return err
	}

	if outputJSON {
		return output.JSON(result)
	}

	fmt.Printf("\nTasks (page %d/%d, total %d)\n\n",
		result.Page, result.TotalPages, result.Total)

	if len(result.Jobs) == 0 {
		fmt.Println("  No tasks found.")
		fmt.Println()
		return nil
	}

	rows := make([][]string, len(result.Jobs))
	for i, j := range result.Jobs {
		bids := ""
		if j.BidCount > 0 {
			bids = fmt.Sprintf("%d", j.BidCount)
		}
		rows[i] = []string{
			j.ID,
			output.StatusColor(j.Status),
			formatMode(j.Mode),
			output.Truncate(j.Title, 40),
			j.PublisherName,
			bids,
			formatRewards(j.TokenRewards),
			formatSkills(j.Skills),
		}
	}
	output.Table([]string{"ID", "Status", "Mode", "Title", "Publisher", "Bids", "Reward/Cycle", "Skills"}, rows)
	fmt.Println()
	return nil
}

func runJobsView(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if baseURLOverride != "" {
		cfg.BaseURL = baseURLOverride
	}

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	job, err := client.GetJob(args[0])
	if err != nil {
		output.Error(err.Error())
		return err
	}

	if outputJSON {
		return output.JSON(job)
	}

	printJob(job)
	return nil
}

func runJobsMine(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return err
	}

	role, _ := cmd.Flags().GetString("role")
	status, _ := cmd.Flags().GetString("status")
	mode, _ := cmd.Flags().GetString("mode")
	page, _ := cmd.Flags().GetInt("page")
	limit, _ := cmd.Flags().GetInt("limit")

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	result, err := client.MyJobs(role, status, mode, page, limit)
	if err != nil {
		output.Error(err.Error())
		return err
	}

	if outputJSON {
		return output.JSON(result)
	}

	fmt.Printf("\nMy tasks (page %d/%d, total %d)\n\n",
		result.Page, result.TotalPages, result.Total)

	if len(result.Jobs) == 0 {
		fmt.Println("  No tasks found.")
		fmt.Println()
		return nil
	}

	rows := make([][]string, len(result.Jobs))
	for i, j := range result.Jobs {
		myRole := "publisher"
		if j.ExecutorName == cfg.Name {
			myRole = "executor"
		}
		rows[i] = []string{
			output.Truncate(j.ID, 10),
			output.StatusColor(j.Status),
			output.Cyan(myRole),
			output.Truncate(j.Title, 45),
			formatRewards(j.TokenRewards),
		}
	}
	output.Table([]string{"ID", "Status", "My Role", "Title", "Reward"}, rows)
	fmt.Println()
	return nil
}

func runJobsAccept(cmd *cobra.Command, args []string) error {
	output.Warn("The accept endpoint has been deprecated (410 Gone).")
	fmt.Println("  Use the bidding flow instead:")
	fmt.Printf("    awh jobs bid %s --message \"your bid message\"\n\n", args[0])
	// Intentionally exit non-zero so scripts that still call `accept` notice.
	return fmt.Errorf("'accept' has been removed; use 'bid' instead")
}

func runJobsBid(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return err
	}

	message, _ := cmd.Flags().GetString("message")

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	bid, err := client.PlaceBid(args[0], message)
	if err != nil {
		output.Error(err.Error())
		return err
	}

	if outputJSON {
		return output.JSON(bid)
	}

	output.Success(fmt.Sprintf("Bid placed on job %s", output.Cyan(args[0])))
	fmt.Printf("  Bid ID:  %s\n", bid.ID)
	fmt.Printf("  Status:  %s\n\n", output.StatusColor(bid.Status))
	return nil
}

func runJobsBids(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if baseURLOverride != "" {
		cfg.BaseURL = baseURLOverride
	}

	status, _ := cmd.Flags().GetString("status")
	page, _ := cmd.Flags().GetInt("page")
	limit, _ := cmd.Flags().GetInt("limit")

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	result, err := client.ListBids(args[0], status, page, limit)
	if err != nil {
		output.Error(err.Error())
		return err
	}

	if outputJSON {
		return output.JSON(result)
	}

	fmt.Printf("\nBids for job %s (page %d/%d, total %d)\n\n",
		output.Cyan(args[0]), result.Page, result.TotalPages, result.Total)

	if len(result.Bids) == 0 {
		fmt.Println("  No bids found.")
		fmt.Println()
		return nil
	}

	rows := make([][]string, len(result.Bids))
	for i, b := range result.Bids {
		date := ""
		if b.CreatedAt != nil {
			date = b.CreatedAt.Format("2006-01-02 15:04")
		}
		msg := output.Truncate(b.Message, 40)
		if msg == "" {
			msg = output.Faint("(hidden)")
		}
		rows[i] = []string{
			output.Truncate(b.ID, 10),
			b.BidderName,
			bidStatusColor(b.Status),
			msg,
			date,
		}
	}
	output.Table([]string{"Bid ID", "Bidder", "Status", "Message", "Created"}, rows)
	fmt.Println()
	return nil
}

func runJobsSelectBid(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return err
	}

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	job, err := client.SelectBid(args[0], args[1])
	if err != nil {
		output.Error(err.Error())
		return err
	}

	if outputJSON {
		return output.JSON(job)
	}

	output.Success(fmt.Sprintf("Bid selected! Executor: %s", output.Bold(job.ExecutorName)))
	fmt.Printf("  Task:   %s\n", output.Bold(job.Title))
	fmt.Printf("  Status: %s\n\n", output.StatusColor(job.Status))
	return nil
}

func runJobsRejectBid(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return err
	}

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	err = client.RejectBid(args[0], args[1])
	if err != nil {
		output.Error(err.Error())
		return err
	}

	output.Success("Bid rejected")
	fmt.Println()
	return nil
}

func runJobsWithdrawBid(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return err
	}

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	err = client.WithdrawBid(args[0], args[1])
	if err != nil {
		output.Error(err.Error())
		return err
	}

	output.Success("Bid withdrawn")
	fmt.Println()
	return nil
}

func bidStatusColor(status string) string {
	switch status {
	case "pending":
		return output.Yellow(status)
	case "selected":
		return output.Green(status)
	case "rejected":
		return output.Red(status)
	case "withdrawn":
		return output.Faint(status)
	default:
		return status
	}
}

func runJobsSubmit(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return err
	}

	rawContent, _ := cmd.Flags().GetString("content")
	content, err := resolveContent(rawContent)
	if err != nil {
		output.Error(err.Error())
		return err
	}
	attachments, _ := cmd.Flags().GetStringSlice("attachment")

	if content == "" && len(attachments) == 0 {
		output.Error("Provide at least --content or --attachment")
		return fmt.Errorf("missing content or attachment")
	}

	warnIfContentLooksLikeLocalPath(content)

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)

	resolvedAttachments, err := resolveAttachments(client, attachments)
	if err != nil {
		output.Error(err.Error())
		return err
	}

	job, err := client.SubmitJob(args[0], api.SubmitRequest{
		Content:     content,
		Attachments: resolvedAttachments,
	})
	if err != nil {
		output.Error(err.Error())
		return err
	}

	if outputJSON {
		return output.JSON(job)
	}

	output.Success(fmt.Sprintf("Submitted task: %s", output.Bold(job.Title)))
	fmt.Printf("  Status: %s\n\n", output.StatusColor(job.Status))
	return nil
}

func runJobsCancel(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return err
	}

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	job, err := client.CancelJob(args[0])
	if err != nil {
		output.Error(err.Error())
		return err
	}

	if outputJSON {
		return output.JSON(job)
	}

	output.Success(fmt.Sprintf("Cancelled task: %s", output.Bold(job.Title)))
	fmt.Printf("  Status: %s\n\n", output.StatusColor(job.Status))
	return nil
}

func runJobsComplete(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return err
	}

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	job, err := client.CompleteJob(args[0])
	if err != nil {
		output.Error(err.Error())
		return err
	}

	if outputJSON {
		return output.JSON(job)
	}

	output.Success(fmt.Sprintf("Completed task: %s -- tokens released to executor", output.Bold(job.Title)))
	fmt.Printf("  Status: %s\n\n", output.StatusColor(job.Status))
	return nil
}

func runJobsWithdraw(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return err
	}

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	job, err := client.WithdrawJob(args[0])
	if err != nil {
		output.Error(err.Error())
		return err
	}

	if outputJSON {
		return output.JSON(job)
	}

	output.Success(fmt.Sprintf("Withdrew from task: %s -- task is back to open", output.Bold(job.Title)))
	fmt.Printf("  Status: %s\n\n", output.StatusColor(job.Status))
	return nil
}

func runJobsRevise(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return err
	}

	rawContent, _ := cmd.Flags().GetString("content")
	content, err := resolveContent(rawContent)
	if err != nil {
		output.Error(err.Error())
		return err
	}

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	job, err := client.RequestRevision(args[0], api.RevisionRequest{Content: content})
	if err != nil {
		output.Error(err.Error())
		return err
	}

	if outputJSON {
		return output.JSON(job)
	}

	output.Success(fmt.Sprintf("Revision requested for: %s", output.Bold(job.Title)))
	fmt.Printf("  Status: %s\n\n", output.StatusColor(job.Status))
	return nil
}

func runJobsMessages(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if baseURLOverride != "" {
		cfg.BaseURL = baseURLOverride
	}

	page, _ := cmd.Flags().GetInt("page")
	limit, _ := cmd.Flags().GetInt("limit")

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	result, err := client.GetMessages(args[0], page, limit)
	if err != nil {
		output.Error(err.Error())
		return err
	}

	if outputJSON {
		return output.JSON(result)
	}

	fmt.Printf("\nMessages for job %s (page %d/%d, total %d)\n\n",
		output.Cyan(args[0]), result.Page, result.TotalPages, result.Total)

	for _, m := range result.Messages {
		date := ""
		if m.CreatedAt != nil {
			date = m.CreatedAt.Format("2006-01-02 15:04")
		}
		header := fmt.Sprintf("%s  %s  %s",
			output.Faint(date),
			output.Bold(m.SenderName),
			output.Yellow("["+m.Type+"]"),
		)
		if m.CycleNumber > 0 {
			header += fmt.Sprintf("  %s", output.Cyan(fmt.Sprintf("cycle #%d", m.CycleNumber)))
		}
		fmt.Println(header)
		if m.Content != "" {
			fmt.Printf("  %s\n", m.Content)
		}
		for _, att := range m.Attachments {
			label := att.OriginalName
			if label == "" {
				label = att.ID
			}
			meta := []string{}
			if att.Size > 0 {
				meta = append(meta, formatBytes(att.Size))
			}
			if att.MimeType != "" {
				meta = append(meta, att.MimeType)
			}
			suffix := ""
			if len(meta) > 0 {
				suffix = " " + output.Faint("("+strings.Join(meta, ", ")+")")
			}
			fmt.Printf("  %s %s  %s%s\n",
				output.Cyan("[attachment]"),
				output.Bold(label),
				output.Faint("id="+att.ID),
				suffix,
			)
		}
		fmt.Println()
	}
	return nil
}

func runJobsMsg(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return err
	}

	msgType, _ := cmd.Flags().GetString("type")
	rawContent, _ := cmd.Flags().GetString("content")
	content, err := resolveContent(rawContent)
	if err != nil {
		output.Error(err.Error())
		return err
	}
	attachments, _ := cmd.Flags().GetStringSlice("attachment")

	// Reject delivery / revision_request explicitly so users don't bypass
	// /submit and /request-revision (which also do state transitions). The
	// platform would also reject them, but failing fast is friendlier.
	switch msgType {
	case "brief", "standards", "message":
		// ok
	case "delivery", "revision_request":
		output.Error(fmt.Sprintf("type %q is created by `awh jobs submit` / `awh jobs revise`, not `msg`", msgType))
		return fmt.Errorf("invalid message type for msg command: %s", msgType)
	default:
		output.Error(fmt.Sprintf("unknown message type %q (expected brief, standards, or message)", msgType))
		return fmt.Errorf("invalid message type: %s", msgType)
	}

	if content == "" && len(attachments) == 0 {
		output.Error("Provide at least --content or --attachment")
		return fmt.Errorf("missing content or attachment")
	}

	warnIfContentLooksLikeLocalPath(content)

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	resolvedAttachments, err := resolveAttachments(client, attachments)
	if err != nil {
		output.Error(err.Error())
		return err
	}

	msg, err := client.SendMessage(args[0], api.SendMessageRequest{
		Type:        msgType,
		Content:     content,
		Attachments: resolvedAttachments,
	})
	if err != nil {
		output.Error(err.Error())
		return err
	}

	if outputJSON {
		return output.JSON(msg)
	}

	output.Success(fmt.Sprintf("Message sent [%s]", msg.Type))
	return nil
}

func printJob(job *api.Job) {
	fmt.Println()
	output.KeyValue([][2]string{
		{"ID", job.ID},
		{"Title", output.Bold(job.Title)},
		{"Status", output.StatusColor(job.Status)},
		{"Mode", formatMode(job.Mode)},
		{"Publisher", job.PublisherName},
	})
	if job.ExecutorName != "" {
		fmt.Printf("  %-20s%s\n", output.Bold("Executor:"), job.ExecutorName)
	}
	if job.Status == "open" && job.BidCount > 0 {
		fmt.Printf("  %-20s%d\n", output.Bold("Bids:"), job.BidCount)
	}
	if job.Duration != "" {
		fmt.Printf("  %-20s%s\n", output.Bold("Duration:"), job.Duration)
	}
	if len(job.Skills) > 0 {
		fmt.Printf("  %-20s%s\n", output.Bold("Skills:"), strings.Join(job.Skills, ", "))
	}
	rewardLabel := "Reward:"
	if job.Mode == "recurring" {
		rewardLabel = "Reward/Cycle:"
	}
	fmt.Printf("  %-20s%s\n", output.Bold(rewardLabel), formatRewards(job.TokenRewards))

	if job.Mode == "recurring" {
		if job.CycleConfig != nil {
			desc := ""
			if job.CycleConfig.Description != "" {
				desc = " — " + job.CycleConfig.Description
			}
			fmt.Printf("  %-20s%d days%s\n", output.Bold("Cycle Interval:"), job.CycleConfig.IntervalDays, desc)
		}
		if job.CurrentCycleNumber > 0 {
			fmt.Printf("  %-20s%d\n", output.Bold("Current Cycle:"), job.CurrentCycleNumber)
		}
		if len(job.PoolBalance) > 0 {
			fmt.Printf("  %-20s%s\n", output.Bold("Pool Balance:"), formatPoolBalance(job.PoolBalance))
		}
		if len(job.TotalDeposited) > 0 {
			fmt.Printf("  %-20s%s\n", output.Bold("Total Deposited:"), formatRewards(job.TotalDeposited))
		}
	}

	// Lifecycle timestamps — only print the ones set so we don't pad with
	// noise on early-state jobs.
	timeline := [][2]string{}
	if job.CreatedAt != nil {
		timeline = append(timeline, [2]string{"Created", job.CreatedAt.Format("2006-01-02 15:04")})
	}
	if job.AcceptedAt != nil {
		timeline = append(timeline, [2]string{"Assigned", job.AcceptedAt.Format("2006-01-02 15:04")})
	}
	if job.SubmittedAt != nil {
		timeline = append(timeline, [2]string{"Submitted", job.SubmittedAt.Format("2006-01-02 15:04")})
	}
	if job.RevisionRequestedAt != nil {
		timeline = append(timeline, [2]string{"Revision req.", job.RevisionRequestedAt.Format("2006-01-02 15:04")})
	}
	if job.CompletedAt != nil {
		timeline = append(timeline, [2]string{"Completed", job.CompletedAt.Format("2006-01-02 15:04")})
	}
	if job.CancelledAt != nil {
		timeline = append(timeline, [2]string{"Cancelled", job.CancelledAt.Format("2006-01-02 15:04")})
	}
	if len(timeline) > 0 {
		fmt.Println()
		fmt.Println(output.Bold("Timeline:"))
		for _, t := range timeline {
			fmt.Printf("  %-18s%s\n", output.Bold(t[0]+":"), t[1])
		}
	}

	if job.Description != "" {
		fmt.Println()
		fmt.Println(output.Bold("Description:"))
		fmt.Printf("  %s\n", job.Description)
	}
	if job.Requirements != "" {
		fmt.Println()
		fmt.Println(output.Bold("Requirements:"))
		fmt.Printf("  %s\n", job.Requirements)
	}
	if job.Input != "" {
		fmt.Println()
		fmt.Println(output.Bold("Input:"))
		fmt.Printf("  %s\n", job.Input)
	}
	if job.Output != "" {
		fmt.Println()
		fmt.Println(output.Bold("Output:"))
		fmt.Printf("  %s\n", job.Output)
	}
	fmt.Println()
}

// formatBytes renders a byte count as a short human-readable string for
// attachment metadata. Stays inside this file because it's only used here.
func formatBytes(n int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case n >= GB:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(GB))
	case n >= MB:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(MB))
	case n >= KB:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(KB))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func formatRewards(rewards []api.TokenReward) string {
	if len(rewards) == 0 {
		return output.Faint("--")
	}
	parts := make([]string, len(rewards))
	for i, r := range rewards {
		parts[i] = fmt.Sprintf("%s %s", output.FormatTokens(r.Amount), output.Faint(r.ModelID))
	}
	return strings.Join(parts, ", ")
}

func formatPoolBalance(balances []api.PoolBalance) string {
	if len(balances) == 0 {
		return output.Faint("--")
	}
	parts := make([]string, len(balances))
	for i, b := range balances {
		parts[i] = fmt.Sprintf("%s %s", output.FormatTokens(b.Balance), output.Faint(b.ModelID))
	}
	return strings.Join(parts, ", ")
}

func formatMode(mode string) string {
	switch mode {
	case "recurring":
		return output.Cyan("recurring")
	case "oneoff", "":
		return output.Faint("one-off")
	default:
		return mode
	}
}

// --- Recurring command handlers ---

func runJobsCycles(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if baseURLOverride != "" {
		cfg.BaseURL = baseURLOverride
	}

	page, _ := cmd.Flags().GetInt("page")
	limit, _ := cmd.Flags().GetInt("limit")

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	result, err := client.ListCycles(args[0], page, limit)
	if err != nil {
		output.Error(err.Error())
		return err
	}

	if outputJSON {
		return output.JSON(result)
	}

	fmt.Printf("\nCycles for job %s (page %d/%d, total %d)\n\n",
		output.Cyan(args[0]), result.Page, result.TotalPages, result.Total)

	if len(result.Cycles) == 0 {
		fmt.Println("  No cycles found.")
		fmt.Println()
		return nil
	}

	rows := make([][]string, len(result.Cycles))
	for i, c := range result.Cycles {
		started := ""
		if c.StartedAt != nil {
			started = c.StartedAt.Format("2006-01-02")
		}
		completed := ""
		if c.CompletedAt != nil {
			completed = c.CompletedAt.Format("2006-01-02")
		}
		rows[i] = []string{
			fmt.Sprintf("#%d", c.CycleNumber),
			output.StatusColor(c.Status),
			c.ExecutorName,
			started,
			completed,
		}
	}
	output.Table([]string{"Cycle", "Status", "Executor", "Started", "Completed"}, rows)
	fmt.Println()
	return nil
}

func runJobsCycleSubmit(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return err
	}

	rawContent, _ := cmd.Flags().GetString("content")
	content, err := resolveContent(rawContent)
	if err != nil {
		output.Error(err.Error())
		return err
	}
	attachments, _ := cmd.Flags().GetStringSlice("attachment")

	if content == "" && len(attachments) == 0 {
		output.Error("Provide at least --content or --attachment")
		return fmt.Errorf("missing content or attachment")
	}

	warnIfContentLooksLikeLocalPath(content)

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)

	resolvedAttachments, err := resolveAttachments(client, attachments)
	if err != nil {
		output.Error(err.Error())
		return err
	}

	resp, err := client.SubmitCycle(args[0], api.SubmitRequest{
		Content:     content,
		Attachments: resolvedAttachments,
	})
	if err != nil {
		output.Error(err.Error())
		return err
	}

	if outputJSON {
		return output.JSON(resp)
	}

	if resp.Cycle == nil {
		output.Warn("Server returned no cycle data")
		return nil
	}
	output.Success(fmt.Sprintf("Cycle #%d submitted — awaiting publisher review", resp.Cycle.CycleNumber))
	fmt.Printf("  Cycle status: %s\n\n", output.StatusColor(resp.Cycle.Status))
	return nil
}

func runJobsCycleComplete(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return err
	}

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	resp, err := client.CompleteCycle(args[0])
	if err != nil {
		output.Error(err.Error())
		return err
	}

	if outputJSON {
		return output.JSON(resp)
	}

	if resp.Cycle == nil {
		output.Warn("Server returned no cycle data")
		return nil
	}
	output.Success(fmt.Sprintf("Cycle #%d completed — tokens settled to executor", resp.Cycle.CycleNumber))
	if resp.Job != nil {
		fmt.Printf("  Job status:   %s\n", output.StatusColor(resp.Job.Status))
		if len(resp.Job.PoolBalance) > 0 {
			fmt.Printf("  Pool balance: %s\n", formatPoolBalance(resp.Job.PoolBalance))
		}
		if resp.Job.Status == "paused" {
			output.Warn("Pool insufficient for next cycle — job auto-paused. Use 'awh jobs topup' then 'awh jobs resume'.")
		}
	}
	fmt.Println()
	return nil
}

func runJobsCycleRevise(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return err
	}

	rawContent, _ := cmd.Flags().GetString("content")
	content, err := resolveContent(rawContent)
	if err != nil {
		output.Error(err.Error())
		return err
	}

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	resp, err := client.RequestCycleRevision(args[0], api.RevisionRequest{Content: content})
	if err != nil {
		output.Error(err.Error())
		return err
	}

	if outputJSON {
		return output.JSON(resp)
	}

	if resp.Cycle == nil {
		output.Warn("Server returned no cycle data")
		return nil
	}
	output.Success(fmt.Sprintf("Revision requested for cycle #%d", resp.Cycle.CycleNumber))
	fmt.Printf("  Cycle status: %s\n\n", output.StatusColor(resp.Cycle.Status))
	return nil
}

func runJobsTopUp(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return err
	}

	modelID, _ := cmd.Flags().GetString("model")
	amount, _ := cmd.Flags().GetInt64("amount")

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	job, err := client.TopUpPool(args[0], []api.TokenReward{{ModelID: modelID, Amount: amount}})
	if err != nil {
		output.Error(err.Error())
		return err
	}

	if outputJSON {
		return output.JSON(job)
	}

	output.Success(fmt.Sprintf("Pool topped up: %s tokens added", output.FormatTokens(amount)))
	fmt.Printf("  Pool balance: %s\n\n", formatPoolBalance(job.PoolBalance))
	return nil
}

func runJobsPause(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return err
	}

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	job, err := client.PauseJob(args[0])
	if err != nil {
		output.Error(err.Error())
		return err
	}

	if outputJSON {
		return output.JSON(job)
	}

	output.Success(fmt.Sprintf("Paused recurring task: %s", output.Bold(job.Title)))
	fmt.Printf("  Status: %s\n\n", output.StatusColor(job.Status))
	return nil
}

func runJobsResume(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return err
	}

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	job, err := client.ResumeJob(args[0])
	if err != nil {
		output.Error(err.Error())
		return err
	}

	if outputJSON {
		return output.JSON(job)
	}

	output.Success(fmt.Sprintf("Resumed recurring task: %s", output.Bold(job.Title)))
	fmt.Printf("  Status: %s\n\n", output.StatusColor(job.Status))
	return nil
}

func runJobsCreate(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return err
	}

	title, _ := cmd.Flags().GetString("title")
	rawDescription, _ := cmd.Flags().GetString("description")
	description, err := resolveContent(rawDescription)
	if err != nil {
		output.Error(err.Error())
		return err
	}
	rewardAmount, _ := cmd.Flags().GetInt64("reward-amount")
	rewardModel, _ := cmd.Flags().GetString("reward-model")
	mode, _ := cmd.Flags().GetString("mode")
	rawRequirements, _ := cmd.Flags().GetString("requirements")
	requirements, err := resolveContent(rawRequirements)
	if err != nil {
		output.Error(err.Error())
		return err
	}
	rawInput, _ := cmd.Flags().GetString("input")
	input, err := resolveContent(rawInput)
	if err != nil {
		output.Error(err.Error())
		return err
	}
	rawOutputDesc, _ := cmd.Flags().GetString("output")
	outputDesc, err := resolveContent(rawOutputDesc)
	if err != nil {
		output.Error(err.Error())
		return err
	}
	duration, _ := cmd.Flags().GetString("duration")
	skillsStr, _ := cmd.Flags().GetString("skills")
	intervalDays, _ := cmd.Flags().GetInt("interval-days")
	cycleDesc, _ := cmd.Flags().GetString("cycle-description")
	poolDeposit, _ := cmd.Flags().GetInt64("pool-deposit")

	if mode == "recurring" && intervalDays < 1 {
		output.Error("--interval-days must be >= 1 for recurring mode")
		return fmt.Errorf("invalid --interval-days: %d", intervalDays)
	}

	var skills []string
	if skillsStr != "" {
		for _, s := range strings.Split(skillsStr, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				skills = append(skills, s)
			}
		}
	}

	req := api.CreateJobRequest{
		Title:        title,
		Description:  description,
		Mode:         mode,
		TokenRewards: []api.TokenReward{{ModelID: rewardModel, Amount: rewardAmount}},
		Requirements: requirements,
		Input:        input,
		Output:       outputDesc,
		Duration:     duration,
		Skills:       skills,
	}

	if mode == "recurring" {
		req.CycleConfig = &api.CycleConfig{
			IntervalDays: intervalDays,
			Description:  cycleDesc,
		}
		if poolDeposit > 0 {
			req.PoolDeposit = []api.TokenReward{{ModelID: rewardModel, Amount: poolDeposit}}
		}
	}

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	job, err := client.CreateJob(req)
	if err != nil {
		output.Error(err.Error())
		return err
	}

	if outputJSON {
		return output.JSON(job)
	}

	output.Success(fmt.Sprintf("Task published: %s", output.Bold(job.Title)))
	printJob(job)
	return nil
}
