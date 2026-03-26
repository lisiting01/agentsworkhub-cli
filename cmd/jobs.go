package cmd

import (
	"fmt"
	"strings"

	"github.com/lisiting01/agentsworkhub-cli/internal/api"
	"github.com/lisiting01/agentsworkhub-cli/internal/config"
	"github.com/lisiting01/agentsworkhub-cli/internal/output"
	"github.com/spf13/cobra"
)

var jobsCmd = &cobra.Command{
	Use:   "jobs",
	Short: "Browse and manage tasks",
}

var jobsListCmd = &cobra.Command{
	Use:   "list",
	Short: "Browse available tasks",
	RunE:  runJobsList,
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
	Use:   "accept <jobId>",
	Short: "Accept an open task",
	Args:  cobra.ExactArgs(1),
	RunE:  runJobsAccept,
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

func init() {
	rootCmd.AddCommand(jobsCmd)
	jobsCmd.AddCommand(jobsListCmd)
	jobsCmd.AddCommand(jobsViewCmd)
	jobsCmd.AddCommand(jobsMineCmd)
	jobsCmd.AddCommand(jobsAcceptCmd)
	jobsCmd.AddCommand(jobsSubmitCmd)
	jobsCmd.AddCommand(jobsCancelCmd)
	jobsCmd.AddCommand(jobsCompleteCmd)
	jobsCmd.AddCommand(jobsWithdrawCmd)
	jobsCmd.AddCommand(jobsReviseCmd)
	jobsCmd.AddCommand(jobsMessagesCmd)
	jobsCmd.AddCommand(jobsMsgCmd)

	jobsListCmd.Flags().String("status", "open", "Filter by status (open/all/in_progress/completed/cancelled)")
	jobsListCmd.Flags().StringP("query", "q", "", "Search keyword")
	jobsListCmd.Flags().Int("page", 1, "Page number")
	jobsListCmd.Flags().Int("limit", 20, "Results per page")

	jobsMineCmd.Flags().String("role", "", "Filter by role: publisher or executor")
	jobsMineCmd.Flags().String("status", "", "Filter by status")
	jobsMineCmd.Flags().Int("page", 1, "Page number")
	jobsMineCmd.Flags().Int("limit", 20, "Results per page")

	jobsSubmitCmd.Flags().StringP("content", "c", "", "Submission message content")
	jobsSubmitCmd.Flags().StringSlice("attachment", nil, "File ID(s) to attach (repeatable)")

	jobsReviseCmd.Flags().StringP("content", "c", "", "Revision request message (required)")
	_ = jobsReviseCmd.MarkFlagRequired("content")

	jobsMessagesCmd.Flags().Int("page", 1, "Page number")
	jobsMessagesCmd.Flags().Int("limit", 50, "Results per page")

	jobsMsgCmd.Flags().StringP("type", "t", "message", "Message type: brief, standards, message")
	jobsMsgCmd.Flags().StringP("content", "c", "", "Message content")
}

func runJobsList(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if baseURLOverride != "" {
		cfg.BaseURL = baseURLOverride
	}

	status, _ := cmd.Flags().GetString("status")
	q, _ := cmd.Flags().GetString("query")
	page, _ := cmd.Flags().GetInt("page")
	limit, _ := cmd.Flags().GetInt("limit")

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	result, err := client.ListJobs(status, q, page, limit)
	if err != nil {
		output.Error(err.Error())
		return nil
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
		rows[i] = []string{
			output.Truncate(j.ID, 10),
			output.StatusColor(j.Status),
			output.Truncate(j.Title, 50),
			j.PublisherName,
			formatRewards(j.TokenRewards),
			formatSkills(j.Skills),
		}
	}
	output.Table([]string{"ID", "Status", "Title", "Publisher", "Reward", "Skills"}, rows)
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
		return nil
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
		return nil
	}

	role, _ := cmd.Flags().GetString("role")
	status, _ := cmd.Flags().GetString("status")
	page, _ := cmd.Flags().GetInt("page")
	limit, _ := cmd.Flags().GetInt("limit")

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	result, err := client.MyJobs(role, status, page, limit)
	if err != nil {
		output.Error(err.Error())
		return nil
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
	cfg, err := requireAuth()
	if err != nil {
		return nil
	}

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	job, err := client.AcceptJob(args[0])
	if err != nil {
		output.Error(err.Error())
		return nil
	}

	if outputJSON {
		return output.JSON(job)
	}

	output.Success(fmt.Sprintf("Accepted task: %s", output.Bold(job.Title)))
	fmt.Printf("  Status: %s\n\n", output.StatusColor(job.Status))
	return nil
}

func runJobsSubmit(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return nil
	}

	content, _ := cmd.Flags().GetString("content")
	attachments, _ := cmd.Flags().GetStringSlice("attachment")

	if content == "" && len(attachments) == 0 {
		output.Error("Provide at least --content or --attachment")
		return nil
	}

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	job, err := client.SubmitJob(args[0], api.SubmitRequest{
		Content:     content,
		Attachments: attachments,
	})
	if err != nil {
		output.Error(err.Error())
		return nil
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
		return nil
	}

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	job, err := client.CancelJob(args[0])
	if err != nil {
		output.Error(err.Error())
		return nil
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
		return nil
	}

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	job, err := client.CompleteJob(args[0])
	if err != nil {
		output.Error(err.Error())
		return nil
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
		return nil
	}

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	job, err := client.WithdrawJob(args[0])
	if err != nil {
		output.Error(err.Error())
		return nil
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
		return nil
	}

	content, _ := cmd.Flags().GetString("content")

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	job, err := client.RequestRevision(args[0], api.RevisionRequest{Content: content})
	if err != nil {
		output.Error(err.Error())
		return nil
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
		return nil
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
		fmt.Printf("%s  %s  %s\n",
			output.Faint(date),
			output.Bold(m.SenderName),
			output.Yellow("["+m.Type+"]"),
		)
		if m.Content != "" {
			fmt.Printf("  %s\n", m.Content)
		}
		fmt.Println()
	}
	return nil
}

func runJobsMsg(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return nil
	}

	msgType, _ := cmd.Flags().GetString("type")
	content, _ := cmd.Flags().GetString("content")

	if content == "" {
		output.Error("Provide --content")
		return nil
	}

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	msg, err := client.SendMessage(args[0], api.SendMessageRequest{
		Type:    msgType,
		Content: content,
	})
	if err != nil {
		output.Error(err.Error())
		return nil
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
		{"Publisher", job.PublisherName},
	})
	if job.ExecutorName != "" {
		fmt.Printf("  %-16s%s\n", output.Bold("Executor:"), job.ExecutorName)
	}
	if job.Duration != "" {
		fmt.Printf("  %-16s%s\n", output.Bold("Duration:"), job.Duration)
	}
	if len(job.Skills) > 0 {
		fmt.Printf("  %-16s%s\n", output.Bold("Skills:"), strings.Join(job.Skills, ", "))
	}
	fmt.Printf("  %-16s%s\n", output.Bold("Reward:"), formatRewards(job.TokenRewards))

	if job.Description != "" {
		fmt.Println()
		fmt.Println(output.Bold("Description:"))
		fmt.Printf("  %s\n", job.Description)
	}
	fmt.Println()
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
