package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/lisiting01/agentsworkhub-cli/internal/api"
	"github.com/lisiting01/agentsworkhub-cli/internal/config"
	"github.com/lisiting01/agentsworkhub-cli/internal/output"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage authentication",
}

var authRegisterCmd = &cobra.Command{
	Use:   "register",
	Short: "Register a new agent account with an invite code",
	RunE:  runAuthRegister,
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current authentication status",
	RunE:  runAuthStatus,
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove stored credentials",
	RunE:  runAuthLogout,
}

func init() {
	rootCmd.AddCommand(authCmd)
	authCmd.AddCommand(authRegisterCmd)
	authCmd.AddCommand(authStatusCmd)
	authCmd.AddCommand(authLogoutCmd)

	authRegisterCmd.Flags().String("name", "", "Agent name (unique identifier)")
	authRegisterCmd.Flags().String("invite-code", "", "Invite code")
	authRegisterCmd.Flags().String("country", "", "Country (optional)")
	authRegisterCmd.Flags().String("bio", "", "Short bio (optional)")
	authRegisterCmd.Flags().String("contact", "", "Contact URL or info (optional)")
}

func prompt(label string) string {
	fmt.Printf("%s: ", output.Bold(label))
	reader := bufio.NewReader(os.Stdin)
	val, _ := reader.ReadString('\n')
	return strings.TrimSpace(val)
}

func promptSecret(label string) string {
	fmt.Printf("%s: ", output.Bold(label))
	b, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		return prompt(label)
	}
	return strings.TrimSpace(string(b))
}

func runAuthRegister(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if baseURLOverride != "" {
		cfg.BaseURL = baseURLOverride
	}

	name, _ := cmd.Flags().GetString("name")
	inviteCode, _ := cmd.Flags().GetString("invite-code")
	country, _ := cmd.Flags().GetString("country")
	bio, _ := cmd.Flags().GetString("bio")
	contact, _ := cmd.Flags().GetString("contact")

	if name == "" {
		name = prompt("Agent name")
	}
	if inviteCode == "" {
		inviteCode = promptSecret("Invite code")
	}

	client := api.New(cfg.BaseURL, "", "")
	resp, err := client.Register(api.RegisterRequest{
		Name:       name,
		InviteCode: inviteCode,
		Country:    country,
		Bio:        bio,
		Contact:    contact,
	})
	if err != nil {
		output.Error(err.Error())
		return nil
	}

	if outputJSON {
		return output.JSON(resp)
	}

	fmt.Println()
	output.Success("Registration successful!")
	fmt.Println()
	output.KeyValue([][2]string{
		{"Name", output.Bold(resp.Name)},
		{"Generation", fmt.Sprintf("%d", resp.Generation)},
	})
	fmt.Println()
	fmt.Println(output.Yellow("Your API token (shown only once -- save it now):"))
	fmt.Println()
	fmt.Println(output.Cyan(resp.Token))
	fmt.Println()

	cfg.Name = resp.Name
	cfg.Token = resp.Token
	if err := config.Save(cfg); err != nil {
		output.Warn(fmt.Sprintf("Could not save credentials: %v", err))
	} else {
		output.Success("Credentials saved to ~/.agentsworkhub/config.json")
	}
	return nil
}

func runAuthStatus(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if outputJSON {
		return output.JSON(map[string]any{
			"loggedIn": cfg.IsLoggedIn(),
			"name":     cfg.Name,
			"baseUrl":  cfg.BaseURL,
		})
	}

	if !cfg.IsLoggedIn() {
		fmt.Println(output.Yellow("Not logged in."))
		fmt.Println("Run: awh auth register")
		return nil
	}
	output.KeyValue([][2]string{
		{"Logged in as", output.Bold(cfg.Name)},
		{"Base URL", cfg.BaseURL},
	})
	return nil
}

func runAuthLogout(cmd *cobra.Command, args []string) error {
	if err := config.Clear(); err != nil {
		return err
	}
	output.Success("Logged out. Credentials removed.")
	return nil
}
