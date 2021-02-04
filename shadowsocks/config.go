// Copyright 2021 The Outline Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package shadowsocks

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/eycorsican/go-tun2socks/common/log"
)

// ProxyConfig represents a Shadowsocks proxy configuration.
type ProxyConfig struct {
	ID         string
	Host       string `json:"server"`
	Port       int    `json:"server_port"`
	Password   string
	Cipher     string `json:"method"`
	Name       string `json:"remarks,omitempty"`
	Plugin     string `json:"plugin,omitempty"`
	PluginOpts string `json:"plugin_opts,omitempty"`
}

// OnlineConfigRequest encapsulates a request to an online config server.
type OnlineConfigRequest struct {
	// URL is the HTTPs endpoint of an online config server.
	URL string
	// Method is the HTTP method to use in the request.
	Method string
	// TrustedCertFingerprint is the sha256 hash of the online config server's
	// TLS certificate.
	TrustedCertFingerprint []byte
}

// OnlineConfigResponse encapsulates a response from an online config server.
type OnlineConfigResponse struct {
	// OnlineConfig is the parsed server response.
	OnlineConfig OnlineConfig
	// HTTPStatusCode is the HTTP status code of the response.
	HTTPStatusCode int
	// RedirectURL is the Location header of a HTTP redirect response.
	RedirectURL string
}

// OnlineConfig represents a SIP008 response from an online config server.
type OnlineConfig struct {
	Proxies []ProxyConfig `json:"servers"`
	Version int
}

// FetchOnlineConfig retrieves Shadowsocks proxy configurations per SIP008:
// https://github.com/shadowsocks/shadowsocks-org/wiki/SIP008-Online-Configuration-Delivery
//
// Pins the trusted certificate when req.TrustedCertFingerprint is non-empty.
// Sets the response's RedirectURL when the status code is a redirect.
// Returns an error if req.URL is a non-HTTPS URL, if there is a connection
// error to the server, or if parsing the configuration fails.
func FetchOnlineConfig(req OnlineConfigRequest) (*OnlineConfigResponse, error) {
	httpreq, err := http.NewRequest(req.Method, req.URL, nil)
	if err != nil {
		return nil, err
	}
	if httpreq.URL.Scheme != "https" {
		return nil, errors.New("URL protocol must be HTTPs")
	}

	client := &http.Client{
		// Do not follow redirects automatically, relay to the caller.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 30 * time.Second,
	}

	if req.TrustedCertFingerprint != nil && len(req.TrustedCertFingerprint) != 0 {
		client.Transport = &http.Transport{
			// Perform custom server certificate verification by pinning the
			// trusted certificate fingerprint.
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify:    true,
				VerifyPeerCertificate: makePinnedCertVerifier(req.TrustedCertFingerprint),
			},
		}
	}

	httpres, err := client.Do(httpreq)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Read the response body to EOF and close it, ignoring errors.
		ioutil.ReadAll(httpres.Body)
		httpres.Body.Close()
	}()

	var res OnlineConfigResponse
	res.HTTPStatusCode = httpres.StatusCode
	if res.HTTPStatusCode >= 300 && res.HTTPStatusCode < 400 {
		// Redirect
		res.RedirectURL = httpres.Header.Get("Location")
		return &res, nil
	} else if res.HTTPStatusCode >= 400 {
		// HTTP error
		return &res, nil
	}

	var config OnlineConfig
	err = json.NewDecoder(httpres.Body).Decode(&config)
	if config.Version != 1 {
		log.Warnf("Received Shadowsocks online config version %d", config.Version)
	}
	res.OnlineConfig = config
	return &res, err
}

type certVerifier func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error

// Verifies whether the pinned certificate SHA256 fingerprint,
// trustedCertFingerprint, matches a fingerprint in the certificate chain,
// regardless of the system's TLS certificate validation errors.
func makePinnedCertVerifier(trustedCertFingerprint []byte) certVerifier {
	return func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return x509.CertificateInvalidError{
				nil, x509.NotAuthorizedToSign, "Did not receive TLS certificate"}
		}
		// Compute the sha256 digest of the whole DER-encoded certificate
		fingerprint := sha256.Sum256(rawCerts[0])
		if bytes.Equal(fingerprint[:], trustedCertFingerprint) {
			return nil
		}
		return x509.CertificateInvalidError{
			nil, x509.NotAuthorizedToSign, "Failed to verify TLS certificate"}
	}
}
