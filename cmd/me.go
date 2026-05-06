package cmd

import (
	"fmt"
	"strings"

	"github.com/lisiting01/agentsworkhub-cli/internal/api"
	"github.com/lisiting01/agentsworkhub-cli/internal/config"
	"github.com/lisiting01/agentsworkhub-cli/internal/output"
	"github.com/spf13/cobra"
)

var meCmd = &cobra.Command{
	Use:   "me",
	Short: "View your agent profile and token balances",
	RunE:  runMe,
}

var txCmd = &cobra.Command{
	Use:   "transactions",
	Short: "View your transaction history",
	RunE:  runTransactions,
}

var meUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update your agent profile (bio, country, contact, hidden)",
	RunE:  runMeUpdate,
}

func init() {
	rootCmd.AddCommand(meCmd)
	meCmd.AddCommand(txCmd)
	meCmd.AddCommand(meUpdateCmd)

	txCmd.Flags().String("model", "", "Filter by modelId")
	txCmd.Flags().Int("page", 1, "Page number")
	txCmd.Flags().Int("limit", 20, "Results per page")

	meUpdateCmd.Flags().String("bio", "", "Short bio")
	meUpdateCmd.Flags().String("country", "", "Country")
	meUpdateCmd.Flags().String("contact", "", "Contact URL or info")
	meUpdateCmd.Flags().Bool("hidden", false, "Hide your profile from the public agent list")
	meUpdateCmd.Flags().Bool("visible", false, "Show your profile on the public agent list")
}

func requireAuth() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if !cfg.IsLoggedIn() {
		output.Error("Not logged in. Run: awh auth register")
		return nil, fmt.Errorf("not authenticated")
	}
	if baseURLOverride != "" {
		cfg.BaseURL = baseURLOverride
	}
	return cfg, nil
}

// loadConfig loads config silently (no user-facing error output).
// Used in daemon children where stderr goes to a log file.
func loadConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if baseURLOverride != "" {
		cfg.BaseURL = baseURLOverride
	}
	return cfg, nil
}

func runMe(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return err
	}

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	profile, err := client.Me()
	if err != nil {
		output.Error(err.Error())
		return err
	}

	if outputJSON {
		return output.JSON(profile)
	}

	fmt.Println()
	output.KeyValue([][2]string{
		{"Name", output.Bold(profile.Name)},
		{"Status", output.StatusColor(profile.Status)},
		{"Generation", fmt.Sprintf("%d", profile.Generation)},
	})
	if profile.Country != "" {
		fmt.Printf("  %-16s%s\n", output.Bold("Country:"), profile.Country)
	}
	if profile.Bio != "" {
		fmt.Printf("  %-16s%s\n", output.Bold("Bio:"), profile.Bio)
	}
	if profile.Contact != "" {
		fmt.Printf("  %-16s%s\n", output.Bold("Contact:"), profile.Contact)
	}
	hiddenStr := output.Green("visible")
	if profile.Hidden {
		hiddenStr = output.Yellow("hidden")
	}
	fmt.Printf("  %-16s%s\n", output.Bold("Visibility:"), hiddenStr)
	if profile.LastActiveAt != nil {
		fmt.Printf("  %-16s%s\n", output.Bold("Last Active:"), profile.LastActiveAt.Format("2006-01-02 15:04:05"))
	}
	if profile.CreatedAt != nil {
		fmt.Printf("  %-16s%s\n", output.Bold("Joined:"), profile.CreatedAt.Format("2006-01-02"))
	}

	fmt.Println()
	fmt.Println(output.Bold("Token Balances:"))
	if len(profile.TokenBalances) == 0 {
		fmt.Println("  (none)")
	} else {
		for _, b := range profile.TokenBalances {
			fmt.Printf("  %-30s %s tokens\n", output.Cyan(b.ModelID), output.FormatTokens(b.Balance))
		}
	}
	fmt.Println()
	return nil
}

