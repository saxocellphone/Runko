// `runko auth` - signup/login/status/logout + the git credential-helper
// protocol (§6.10, §12.7, §15.1). Command wiring only; the mechanics live
// in auth.go and gitauth.go.
package main

import (
	"bufio"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"

	"github.com/saxocellphone/runko/internal/clierr"
)

func newAuthCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "auth",
		Short:   "Sign up, sign in, and manage the stored credential",
		GroupID: "start",
		Long: `First contact is one command: signup registers the account, creates or
joins the org, and stores the credential - signup IS login (§6.10).
Every control-plane command then needs no --runkod-url/--token flags.`,
		Example: `  runko auth signup --runkod-url https://<host> --name <you> --org <org> --create
  runko auth login --runkod-url https://<host>/o/<org> --name <you>
  runko auth status`,
		Args: cobra.ArbitraryArgs,
		RunE: groupRunE,
	}
	cmd.AddCommand(newAuthSignupCmd(a), newAuthLoginCmd(a), newAuthStatusCmd(), newAuthLogoutCmd(), newAuthGitCredentialCmd())
	return cmd
}

// newAuthSignupCmd - first contact, CLI-first (§6.10): one command
// registers the account, creates or joins the org, and stores the
// credential - signup IS login, so nothing downstream needs auth flags.
func newAuthSignupCmd(a *app) *cobra.Command {
	var (
		name, password, org, code, email string
		create, join                     bool
	)
	cmd := &cobra.Command{
		Use:   "signup --runkod-url <host> --name <you> --org <org> --create|--join",
		Short: "Register an account and create or join an org",
		Long: `Registers the account against the control plane's HOST root (signup is
served by the hub, not an /o/<org> mount) and on success stores the
credential ALREADY pointed at the created/joined org's mount. The
password prompts hidden when --password is omitted; --email is
optional and never prompts.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.runkodURL == "" || name == "" || org == "" || create == join {
				return &clierr.Error{
					Code: "missing_field", Field: "signup",
					Message:    "auth signup needs --runkod-url, --name, --org, and exactly one of --create/--join",
					Suggestion: "runko auth signup --runkod-url https://<host> --name <you> --org <org> --create",
				}
			}
			orgMode := "join"
			if create {
				orgMode = "create"
			}
			_, err := AuthSignup(cmd.Context(), http.DefaultClient, a.runkodURL, name, password, org, orgMode, code, email, bufio.NewReader(os.Stdin), os.Stdout)
			return err
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&name, "name", "", "the account name to register")
	fl.StringVar(&password, "password", "", "password (min 8 chars); omit to be prompted securely (input hidden)")
	fl.StringVar(&org, "org", "", "the org to create or join - every account belongs to one")
	fl.BoolVar(&create, "create", false, "create --org as a new org; you become its admin")
	fl.BoolVar(&join, "join", false, "join --org, an existing org, as a member")
	fl.StringVar(&code, "invite-code", "", "invite code, if this control plane requires one to sign up")
	fl.StringVar(&email, "email", "", "your email address - OPTIONAL, so nothing prompts for it when omitted")
	return cmd
}

func newAuthLoginCmd(a *app) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "login --runkod-url <url>/o/<org> [--name <you>]",
		Short: "Sign in once; store the validated credential",
		Long: `Validates against GET /api/whoami, then stores {url, name?, secret} in
the platform config dir, 0600. With --name the secret is a principal
password (HTTP Basic); without it, a bare bearer token. Every
control-plane command falls back to this stored credential.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.runkodURL == "" {
				return &clierr.Error{
					Code: "missing_url", Field: "runkod-url",
					Message:    "auth login needs --runkod-url (the /o/<org> API mount, not the web path)",
					Suggestion: "runko auth login --runkod-url https://<host>/o/<org> --name <you>",
				}
			}
			_, err := AuthLogin(cmd.Context(), http.DefaultClient, a.runkodURL, name, a.token, bufio.NewReader(os.Stdin), os.Stdout)
			return err
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "your principal name, e.g. alice; omit to store a bare deploy token (anonymous bearer)")
	return cmd
}

func newAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Who am I, against which control plane",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cred, found, err := loadCredential()
			if err != nil {
				return err
			}
			if !found {
				fmt.Println("not logged in (runko auth login --runkod-url <url>)")
				return nil
			}
			who, anonymous, err := whoami(cmd.Context(), http.DefaultClient, cred)
			if err != nil {
				return err
			}
			if anonymous {
				fmt.Printf("%s: logged in anonymously (deploy token)\n", cred.URL)
			} else {
				fmt.Printf("%s: logged in as %s\n", cred.URL, who)
			}
			return nil
		},
	}
}

func newAuthLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Forget the stored credential",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := credentialPath()
			if err != nil {
				return err
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			fmt.Println("logged out")
			return nil
		},
	}
}

// newAuthGitCredentialCmd - git's credential-helper protocol (§12.7):
// workspace stores stamp `credential.helper = !runko auth git-credential`,
// so raw git in any worktree resolves the INVOKING principal's stored
// login. Called by git, not humans; get/store/erase on argv, attributes
// on stdin - hence Hidden.
func newAuthGitCredentialCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "git-credential <get|store|erase>",
		Short:  "Git credential-helper protocol (called by git)",
		Hidden: true,
		Args:   cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 {
				return usageError("usage: runko auth git-credential <get|store|erase> (called by git)")
			}
			return AuthGitCredential(args[0], os.Stdin, os.Stdout)
		},
	}
}
