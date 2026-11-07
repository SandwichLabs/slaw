package cmd

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/workspace"
	"github.com/spf13/cobra"
)

var connCredentialsCmd = &cobra.Command{
	Use:   "credentials",
	Short: "Set, clear or inspect a connection's credentials",
}

var connCredsSetCmd = &cobra.Command{
	Use:   "set <name>",
	Short: "Store credentials in the keychain (prompts without echo; reads stdin when piped)",
	Long: `Ask for the connection's credential fields (user, password; token; key id,
secret) and store them in the keychain, bound to the connection's server. A
blank answer keeps the stored value. When input is piped, one line is read per
field, in the order the prompts would appear.

--sign-in chooses the method for this connection first: keychain (default),
prompt, env:PREFIX, which reads PREFIX_USER, PREFIX_PASSWORD, PREFIX_TOKEN,
PREFIX_KEY_ID and PREFIX_SECRET at connect time and stores nothing, or (S3
only) aws-sso:PROFILE, which signs in with that AWS IAM Identity Center
profile from ~/.aws/config, stores nothing and asks for nothing. Sign in with
"aws sso login --profile PROFILE".`,
	Args:         cobra.ExactArgs(1),
	RunE:         runConnCredsSet,
	SilenceUsage: true,
}

func runConnCredsSet(cmd *cobra.Command, args []string) error {
	ws, err := openWS(cmd)
	if err != nil {
		return err
	}
	defer ws.Close() //nolint:errcheck
	c, ok := findConn(ws, args[0])
	if !ok {
		return fmt.Errorf("unknown connection %q", args[0])
	}
	if !c.ConnectionDef.NeedsCredential() {
		return fmt.Errorf("%s connections have no credentials", c.Type)
	}
	if m := flagString(cmd, "sign-in"); m != "" {
		err := ws.SetSignIn(c.Name, m)
		if errors.Is(err, workspace.ErrRebindNeedsConfirm) {
			if yes, _ := cmd.Flags().GetBool("confirm-rebind"); !yes {
				return fmt.Errorf("%w (run again with --confirm-rebind if %s should use %s for its new server)", err, c.Name, m)
			}
			err = ws.RebindSignIn(c.Name, m)
		}
		if err != nil {
			return err
		}
		c, _ = findConn(ws, args[0])
	}
	out := cmd.OutOrStdout()
	if kind, prefix, _ := secrets.ParseMethod(c.SignIn); kind == "env" {
		fmt.Fprintf(out, "%s signs in from the environment: %s. Nothing is stored.\n", c.Name, strings.Join(secrets.EnvVarNames(prefix), ", "))
		return nil
	} else if kind == secrets.MethodAWSSSO {
		printSSONote(out, c.Name, prefix)
		return nil
	}
	cred, err := promptCredentials(cmd.InOrStdin(), cmd.ErrOrStderr(), c.Type, c.Options)
	if err != nil {
		return err
	}
	if err := ws.SetCredentials(cmdCtx(cmd), c.Name, cred, false); err != nil {
		return errors.New(cliRedactor.Redact(err.Error()))
	}
	fmt.Fprintf(out, "Credentials for %s saved (%s).\n", c.Name, c.SignIn)
	return nil
}

var connCredsClearCmd = &cobra.Command{
	Use:          "clear <name>",
	Short:        "Remove the saved credentials of a connection",
	Args:         cobra.ExactArgs(1),
	RunE:         runConnCredsClear,
	SilenceUsage: true,
}

func runConnCredsClear(cmd *cobra.Command, args []string) error {
	ws, err := openWS(cmd)
	if err != nil {
		return err
	}
	defer ws.Close() //nolint:errcheck
	if err := ws.ClearCredentials(cmdCtx(cmd), args[0]); err != nil {
		return errors.New(cliRedactor.Redact(err.Error()))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Cleared the saved credentials of %s.\n", args[0])
	return nil
}

var connCredsStatusCmd = &cobra.Command{
	Use:          "status <name>",
	Short:        "Say whether credentials are set (never what they are)",
	Args:         cobra.ExactArgs(1),
	RunE:         runConnCredsStatus,
	SilenceUsage: true,
}

func runConnCredsStatus(cmd *cobra.Command, args []string) error {
	ws, err := openWS(cmd)
	if err != nil {
		return err
	}
	defer ws.Close() //nolint:errcheck
	st, err := ws.CredentialStatus(cmdCtx(cmd), args[0])
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "method:        %s\n", st.Method)
	if st.Username != "" {
		fmt.Fprintf(out, "user:          %s\n", st.Username)
	}
	if st.KeyID != "" {
		fmt.Fprintf(out, "key id:        %s\n", st.KeyID)
	}
	fmt.Fprintf(out, "secret set:    %t\n", st.SecretSet)
	fmt.Fprintf(out, "server match:  %t\n", st.FingerprintMatches)
	fmt.Fprintf(out, "available:     %t\n", st.Available)
	if st.Detail != "" {
		fmt.Fprintf(out, "note:          %s\n", st.Detail)
	}
	if st.Profile != "" {
		fmt.Fprintf(out, "profile:       %s\n", st.Profile)
		fmt.Fprintf(out, "signed in:     %t\n", st.SignedIn)
		if !st.SignedIn {
			fmt.Fprintf(out, "sign in:       slaw conn credentials sign-in %s   (or: aws sso login --profile %s)\n", args[0], st.Profile)
		}
		if st.ExpiresAt != nil {
			fmt.Fprintf(out, "session until: %s\n", st.ExpiresAt.Local().Format(time.RFC3339))
		}
	}
	names := make([]string, 0, len(st.Env))
	for _, e := range st.Env {
		names = append(names, fmt.Sprintf("%s=%t", e.Name, e.Set))
	}
	sort.Strings(names)
	if len(names) > 0 {
		fmt.Fprintf(out, "env set:       %s\n", strings.Join(names, " "))
	}
	return nil
}

// printSSONote says what an aws-sso sign-in means: nothing is stored, and how to
// sign in. It never names an account, role or start URL.
func printSSONote(out io.Writer, name, profile string) {
	fmt.Fprintf(out, "%s signs in with your AWS profile %s. Nothing is stored. If the session has ended, run: slaw conn credentials sign-in %s (or: aws sso login --profile %s)\n", name, profile, name, profile)
}
