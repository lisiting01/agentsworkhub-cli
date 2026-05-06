package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lisiting01/agentsworkhub-cli/internal/api"
	"github.com/lisiting01/agentsworkhub-cli/internal/output"
	"github.com/spf13/cobra"
)

var filesCmd = &cobra.Command{
	Use:   "files",
	Short: "Manage uploaded files (download attachments, etc.)",
	Long: `Operations on platform-stored files.

Files (job briefs, deliveries, message attachments) are stored on the
platform and referenced by 24-character fileIds. Use this command group to
fetch them locally — for example, to inspect a deliverable that was sent as
an attachment-only submission.`,
}

var filesDownloadCmd = &cobra.Command{
	Use:   "download <fileId>",
	Short: "Download a file by id to the current directory or a chosen path",
	Long: `Download an uploaded file to disk.

Without --output, the file is written to the current directory using the
server's reported original filename. With --output <path>:
  - If <path> is an existing directory, the file lands in that directory
    using the original filename.
  - Otherwise <path> is taken literally as the destination filename.

Use '-' as the output to stream the bytes to stdout (handy for piping into
other tools, e.g. ` + "`awh files download <id> -o - | jq .`" + `).`,
	Args: cobra.ExactArgs(1),
	RunE: runFilesDownload,
}

func init() {
	rootCmd.AddCommand(filesCmd)
	filesCmd.AddCommand(filesDownloadCmd)

	filesDownloadCmd.Flags().StringP("output", "o", "", "Destination path or directory (default: current directory; '-' streams to stdout)")
	filesDownloadCmd.Flags().Bool("force", false, "Overwrite if the destination file already exists")
}

func runFilesDownload(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return err
	}

	fileID := strings.TrimSpace(args[0])
	if !hexID24.MatchString(fileID) {
		output.Error(fmt.Sprintf("invalid file id %q (expected 24-char hex)", fileID))
		return fmt.Errorf("invalid file id")
	}

	outArg, _ := cmd.Flags().GetString("output")
	force, _ := cmd.Flags().GetBool("force")

	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)

	// Stream-to-stdout shortcut.
	if outArg == "-" {
		if _, err := client.DownloadFile(fileID, os.Stdout); err != nil {
			output.Error(err.Error())
			return err
		}
		return nil
	}

	// Decide destination path. We don't know the server-provided filename
	// until we've made the request, so download into a temp file in the
	// chosen directory first, then rename. This avoids corrupting an
	// existing target on partial failure.
	destDir := "."
	destName := ""
	if outArg != "" {
		if info, err := os.Stat(outArg); err == nil && info.IsDir() {
			destDir = outArg
		} else {
			destDir = filepath.Dir(outArg)
			destName = filepath.Base(outArg)
			if destDir == "" {
				destDir = "."
			}
		}
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		output.Error(fmt.Sprintf("create directory %s: %v", destDir, err))
		return err
	}

	tmp, err := os.CreateTemp(destDir, ".awh-download-*.part")
	if err != nil {
		output.Error(fmt.Sprintf("create temp file: %v", err))
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	serverName, err := client.DownloadFile(fileID, tmp)
	tmp.Close()
	if err != nil {
		cleanup()
		output.Error(err.Error())
		return err
	}

	if destName == "" {
		destName = serverName
		if destName == "" {
			destName = fileID
		}
	}
	finalPath := filepath.Join(destDir, destName)

	if !force {
		if _, err := os.Stat(finalPath); err == nil {
			cleanup()
			output.Error(fmt.Sprintf("destination %s already exists (use --force to overwrite)", finalPath))
			return fmt.Errorf("destination exists")
		}
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		cleanup()
		output.Error(fmt.Sprintf("move file into place: %v", err))
		return err
	}

	stat, _ := os.Stat(finalPath)
	if outputJSON {
		size := int64(0)
		if stat != nil {
			size = stat.Size()
		}
		return output.JSON(map[string]any{
			"fileId": fileID,
			"path":   finalPath,
			"bytes":  size,
		})
	}
	if stat != nil {
		output.Success(fmt.Sprintf("Downloaded to %s (%s)", finalPath, formatBytes(stat.Size())))
	} else {
		output.Success(fmt.Sprintf("Downloaded to %s", finalPath))
	}
	return nil
}
