package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"
	"golang.org/x/crypto/bcrypt"
)

// SAML 2.0 Service Provider (F18).
//
// This is the SAML alternative to the existing OIDC SSO for enterprises whose
// IdP speaks SAML (ADFS, Okta SAML apps, Azure AD SAML). It uses the vetted
// crewjam/saml library for the security-critical parts — XML signature
// verification, audience/condition checks — and then plugs the verified
// identity into PulseTrace's own JWT session, exactly like the OIDC callback:
// look the user up (or auto-provision as viewer), issue a session token, and
// hand the browser back to the app.
//
// It is disabled unless the IdP metadata is configured (SAML_IDP_METADATA_URL
// or SAML_IDP_METADATA_XML), so a deployment without SAML is unaffected.

const samlRequestCookie = "saml_authn_request_id"

// SAMLHandler lazily builds a crewjam ServiceProvider from env on first use and
// serves the metadata, login-redirect, and ACS endpoints.
type SAMLHandler struct {
	db       *sql.DB
	sessions *SessionStore

	once sync.Once
	sp   *saml.ServiceProvider
	err  error
}

func NewSAMLHandler(db *sql.DB, sessions *SessionStore) *SAMLHandler {
	return &SAMLHandler{db: db, sessions: sessions}
}

// Configured reports whether SAML IdP metadata is present in the environment.
func (h *SAMLHandler) Configured() bool {
	return strings.TrimSpace(os.Getenv("SAML_IDP_METADATA_URL")) != "" ||
		strings.TrimSpace(os.Getenv("SAML_IDP_METADATA_XML")) != ""
}

// provider builds (once) the ServiceProvider: SP key/cert, our metadata/ACS
// URLs, and the IdP metadata. Any misconfiguration is captured so the handlers
// can return a clean 503 rather than panicking.
func (h *SAMLHandler) provider() (*saml.ServiceProvider, error) {
	h.once.Do(func() {
		if !h.Configured() {
			h.err = fmt.Errorf("SAML is not configured")
			return
		}
		root := strings.TrimRight(strings.TrimSpace(os.Getenv("SAML_SP_ROOT_URL")), "/")
		if root == "" {
			root = "http://localhost:8080"
		}
		metaURL, err := url.Parse(root + "/api/v1/auth/saml/metadata")
		if err != nil {
			h.err = err
			return
		}
		acsURL, err := url.Parse(root + "/api/v1/auth/saml/acs")
		if err != nil {
			h.err = err
			return
		}

		key, cert, err := loadOrGenerateSPKeypair()
		if err != nil {
			h.err = err
			return
		}

		idpMeta, err := loadIDPMetadata()
		if err != nil {
			h.err = err
			return
		}

		entityID := strings.TrimSpace(os.Getenv("SAML_SP_ENTITY_ID"))
		if entityID == "" {
			entityID = metaURL.String()
		}

		h.sp = &saml.ServiceProvider{
			EntityID:    entityID,
			Key:         key,
			Certificate: cert,
			MetadataURL: *metaURL,
			AcsURL:      *acsURL,
			IDPMetadata: idpMeta,
		}
	})
	return h.sp, h.err
}

