// Invite CLI for the Ory stack.
//
// Usage:
//   invite <email> <app1> [app2 ...]
//
// Creates a Kratos identity with traits.email=<email> and
// traits.groups=[<app1>-users, <app2>-users, ...], then generates a Kratos
// recovery link (default 1h expiry) and prints it.
//
// Send the printed link to the user. Clicking the link opens a Kratos
// session and drops them on /settings, where they can pick whichever auth
// methods (Google, GitHub, GitLab, passkey, password, ...) they want to
// link to this identity.
//
// Required env vars:
//   KRATOS_ADMIN_URL   e.g. http://kratos:4434  (when run inside the docker
//                     network) or http://localhost:4434 (via SSH tunnel).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

type createIdentityReq struct {
	SchemaID string `json:"schema_id"`
	Traits   traits `json:"traits"`
}

type traits struct {
	Email  string   `json:"email"`
	Name   nameT    `json:"name,omitempty"`
	Groups []string `json:"groups"`
}

type nameT struct {
	First string `json:"first,omitempty"`
	Last  string `json:"last,omitempty"`
}

type identityResp struct {
	ID     string `json:"id"`
	Traits traits `json:"traits"`
}

type recoveryReq struct {
	IdentityID string `json:"identity_id"`
	ExpiresIn  string `json:"expires_in,omitempty"`
}

type recoveryResp struct {
	RecoveryLink string    `json:"recovery_link"`
	RecoveryCode string    `json:"recovery_code"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func main() {
	log.SetFlags(0)

	var (
		kratosAdmin = flag.String("kratos-admin", os.Getenv("KRATOS_ADMIN_URL"), "Kratos admin URL")
		expiresIn   = flag.String("expires-in", getenv("INVITE_EXPIRES_IN", "1h"), "Invite link lifespan (1h, 24h, ...)")
		firstName   = flag.String("first", "", "Optional first name")
		lastName    = flag.String("last", "", "Optional last name")
		extraGroups = flag.String("extra-groups", "", "Comma-separated extra groups (added in addition to <app>-users)")
	)
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: invite [flags] <email> <app1> [app2 ...]")
		fmt.Fprintln(os.Stderr, "\nEach <appN> becomes the group `<appN>-users`. Each Hydra OAuth2 client")
		fmt.Fprintln(os.Stderr, "should be created with metadata.required_groups including its <app>-users.")
		fmt.Fprintln(os.Stderr)
		flag.PrintDefaults()
	}
	reorderArgsForFlags()
	flag.Parse()

	if flag.NArg() < 2 {
		flag.Usage()
		os.Exit(2)
	}
	if *kratosAdmin == "" {
		log.Fatal("KRATOS_ADMIN_URL not set and --kratos-admin not given")
	}

	email := flag.Arg(0)
	apps := flag.Args()[1:]

	groups := make([]string, 0, len(apps)+4)
	for _, app := range apps {
		groups = append(groups, app+"-users")
	}
	if *extraGroups != "" {
		for _, g := range strings.Split(*extraGroups, ",") {
			if g = strings.TrimSpace(g); g != "" {
				groups = append(groups, g)
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := &http.Client{Timeout: 10 * time.Second}
	admin := strings.TrimRight(*kratosAdmin, "/")

	ident, err := createIdentity(ctx, client, admin, createIdentityReq{
		SchemaID: "default",
		Traits: traits{
			Email:  email,
			Name:   nameT{First: *firstName, Last: *lastName},
			Groups: groups,
		},
	})
	if err != nil {
		log.Fatalf("create identity: %v", err)
	}

	link, err := createRecoveryLink(ctx, client, admin, recoveryReq{
		IdentityID: ident.ID,
		ExpiresIn:  *expiresIn,
	})
	if err != nil {
		log.Fatalf("recovery link (identity %s already created): %v", ident.ID, err)
	}

	// Kratos returns the recovery flow URL and the code separately. The
	// Login UI's /recovery page auto-submits when ?code=... is appended,
	// so we hand the user a single one-click URL.
	oneClick := link.RecoveryLink
	if link.RecoveryCode != "" {
		sep := "?"
		if strings.Contains(oneClick, "?") {
			sep = "&"
		}
		oneClick = oneClick + sep + "code=" + link.RecoveryCode
	}

	fmt.Println()
	fmt.Println("=== Invitation created ===")
	fmt.Printf("Email:       %s\n", email)
	fmt.Printf("Groups:      %s\n", strings.Join(groups, ", "))
	fmt.Printf("Identity ID: %s\n", ident.ID)
	fmt.Printf("Expires:     %s (in %s)\n", link.ExpiresAt.Format(time.RFC3339), *expiresIn)
	fmt.Println()
	fmt.Println("Send this link to the user (one-click, code is pre-filled):")
	fmt.Println(oneClick)
	if link.RecoveryCode != "" {
		fmt.Println()
		fmt.Printf("Fallback if the page asks for a code manually: %s\n", link.RecoveryCode)
	}
	fmt.Println()
}

// reorderArgsForFlags moves any -flag/--flag (and its value) to the front of
// os.Args before flag.Parse runs. Go's stdlib `flag` stops parsing flags
// once it sees the first non-flag arg, which surprises users who write
// `invite <email> <app> --extra-groups admins`. Every flag in this CLI takes
// exactly one value, so the rule is unambiguous.
func reorderArgsForFlags() {
	known := map[string]struct{}{
		"-kratos-admin": {}, "--kratos-admin": {},
		"-expires-in": {}, "--expires-in": {},
		"-first": {}, "--first": {},
		"-last": {}, "--last": {},
		"-extra-groups": {}, "--extra-groups": {},
	}
	var flags, rest []string
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		if _, ok := known[a]; ok && i+1 < len(args) {
			flags = append(flags, a, args[i+1])
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			// --flag=value form or short flags / unknown — leave intact
			flags = append(flags, a)
			continue
		}
		rest = append(rest, a)
	}
	os.Args = append([]string{os.Args[0]}, append(flags, rest...)...)
}

func createIdentity(ctx context.Context, c *http.Client, admin string, in createIdentityReq) (*identityResp, error) {
	var out identityResp
	if err := doJSON(ctx, c, http.MethodPost, admin+"/admin/identities", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func createRecoveryLink(ctx context.Context, c *http.Client, admin string, in recoveryReq) (*recoveryResp, error) {
	var out recoveryResp
	// /admin/recovery/code uses the `code` strategy and works with
	// methods.code.enabled=true (which we keep on for the recovery flow even
	// when code-based login is disabled). The deprecated /admin/recovery/link
	// requires methods.link.enabled=true — which we keep off because it adds
	// a passwordless-login attack surface we don't want.
	if err := doJSON(ctx, c, http.MethodPost, admin+"/admin/recovery/code", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func doJSON(ctx context.Context, c *http.Client, method, u string, in, out interface{}) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return errors.New(fmt.Sprintf("%s %s: %d %s", method, u, resp.StatusCode, strings.TrimSpace(string(buf))))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