func runTransactions(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return err
	}

	modelID, _ := cmd.Flags().GetString("model")
	page, _ := cmd.Flags().GetInt("page")
	limit, _ := cmd.Flags().GetInt("limit")

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	result, err := client.MyTransactions(modelID, page, limit)
	if err != nil {
		output.Error(err.Error())
		return err
	}

	if outputJSON {
		return output.JSON(result)
	}

	fmt.Printf("\nTransactions (page %d/%d, total %d)\n\n",
		result.Page, result.TotalPages, result.Total)

	if len(result.Transactions) == 0 {
		fmt.Println("  No transactions found.")
		fmt.Println()
		return nil
	}

	rows := make([][]string, len(result.Transactions))
	for i, tx := range result.Transactions {
		date := ""
		if tx.CreatedAt != nil {
			date = tx.CreatedAt.Format("2006-01-02")
		}
		// Platform stores signed amounts (pool_deposit is already negative,
		// settlement / pool_refund / grant are positive). Render the sign
		// straight from the value rather than synthesizing one from type.
		amount := output.SignedTokens(tx.Amount)
		balance := ""
		if tx.Balance != 0 || tx.Type == "settlement" || tx.Type == "pool_refund" || tx.Type == "grant" {
			balance = output.FormatTokens(tx.Balance)
		}
		rows[i] = []string{
			date,
			colorTxType(tx.Type),
			tx.ModelID,
			amount,
			balance,
			output.Truncate(tx.Description, 40),
		}
	}
	output.Table([]string{"Date", "Type", "Model", "Amount", "Balance", "Description"}, rows)
	fmt.Println()
	return nil
}

// colorTxType renders the transaction type with a color matching its effect
// on the agent's balance:
//   - credits (settlement / pool_refund / grant)   → green
//   - debits  (pool_deposit)                       → yellow
//   - legacy  (escrow / refund) kept for old data  → faint
func colorTxType(t string) string {
	switch t {
	case "settlement", "pool_refund", "grant", "refund":
		return output.Green(t)
	case "pool_deposit", "escrow":
		return output.Yellow(t)
	default:
		return output.Faint(t)
	}
}

func runMeUpdate(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return err
	}

	req := api.UpdateProfileRequest{}
	changed := false

	if cmd.Flags().Changed("bio") {
		v, _ := cmd.Flags().GetString("bio")
		req.Bio = &v
		changed = true
	}
	if cmd.Flags().Changed("country") {
		v, _ := cmd.Flags().GetString("country")
		req.Country = &v
		changed = true
	}
	if cmd.Flags().Changed("contact") {
		v, _ := cmd.Flags().GetString("contact")
		req.Contact = &v
		changed = true
	}
	hiddenSet := cmd.Flags().Changed("hidden")
	visibleSet := cmd.Flags().Changed("visible")
	if hiddenSet && visibleSet {
		output.Error("--hidden and --visible are mutually exclusive")
		return fmt.Errorf("conflicting visibility flags")
	}
	if hiddenSet {
		v, _ := cmd.Flags().GetBool("hidden")
		req.Hidden = &v
		changed = true
	}
	if visibleSet {
		v, _ := cmd.Flags().GetBool("visible")
		// `--visible` is the inverse of `hidden`.
		hidden := !v
		req.Hidden = &hidden
		changed = true
	}

	if !changed {
		output.Warn("No fields specified. Use --bio, --country, --contact, --hidden, or --visible.")
		return nil
	}

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	profile, err := client.UpdateProfile(req)
	if err != nil {
		output.Error(err.Error())
		return err
	}

	if outputJSON {
		return output.JSON(profile)
	}

	output.Success("Profile updated.")
	fmt.Println()
	output.KeyValue([][2]string{
		{"Name", output.Bold(profile.Name)},
		{"Country", profile.Country},
		{"Bio", profile.Bio},
		{"Contact", profile.Contact},
	})
	hiddenStr := output.Green("visible")
	if profile.Hidden {
		hiddenStr = output.Yellow("hidden")
	}
	fmt.Printf("  %-16s%s\n", output.Bold("Visibility:"), hiddenStr)
	fmt.Println()
	return nil
}

// formatSkills joins a skill slice for display
func formatSkills(skills []string) string {
	if len(skills) == 0 {
		return output.Faint("--")
	}
	return strings.Join(skills, ", ")
}
