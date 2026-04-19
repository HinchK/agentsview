// ABOUTME: `session stats` subcommand — window-scoped analytics
// ABOUTME: emitting the v1 SessionStats JSON schema.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/wesm/agentsview/internal/service"
)

func newSessionStatsCommand() *cobra.Command {
	var (
		since, until, agent, timezone, ghToken string
		includeProjects, excludeProjects       []string
	)
	cmd := &cobra.Command{
		Use:          "stats",
		Short:        "Window-scoped session analytics (v1 schema)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, cleanup, err := resolveService(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			if ghToken == "" {
				// Fall back to env; spec: GH_TOKEN or GITHUB_TOKEN.
				ghToken = os.Getenv("GH_TOKEN")
				if ghToken == "" {
					ghToken = os.Getenv("GITHUB_TOKEN")
				}
			}

			stats, err := svc.Stats(cmd.Context(), service.StatsFilter{
				Since:           since,
				Until:           until,
				Agent:           agent,
				IncludeProjects: includeProjects,
				ExcludeProjects: excludeProjects,
				Timezone:        timezone,
				GHToken:         ghToken,
			})
			if err != nil {
				return err
			}
			if outputFormat(cmd) == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(stats)
			}
			return printSessionStatsHuman(cmd.OutOrStdout(), stats)
		},
	}

	f := cmd.Flags()
	f.StringVar(&since, "since", "28d",
		"Start of window (duration like 28d, or YYYY-MM-DD)")
	f.StringVar(&until, "until", "",
		"End of window (YYYY-MM-DD; default: now)")
	f.StringVar(&agent, "agent", "all",
		"Filter by agent (claude, codex, cursor, ... or 'all')")
	f.StringArrayVar(&includeProjects, "include-project", nil,
		"Restrict to these projects (repeatable)")
	f.StringArrayVar(&excludeProjects, "exclude-project", nil,
		"Exclude these projects (repeatable)")
	f.StringVar(&timezone, "timezone", "",
		"Timezone for temporal (default: local system timezone)")
	f.StringVar(&ghToken, "gh-token", "",
		"GitHub token for PR aggregation (falls back to GH_TOKEN/GITHUB_TOKEN env)")
	return cmd
}

// printSessionStatsHuman renders a human-readable summary. Stub for now;
// real formatting happens in Task 19.
func printSessionStatsHuman(w io.Writer, stats *service.SessionStats) error {
	_, err := fmt.Fprintf(w, "SessionStats (schema_version=%d, %d sessions total)\n",
		stats.SchemaVersion, stats.Totals.SessionsAll)
	return err
}
