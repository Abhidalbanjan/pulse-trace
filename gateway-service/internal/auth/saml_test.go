package auth

import (
	"testing"

	"github.com/crewjam/saml"
)

func TestExtractSAMLIdentity_FromEmailAttribute(t *testing.T) {
	a := &saml.Assertion{
		AttributeStatements: []saml.AttributeStatement{{
			Attributes: []saml.Attribute{{
				Name:   "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress",
				Values: []saml.AttributeValue{{Value: "alice@example.com"}},
			}},
		}},
	}
	if got := extractSAMLIdentity(a); got != "alice@example.com" {
		t.Fatalf("expected email from attribute, got %q", got)
	}
}

func TestExtractSAMLIdentity_FriendlyName(t *testing.T) {
	a := &saml.Assertion{
		AttributeStatements: []saml.AttributeStatement{{
			Attributes: []saml.Attribute{{
				FriendlyName: "email",
				Name:         "urn:oid:0.9.2342.19200300.100.1.3",
				Values:       []saml.AttributeValue{{Value: "bob@corp.com"}},
			}},
		}},
	}
	if got := extractSAMLIdentity(a); got != "bob@corp.com" {
		t.Fatalf("expected email via friendlyName/oid, got %q", got)
	}
}

func TestExtractSAMLIdentity_FallsBackToNameID(t *testing.T) {
	a := &saml.Assertion{Subject: &saml.Subject{NameID: &saml.NameID{Value: "carol@x.io"}}}
	if got := extractSAMLIdentity(a); got != "carol@x.io" {
		t.Fatalf("expected NameID fallback, got %q", got)
	}
}

func TestExtractSAMLIdentity_EmptyWhenNothing(t *testing.T) {
	if got := extractSAMLIdentity(&saml.Assertion{}); got != "" {
		t.Fatalf("expected empty identity, got %q", got)
	}
	if got := extractSAMLIdentity(nil); got != "" {
		t.Fatalf("expected empty for nil, got %q", got)
	}
}

func TestGenerateSelfSignedKeypair(t *testing.T) {
	key, cert, err := generateSelfSignedKeypair()
	if err != nil {
		t.Fatal(err)
	}
	if key == nil || cert == nil {
		t.Fatal("expected a usable keypair")
	}
	if cert.Subject.CommonName != "pulsetrace-saml-sp" {
		t.Fatalf("unexpected cert subject: %s", cert.Subject.CommonName)
	}
}

func TestSAMLHandler_DisabledWithoutConfig(t *testing.T) {
	t.Setenv("SAML_IDP_METADATA_URL", "")
	t.Setenv("SAML_IDP_METADATA_XML", "")
	h := NewSAMLHandler(nil, nil)
	if h.Configured() {
		t.Fatal("SAML must report unconfigured with no IdP metadata")
	}
}
