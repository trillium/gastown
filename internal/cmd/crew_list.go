package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/crew"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// CrewListItem represents a crew worker in list output.
type CrewListItem struct {
	Name       string `json:"name"`
	Rig        string `json:"rig"`
	Branch     string `json:"branch"`
	Path       string `json:"path"`
	HasSession bool   `json:"has_session"`
	GitClean   bool   `json:"git_clean"`
	Machine    string `json:"machine,omitempty"`
}

func runCrewList(cmd *cobra.Command, args []string) error {
	// Accept positional rig argument: gt crew list <rig>
	if len(args) > 0 {
		if crewRig != "" {
			return fmt.Errorf("cannot specify both positional rig argument and --rig flag")
		}
		crewRig = args[0]
	}

	if crewListAll && crewRig != "" {
		return fmt.Errorf("cannot use --all with a rig filter (--rig flag or positional argument)")
	}

	var rigs []*rig.Rig
	if crewListAll {
		allRigs, err := getAllRigs()
		if err != nil {
			return err
		}
		rigs = allRigs
	} else {
		_, r, err := getCrewManager(crewRig)
		if err != nil {
			return err
		}
		rigs = []*rig.Rig{r}
	}

	// Check session and git status for each worker
	t := tmux.NewTmux()
	var items []CrewListItem

	for _, r := range rigs {
		crewGit := git.NewGit(r.Path)
		crewMgr := crew.NewManager(r, crewGit)

		workers, err := crewMgr.List()
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to list crew workers in %s: %v\n", r.Name, err)
			continue
		}

		for _, w := range workers {
			sessionID := crewSessionName(r.Name, w.Name)
			hasSession, _ := t.HasSession(sessionID)

			workerGit := git.NewGit(w.ClonePath)
			gitClean := true
			if status, err := workerGit.Status(); err == nil {
				gitClean = status.Clean
			}

			items = append(items, CrewListItem{
				Name:       w.Name,
				Rig:        r.Name,
				Branch:     w.Branch,
				Path:       w.ClonePath,
				HasSession: hasSession,
				GitClean:   gitClean,
			})
		}
	}

	// Merge remote crew when --all-machines is set
	if crewListAllMachines {
		townRoot, err := workspace.FindFromCwdOrError()
		if err == nil {
			machinesPath := constants.MayorMachinesPath(townRoot)
			machines, loadErr := config.LoadMachinesConfig(machinesPath)
			if loadErr != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to load machines config: %v\n", loadErr)
			} else {
				remoteSessions := listAllRemoteSessions(machines)
				for _, rs := range remoteSessions {
					if rs.Identity.Role != session.RoleCrew {
						continue
					}
					items = append(items, CrewListItem{
						Name:       rs.Identity.Name,
						Rig:        rs.Identity.Rig,
						HasSession: true,
						Machine:    rs.Machine,
					})
				}
			}
		}
	}

	if len(items) == 0 {
		fmt.Println("No crew workspaces found.")
		return nil
	}

	if crewJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(items)
	}

	// Determine if we have any remote crew (controls Machine column display)
	hasRemote := false
	for _, item := range items {
		if item.Machine != "" {
			hasRemote = true
			break
		}
	}

	// Text output
	fmt.Printf("%s\n\n", style.Bold.Render("Crew Workspaces"))
	for _, item := range items {
		status := style.Dim.Render("○")
		if item.HasSession {
			status = style.Bold.Render("●")
		}

		machineSuffix := ""
		if hasRemote {
			m := item.Machine
			if m == "" {
				m = "local"
			}
			machineSuffix = "  " + style.Dim.Render("["+m+"]")
		}

		if item.Machine != "" {
			// Remote crew: limited info (no branch/path/git status)
			fmt.Printf("  %s %s/%s%s\n", status, item.Rig, item.Name, machineSuffix)
		} else {
			gitStatus := style.Dim.Render("clean")
			if !item.GitClean {
				gitStatus = style.Bold.Render("dirty")
			}
			fmt.Printf("  %s %s/%s%s\n", status, item.Rig, item.Name, machineSuffix)
			fmt.Printf("    Branch: %s  Git: %s\n", item.Branch, gitStatus)
			fmt.Printf("    %s\n", style.Dim.Render(item.Path))
		}
	}

	return nil
}
