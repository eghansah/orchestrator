package types

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func selfSignedPEM(t *testing.T, cn string, notBefore, notAfter time.Time) string {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestTrustedCA_Expired(t *testing.T) {
	now := time.Now()
	expired := TrustedCA{
		ID:  "expired",
		PEM: selfSignedPEM(t, "expired-ca", now.Add(-48*time.Hour), now.Add(-24*time.Hour)),
	}
	valid := TrustedCA{
		ID:  "valid",
		PEM: selfSignedPEM(t, "valid-ca", now.Add(-time.Hour), now.Add(365*24*time.Hour)),
	}

	if !expired.Expired() {
		t.Error("expected expired cert to report Expired() == true")
	}
	if valid.Expired() {
		t.Error("expected valid cert to report Expired() == false")
	}
}

func TestTrustBundles_ExcludeExpired(t *testing.T) {
	now := time.Now()
	cas := map[string]TrustedCA{
		"expired-both": {
			ID: "expired-both", Label: "expired-both",
			PEM:                 selfSignedPEM(t, "expired-both", now.Add(-48*time.Hour), now.Add(-24*time.Hour)),
			AppliesToOpenBao:    true,
			AppliesToRegistries: true,
		},
		"valid-bao": {
			ID: "valid-bao", Label: "valid-bao",
			PEM:              selfSignedPEM(t, "valid-bao", now.Add(-time.Hour), now.Add(365*24*time.Hour)),
			AppliesToOpenBao: true,
		},
		"valid-registry": {
			ID: "valid-registry", Label: "valid-registry",
			PEM:                 selfSignedPEM(t, "valid-registry", now.Add(-time.Hour), now.Add(365*24*time.Hour)),
			AppliesToRegistries: true,
		},
	}

	baoBundle := OpenBaoTrustBundle(cas)
	if strings.Contains(baoBundle, cas["expired-both"].PEM) {
		t.Error("OpenBaoTrustBundle should exclude expired CA even though AppliesToOpenBao is true")
	}
	if !strings.Contains(baoBundle, cas["valid-bao"].PEM) {
		t.Error("OpenBaoTrustBundle should include the valid CA flagged AppliesToOpenBao")
	}
	if strings.Contains(baoBundle, cas["valid-registry"].PEM) {
		t.Error("OpenBaoTrustBundle should not include a CA that isn't flagged AppliesToOpenBao")
	}

	registryBundle := RegistryTrustBundle(cas)
	if strings.Contains(registryBundle, cas["expired-both"].PEM) {
		t.Error("RegistryTrustBundle should exclude expired CA even though AppliesToRegistries is true")
	}
	if !strings.Contains(registryBundle, cas["valid-registry"].PEM) {
		t.Error("RegistryTrustBundle should include the valid CA flagged AppliesToRegistries")
	}
}
