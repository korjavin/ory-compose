// Hydra consent service for the Ory stack.
//
// On every consent request:
//   1. Fetch the consent request from Hydra admin.
//   2. Fetch the Kratos identity referenced by the request's subject.
//   3. Read client.metadata.required_groups; if non-empty, the identity must
//      have at least one matching group, otherwise consent is rejected.
//   4. Copy identity.traits.groups (and email/name) into the id_token and
//      access_token session, then accept consent.
//
// Auto-accept (no UI) because all configured Hydra clients are first-party.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"
)

const (
	defaultPort        = "3001"
	defaultRememberFor = 3600
)

type consentRequest struct {
	Challenge         string   `json:"challenge"`
	Subject           string   `json:"subject"`
	RequestedScope    []string `json:"requested_scope"`
	RequestedAudience []string `json:"requested_access_token_audience"`
	Client            struct {
		ClientID   string                 `json:"client_id"`
		ClientName string                 `json:"client_name"`
		Metadata   map[string]interface{} `json:"metadata"`
	} `json:"client"`
}

type identity struct {
	ID     string `json:"id"`
	Traits struct {
		Email string `json:"email"`
		Name  struct {
			First string `json:"first"`
			Last  string `json:"last"`
		} `json:"name"`
		Groups []string `json:"groups"`
	} `json:"traits"`
}

type redirect struct {
	RedirectTo string `json:"redirect_to"`
}

type server struct {
	hydraAdmin  string
	kratosAdmin string
	httpClient  *http.Client
}

func main() {
	hydra := mustEnv("HYDRA_ADMIN_URL")
	kratos := mustEnv("KRATOS_ADMIN_URL")
	port := getenv("PORT", defaultPort)

	s := &server{
		hydraAdmin:  hydra,
		kratosAdmin: kratos,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/consent", s.handleConsent)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})

	log.Printf("consent service listening on :%s (hydra=%s kratos=%s)", port, hydra, kratos)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}

func (s *server) handleConsent(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	challenge := r.URL.Query().Get("consent_challenge")
	if challenge == "" {
		http.Error(w, "missing consent_challenge", http.StatusBadRequest)
		return
	}

	cr, err := s.fetchConsentRequest(ctx, challenge)
	if err != nil {
		log.Printf("consent: fetch request: %v", err)
		http.Error(w, "consent fetch failed", http.StatusBadGateway)
		return
	}

	ident, err := s.fetchIdentity(ctx, cr.Subject)
	if err != nil {
		log.Printf("consent: fetch identity %s: %v", cr.Subject, err)
		http.Error(w, "identity fetch failed", http.StatusBadGateway)
		return
	}

	required := requiredGroups(cr.Client.Metadata)
	if len(required) > 0 && !overlap(ident.Traits.Groups, required) {
		log.Printf("consent: deny client=%s subject=%s user_groups=%v required=%v",
			cr.Client.ClientID, cr.Subject, ident.Traits.Groups, required)
		redirectTo, err := s.rejectConsent(ctx, challenge, required)
		if err != nil {
			log.Printf("consent: reject: %v", err)
			http.Error(w, "reject failed", http.StatusBadGateway)
			return
		}
		http.Redirect(w, r, redirectTo, http.StatusFound)
		return
	}

	log.Printf("consent: allow client=%s subject=%s groups=%v",
		cr.Client.ClientID, cr.Subject, ident.Traits.Groups)
	redirectTo, err := s.acceptConsent(ctx, challenge, cr, ident)
	if err != nil {
		log.Printf("consent: accept: %v", err)
		http.Error(w, "accept failed", http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, redirectTo, http.StatusFound)
}

func requiredGroups(meta map[string]interface{}) []string {
	if meta == nil {
		return nil
	}
	raw, ok := meta["required_groups"]
	if !ok {
		return nil
	}
	list, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func overlap(a, b []string) bool {
	set := make(map[string]struct{}, len(a))
	for _, x := range a {
		set[x] = struct{}{}
	}
	for _, y := range b {
		if _, ok := set[y]; ok {
			return true
		}
	}
	return false
}

func (s *server) fetchConsentRequest(ctx context.Context, challenge string) (*consentRequest, error) {
	u := fmt.Sprintf("%s/admin/oauth2/auth/requests/consent?consent_challenge=%s",
		s.hydraAdmin, url.QueryEscape(challenge))
	var cr consentRequest
	if err := s.doJSON(ctx, http.MethodGet, u, nil, &cr); err != nil {
		return nil, err
	}
	return &cr, nil
}

func (s *server) fetchIdentity(ctx context.Context, id string) (*identity, error) {
	u := fmt.Sprintf("%s/admin/identities/%s", s.kratosAdmin, url.PathEscape(id))
	var ident identity
	if err := s.doJSON(ctx, http.MethodGet, u, nil, &ident); err != nil {
		return nil, err
	}
	return &ident, nil
}

func (s *server) acceptConsent(ctx context.Context, challenge string, cr *consentRequest, ident *identity) (string, error) {
	body := map[string]interface{}{
		"grant_scope":                 cr.RequestedScope,
		"grant_access_token_audience": cr.RequestedAudience,
		"remember":                    true,
		"remember_for":                defaultRememberFor,
		"session": map[string]interface{}{
			"id_token": map[string]interface{}{
				"email":       ident.Traits.Email,
				"name":        joinName(ident),
				"given_name":  ident.Traits.Name.First,
				"family_name": ident.Traits.Name.Last,
				"groups":      ident.Traits.Groups,
			},
			"access_token": map[string]interface{}{
				"groups": ident.Traits.Groups,
			},
		},
	}
	u := fmt.Sprintf("%s/admin/oauth2/auth/requests/consent/accept?consent_challenge=%s",
		s.hydraAdmin, url.QueryEscape(challenge))
	var r redirect
	if err := s.doJSON(ctx, http.MethodPut, u, body, &r); err != nil {
		return "", err
	}
	return r.RedirectTo, nil
}

func (s *server) rejectConsent(ctx context.Context, challenge string, required []string) (string, error) {
	body := map[string]interface{}{
		"error":             "access_denied",
		"error_description": fmt.Sprintf("user must be a member of one of: %v", required),
		"status_code":       http.StatusForbidden,
	}
	u := fmt.Sprintf("%s/admin/oauth2/auth/requests/consent/reject?consent_challenge=%s",
		s.hydraAdmin, url.QueryEscape(challenge))
	var r redirect
	if err := s.doJSON(ctx, http.MethodPut, u, body, &r); err != nil {
		return "", err
	}
	return r.RedirectTo, nil
}

func joinName(i *identity) string {
	if i.Traits.Name.First == "" && i.Traits.Name.Last == "" {
		return ""
	}
	if i.Traits.Name.Last == "" {
		return i.Traits.Name.First
	}
	if i.Traits.Name.First == "" {
		return i.Traits.Name.Last
	}
	return i.Traits.Name.First + " " + i.Traits.Name.Last
}

func (s *server) doJSON(ctx context.Context, method, u string, in, out interface{}) error {
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

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s %s: %d %s", method, u, resp.StatusCode, string(buf))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatal(errors.New("required env var not set: " + key))
	}
	return v
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
