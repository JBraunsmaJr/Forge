package scheduler

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type OIDCProvider struct {
	Issuer     string
	PrivateKey *rsa.PrivateKey
	PublicKey  *rsa.PublicKey
	KeyID      string
}

type JWKS struct {
	Keys []JWK `json:"keys"`
}

type JWK struct {
	Kty string   `json:"kty"`
	Kid string   `json:"kid"`
	Use string   `json:"use"`
	Alg string   `json:"alg"`
	N   string   `json:"n"`
	E   string   `json:"e"`
	X5c []string `json:"x5c,omitempty"`
}

func NewOIDCProvider(issuer string) (*OIDCProvider, error) {
	keyPEM := os.Getenv("FORGE_OIDC_KEY")
	var priv *rsa.PrivateKey
	var err error

	if keyPEM != "" {
		block, _ := pem.Decode([]byte(keyPEM))
		if block == nil {
			return nil, fmt.Errorf("failed to decode OIDC key PEM")
		}
		priv, err = x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			// Try PKCS8
			key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("failed to parse OIDC private key (PKCS1 or PKCS8): %v", err)
			}
			var ok bool
			priv, ok = key.(*rsa.PrivateKey)
			if !ok {
				return nil, fmt.Errorf("OIDC key is not an RSA private key")
			}
		}
	} else {
		priv, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, fmt.Errorf("failed to generate OIDC key: %v", err)
		}
	}

	return &OIDCProvider{
		Issuer:     issuer,
		PrivateKey: priv,
		PublicKey:  &priv.PublicKey,
		KeyID:      "forge-default-key",
	}, nil
}

func (p *OIDCProvider) HandleConfiguration(w http.ResponseWriter, r *http.Request) {
	config := map[string]any{
		"issuer":                                p.Issuer,
		"jwks_uri":                              fmt.Sprintf("%s/api/v1/jwks", p.Issuer),
		"response_types_supported":              []string{"id_token"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"claims_supported": []string{
			"sub", "iss", "aud", "exp", "iat", "run_id", "job_id", "org_id", "repository",
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(config)
}

func (p *OIDCProvider) HandleJWKS(w http.ResponseWriter, r *http.Request) {
	n := base64.RawURLEncoding.EncodeToString(p.PublicKey.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(p.PublicKey.E)).Bytes())

	jwks := JWKS{
		Keys: []JWK{
			{
				Kty: "RSA",
				Kid: p.KeyID,
				Use: "sig",
				Alg: "RS256",
				N:   n,
				E:   e,
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(jwks)
}

type ForgeClaims struct {
	jwt.RegisteredClaims
	RunID      string `json:"run_id"`
	JobID      string `json:"job_id"`
	OrgID      string `json:"org_id"`
	Repository string `json:"repository"`
}

func (p *OIDCProvider) GenerateToken(runID, jobID, orgID, repository string) (string, error) {
	now := time.Now()
	claims := ForgeClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    p.Issuer,
			Subject:   fmt.Sprintf("repo:%s:run:%s:job:%s", repository, runID, jobID),
			Audience:  jwt.ClaimStrings{"forge.run"},
			ExpiresAt: jwt.NewNumericDate(now.Add(1 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
		},
		RunID:      runID,
		JobID:      jobID,
		OrgID:      orgID,
		Repository: repository,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = p.KeyID

	return token.SignedString(p.PrivateKey)
}