// Metadata handles GET /api/v1/auth/saml/metadata — the SP metadata XML an IdP
// admin uploads to register PulseTrace as a service provider.
func (h *SAMLHandler) Metadata(w http.ResponseWriter, r *http.Request) {
	sp, err := h.provider()
	if err != nil {
		http.Error(w, "SAML is not configured on this server", http.StatusServiceUnavailable)
		return
	}
	out, err := xml.MarshalIndent(sp.Metadata(), "", "  ")
	if err != nil {
		http.Error(w, "failed to render metadata", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/samlmetadata+xml")
	_, _ = w.Write(out)
}

// Login handles GET /api/v1/auth/saml/login — builds a SAML AuthnRequest and
// redirects the browser to the IdP (HTTP-Redirect binding).
func (h *SAMLHandler) Login(w http.ResponseWriter, r *http.Request) {
	sp, err := h.provider()
	if err != nil {
		http.Error(w, "SAML is not configured on this server", http.StatusServiceUnavailable)
		return
	}
	authReq, err := sp.MakeAuthenticationRequest(
		sp.GetSSOBindingLocation(saml.HTTPRedirectBinding),
		saml.HTTPRedirectBinding, saml.HTTPPostBinding)
	if err != nil {
		http.Error(w, "failed to build SAML request", http.StatusInternalServerError)
		return
	}

	// Remember our request id so ParseResponse can bind the IdP's response to
	// this request (InResponseTo), defeating stray/replayed assertions. The
	// cookie is short-lived, HttpOnly, and SameSite-Lax so it survives the IdP
	// redirect round-trip.
	http.SetCookie(w, &http.Cookie{
		Name:     samlRequestCookie,
		Value:    authReq.ID,
		MaxAge:   int((5 * time.Minute).Seconds()),
		HttpOnly: true,
		Secure:   r.URL.Scheme == "https",
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
	})

	redirectURL, err := authReq.Redirect("", sp)
	if err != nil {
		http.Error(w, "failed to build SAML redirect", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, redirectURL.String(), http.StatusFound)
}

// ACS handles POST /api/v1/auth/saml/acs — the Assertion Consumer Service. It
// verifies the IdP's signed response, extracts the identity, provisions/looks
// up the user, and issues a PulseTrace session, then bounces to the frontend.
func (h *SAMLHandler) ACS(w http.ResponseWriter, r *http.Request) {
	sp, err := h.provider()
	if err != nil {
		http.Error(w, "SAML is not configured on this server", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid SAML response", http.StatusBadRequest)
		return
	}

	var possibleRequestIDs []string
	if c, err := r.Cookie(samlRequestCookie); err == nil && c.Value != "" {
		possibleRequestIDs = append(possibleRequestIDs, c.Value)
	}
	// Allow IdP-initiated SSO (no prior AuthnRequest) only if explicitly enabled.
	if strings.EqualFold(os.Getenv("SAML_ALLOW_IDP_INITIATED"), "true") {
		possibleRequestIDs = append(possibleRequestIDs, "")
	}

	assertion, err := sp.ParseResponse(r, possibleRequestIDs)
	if err != nil {
		// crewjam wraps the underlying reason in InvalidResponseError.
		log.Printf("saml: assertion rejected: %v", err)
		http.Error(w, "SAML assertion could not be verified", http.StatusUnauthorized)
		return
	}

	email := extractSAMLIdentity(assertion)
	if email == "" {
		http.Error(w, "SAML assertion did not contain an identity", http.StatusBadRequest)
		return
	}

	role, tenantID, tier, err := h.resolveOrProvision(r.Context(), email)
	if err != nil {
		http.Error(w, "failed to provision SAML user", http.StatusInternalServerError)
		return
	}

	// Clear the one-shot request cookie.
	http.SetCookie(w, &http.Cookie{Name: samlRequestCookie, Value: "", MaxAge: -1, Path: "/"})

	jti := createSession(r.Context(), h.db, email, tenantID, r.UserAgent(), clientIP(r))
	token, err := issueSessionToken(email, role, tenantID, tier, jti)
	if err != nil {
		http.Error(w, "failed to issue session", http.StatusInternalServerError)
		return
	}
	WriteAudit(h.db, email, "login", "saml_sso", email, nil, map[string]string{"tenant": tenantID})

	// Hand the browser back to the app with the token, mirroring the OIDC flow
	// (the Settings page reads ?token= and stores it).
	appBase := strings.TrimRight(strings.TrimSpace(os.Getenv("APP_BASE_URL")), "/")
	if appBase == "" {
		appBase = "http://localhost:3000"
	}
	http.Redirect(w, r, appBase+"/settings?token="+url.QueryEscape(token), http.StatusFound)
}

// resolveOrProvision returns the role/tenant/tier for a SAML-authenticated
// email, auto-provisioning a viewer in the default tenant on first sign-in —
// the same posture as the OIDC callback. A deactivated account is refused.
func (h *SAMLHandler) resolveOrProvision(ctx context.Context, email string) (role, tenantID, tier string, err error) {
	var active bool
	err = h.db.QueryRowContext(ctx,
		"SELECT role, tenant_id, tier, COALESCE(active, true) FROM users WHERE username = $1", email).
		Scan(&role, &tenantID, &tier, &active)
	if err == sql.ErrNoRows {
		role, tenantID, tier = "viewer", defaultTenantID, defaultTenantTier
		dummy := make([]byte, 24)
		_, _ = rand.Read(dummy)
		hash, herr := bcrypt.GenerateFromPassword(dummy, bcrypt.DefaultCost)
		if herr != nil {
			return "", "", "", herr
		}
		if _, ierr := h.db.ExecContext(ctx,
			"INSERT INTO users (username, password_hash, role, tenant_id, tier) VALUES ($1, $2, $3, $4, $5)",
			email, string(hash), role, tenantID, tier); ierr != nil {
			return "", "", "", ierr
		}
		return role, tenantID, tier, nil
	}
	if err != nil {
		return "", "", "", err
	}
	if !active {
		return "", "", "", fmt.Errorf("account deactivated")
	}
	return role, tenantID, tier, nil
}

// ── Helpers (pure, unit-tested) ────────────────────────────────────────────

// extractSAMLIdentity pulls the best identity from a verified assertion: an
// email/emailAddress/NameID/upn attribute, falling back to the Subject NameID.
func extractSAMLIdentity(a *saml.Assertion) string {
	if a == nil {
		return ""
	}
	// Preferred attribute names, in order.
	wanted := []string{
		"email", "emailaddress", "urn:oid:0.9.2342.19200300.100.1.3",
		"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress",
		"upn", "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/upn",
	}
	for _, stmt := range a.AttributeStatements {
		for _, attr := range stmt.Attributes {
			name := strings.ToLower(strings.TrimSpace(attr.Name))
			friendly := strings.ToLower(strings.TrimSpace(attr.FriendlyName))
			for _, w := range wanted {
				if name == w || friendly == w {
					if v := firstAttrValue(attr.Values); v != "" {
						return v
					}
				}
			}
		}
	}
	if a.Subject != nil && a.Subject.NameID != nil {
		return strings.TrimSpace(a.Subject.NameID.Value)
	}
	return ""
}

func firstAttrValue(vals []saml.AttributeValue) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v.Value); s != "" {
			return s
		}
	}
	return ""
}

// loadIDPMetadata reads the IdP metadata from a URL or inline XML env var.
func loadIDPMetadata() (*saml.EntityDescriptor, error) {
	if raw := strings.TrimSpace(os.Getenv("SAML_IDP_METADATA_XML")); raw != "" {
		return samlsp.ParseMetadata([]byte(raw))
	}
	metaURLStr := strings.TrimSpace(os.Getenv("SAML_IDP_METADATA_URL"))
	u, err := url.Parse(metaURLStr)
	if err != nil {
		return nil, fmt.Errorf("invalid SAML_IDP_METADATA_URL: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return samlsp.FetchMetadata(ctx, http.DefaultClient, *u)
}

// loadOrGenerateSPKeypair returns the SP signing key/cert from PEM env vars, or
// generates an ephemeral self-signed pair if none are set. Ephemeral keys are
// fine for signing AuthnRequests in dev; production should pin SAML_SP_KEY/CERT
// so the SP metadata's certificate is stable across restarts.
func loadOrGenerateSPKeypair() (*rsa.PrivateKey, *x509.Certificate, error) {
	keyPEM := strings.TrimSpace(os.Getenv("SAML_SP_KEY"))
	certPEM := strings.TrimSpace(os.Getenv("SAML_SP_CERT"))
	if keyPEM != "" && certPEM != "" {
		return parseKeypairPEM(keyPEM, certPEM)
	}
	return generateSelfSignedKeypair()
}

func parseKeypairPEM(keyPEM, certPEM string) (*rsa.PrivateKey, *x509.Certificate, error) {
	kb, _ := pem.Decode([]byte(keyPEM))
	if kb == nil {
		return nil, nil, fmt.Errorf("SAML_SP_KEY is not valid PEM")
	}
	key, err := x509.ParsePKCS1PrivateKey(kb.Bytes)
	if err != nil {
		// Try PKCS8.
		k8, e8 := x509.ParsePKCS8PrivateKey(kb.Bytes)
		if e8 != nil {
			return nil, nil, fmt.Errorf("SAML_SP_KEY: %w", err)
		}
		rk, ok := k8.(*rsa.PrivateKey)
		if !ok {
			return nil, nil, fmt.Errorf("SAML_SP_KEY must be an RSA key")
		}
		key = rk
	}
	cb, _ := pem.Decode([]byte(certPEM))
	if cb == nil {
		return nil, nil, fmt.Errorf("SAML_SP_CERT is not valid PEM")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("SAML_SP_CERT: %w", err)
	}
	return key, cert, nil
}

func generateSelfSignedKeypair() (*rsa.PrivateKey, *x509.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "pulsetrace-saml-sp"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(5, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	return key, cert, nil
}
