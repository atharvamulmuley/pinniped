// Copyright 2020-2021 the Pinniped contributors. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	coreosoidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	v1 "k8s.io/api/core/v1"

	configv1alpha1 "go.pinniped.dev/generated/latest/apis/supervisor/config/v1alpha1"
	idpv1alpha1 "go.pinniped.dev/generated/latest/apis/supervisor/idp/v1alpha1"
	"go.pinniped.dev/internal/certauthority"
	"go.pinniped.dev/internal/oidc"
	"go.pinniped.dev/internal/testutil"
	"go.pinniped.dev/pkg/oidcclient/nonce"
	"go.pinniped.dev/pkg/oidcclient/pkce"
	"go.pinniped.dev/pkg/oidcclient/state"
	"go.pinniped.dev/test/library"
	"go.pinniped.dev/test/library/browsertest"
)

func TestSupervisorLogin(t *testing.T) {
	env := library.IntegrationEnv(t)

	tests := []struct {
		name                                 string
		createIDP                            func(t *testing.T)
		requestAuthorization                 func(t *testing.T, downstreamAuthorizeURL, downstreamCallbackURL string, httpClient *http.Client)
		wantDownstreamIDTokenSubjectToMatch  string
		wantDownstreamIDTokenUsernameToMatch string
	}{
		{
			name: "oidc",
			createIDP: func(t *testing.T) {
				t.Helper()
				library.CreateTestOIDCIdentityProvider(t, idpv1alpha1.OIDCIdentityProviderSpec{
					Issuer: env.SupervisorUpstreamOIDC.Issuer,
					TLS: &idpv1alpha1.TLSSpec{
						CertificateAuthorityData: base64.StdEncoding.EncodeToString([]byte(env.SupervisorUpstreamOIDC.CABundle)),
					},
					Client: idpv1alpha1.OIDCClient{
						SecretName: library.CreateClientCredsSecret(t, env.SupervisorUpstreamOIDC.ClientID, env.SupervisorUpstreamOIDC.ClientSecret).Name,
					},
				}, idpv1alpha1.PhaseReady)
			},
			requestAuthorization: requestAuthorizationUsingOIDCIdentityProvider,
			// the ID token Subject should include the upstream user ID after the upstream issuer name
			wantDownstreamIDTokenSubjectToMatch: regexp.QuoteMeta(env.SupervisorUpstreamOIDC.Issuer+"?sub=") + ".+",
			// the ID token Username should include the upstream user ID after the upstream issuer name
			wantDownstreamIDTokenUsernameToMatch: regexp.QuoteMeta(env.SupervisorUpstreamOIDC.Issuer+"?sub=") + ".+",
		},
		// TODO add more variations of this LDAP test to try using different user search filters and attributes
		{
			name: "ldap",
			createIDP: func(t *testing.T) {
				t.Helper()
				secret := library.CreateTestSecret(t, env.SupervisorNamespace, "ldap-service-account", v1.SecretTypeBasicAuth,
					map[string]string{
						v1.BasicAuthUsernameKey: env.SupervisorUpstreamLDAP.BindUsername,
						v1.BasicAuthPasswordKey: env.SupervisorUpstreamLDAP.BindPassword,
					},
				)
				library.CreateTestLDAPIdentityProvider(t, idpv1alpha1.LDAPIdentityProviderSpec{
					Host: env.SupervisorUpstreamLDAP.Host,
					TLS: &idpv1alpha1.LDAPIdentityProviderTLSSpec{
						CertificateAuthorityData: base64.StdEncoding.EncodeToString([]byte(env.SupervisorUpstreamLDAP.CABundle)),
					},
					Bind: idpv1alpha1.LDAPIdentityProviderBindSpec{
						SecretName: secret.Name,
					},
					UserSearch: idpv1alpha1.LDAPIdentityProviderUserSearchSpec{
						Base:   env.SupervisorUpstreamLDAP.UserSearchBase,
						Filter: "",
						Attributes: idpv1alpha1.LDAPIdentityProviderUserSearchAttributesSpec{
							Username: env.SupervisorUpstreamLDAP.TestUserMailAttributeName,
							UniqueID: env.SupervisorUpstreamLDAP.TestUserUniqueIDAttributeName,
						},
					},
				}, idpv1alpha1.LDAPPhaseReady)
			},
			requestAuthorization: func(t *testing.T, downstreamAuthorizeURL, _ string, httpClient *http.Client) {
				requestAuthorizationUsingLDAPIdentityProvider(t,
					downstreamAuthorizeURL,
					env.SupervisorUpstreamLDAP.TestUserMailAttributeValue, // username to present to server during login
					env.SupervisorUpstreamLDAP.TestUserPassword,           // password to present to server during login
					httpClient,
				)
			},
			// the ID token Subject should be the Host URL plus the value pulled from the requested UserSearch.Attributes.UID attribute
			wantDownstreamIDTokenSubjectToMatch: regexp.QuoteMeta(
				"ldaps://" + env.SupervisorUpstreamLDAP.Host + "?sub=" + env.SupervisorUpstreamLDAP.TestUserUniqueIDAttributeValue,
			),
			// the ID token Username should have been pulled from the requested UserSearch.Attributes.Username attribute
			wantDownstreamIDTokenUsernameToMatch: regexp.QuoteMeta(env.SupervisorUpstreamLDAP.TestUserMailAttributeValue),
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			testSupervisorLogin(t,
				test.createIDP,
				test.requestAuthorization,
				test.wantDownstreamIDTokenSubjectToMatch,
				test.wantDownstreamIDTokenUsernameToMatch,
			)
		})
	}
}

func testSupervisorLogin(
	t *testing.T,
	createIDP func(t *testing.T),
	requestAuthorization func(t *testing.T, downstreamAuthorizeURL, downstreamCallbackURL string, httpClient *http.Client),
	wantDownstreamIDTokenSubjectToMatch, wantDownstreamIDTokenUsernameToMatch string,
) {
	env := library.IntegrationEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Infer the downstream issuer URL from the callback associated with the upstream test client registration.
	issuerURL, err := url.Parse(env.SupervisorUpstreamOIDC.CallbackURL)
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(issuerURL.Path, "/callback"))
	issuerURL.Path = strings.TrimSuffix(issuerURL.Path, "/callback")
	t.Logf("testing with downstream issuer URL %s", issuerURL.String())

	// Generate a CA bundle with which to serve this provider.
	t.Logf("generating test CA")
	ca, err := certauthority.New("Downstream Test CA", 1*time.Hour)
	require.NoError(t, err)

	// Create an HTTP client that can reach the downstream discovery endpoint using the CA certs.
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: ca.Pool()},
			Proxy: func(req *http.Request) (*url.URL, error) {
				if strings.HasPrefix(req.URL.Host, "127.0.0.1") {
					// don't proxy requests to localhost to avoid proxying calls to our local callback listener
					return nil, nil
				}
				if env.Proxy == "" {
					t.Logf("passing request for %s with no proxy", req.URL)
					return nil, nil
				}
				proxyURL, err := url.Parse(env.Proxy)
				require.NoError(t, err)
				t.Logf("passing request for %s through proxy %s", req.URL, proxyURL.String())
				return proxyURL, nil
			},
		},
		// Don't follow redirects automatically.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	oidcHTTPClientContext := coreosoidc.ClientContext(ctx, httpClient)

	// Use the CA to issue a TLS server cert.
	t.Logf("issuing test certificate")
	tlsCert, err := ca.IssueServerCert([]string{issuerURL.Hostname()}, nil, 1*time.Hour)
	require.NoError(t, err)
	certPEM, keyPEM, err := certauthority.ToPEM(tlsCert)
	require.NoError(t, err)

	// Write the serving cert to a secret.
	certSecret := library.CreateTestSecret(t,
		env.SupervisorNamespace,
		"oidc-provider-tls",
		v1.SecretTypeTLS,
		map[string]string{"tls.crt": string(certPEM), "tls.key": string(keyPEM)},
	)

	// Create the downstream FederationDomain and expect it to go into the success status condition.
	downstream := library.CreateTestFederationDomain(ctx, t,
		issuerURL.String(),
		certSecret.Name,
		configv1alpha1.SuccessFederationDomainStatusCondition,
	)

	// Ensure the the JWKS data is created and ready for the new FederationDomain by waiting for
	// the `/jwks.json` endpoint to succeed, because there is no point in proceeding and eventually
	// calling the token endpoint from this test until the JWKS data has been loaded into
	// the server's in-memory JWKS cache for the token endpoint to use.
	requestJWKSEndpoint, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		fmt.Sprintf("%s/jwks.json", issuerURL.String()),
		nil,
	)
	require.NoError(t, err)
	var jwksRequestStatus int
	assert.Eventually(t, func() bool {
		rsp, err := httpClient.Do(requestJWKSEndpoint)
		require.NoError(t, err)
		require.NoError(t, rsp.Body.Close())
		jwksRequestStatus = rsp.StatusCode
		return jwksRequestStatus == http.StatusOK
	}, 30*time.Second, 200*time.Millisecond)
	require.Equal(t, http.StatusOK, jwksRequestStatus)

	// Create upstream IDP and wait for it to become ready.
	createIDP(t)

	// Perform OIDC discovery for our downstream.
	var discovery *coreosoidc.Provider
	assert.Eventually(t, func() bool {
		discovery, err = coreosoidc.NewProvider(oidcHTTPClientContext, downstream.Spec.Issuer)
		return err == nil
	}, 30*time.Second, 200*time.Millisecond)
	require.NoError(t, err)

	// Start a callback server on localhost.
	localCallbackServer := startLocalCallbackServer(t)

	// Form the OAuth2 configuration corresponding to our CLI client.
	downstreamOAuth2Config := oauth2.Config{
		// This is the hardcoded public client that the supervisor supports.
		ClientID:    "pinniped-cli",
		Endpoint:    discovery.Endpoint(),
		RedirectURL: localCallbackServer.URL,
		Scopes:      []string{"openid", "pinniped:request-audience", "offline_access"},
	}

	// Build a valid downstream authorize URL for the supervisor.
	stateParam, err := state.Generate()
	require.NoError(t, err)
	nonceParam, err := nonce.Generate()
	require.NoError(t, err)
	pkceParam, err := pkce.Generate()
	require.NoError(t, err)
	downstreamAuthorizeURL := downstreamOAuth2Config.AuthCodeURL(
		stateParam.String(),
		nonceParam.Param(),
		pkceParam.Challenge(),
		pkceParam.Method(),
	)

	// Perform parameterized auth code acquisition.
	requestAuthorization(t, downstreamAuthorizeURL, localCallbackServer.URL, httpClient)

	// Expect that our callback handler was invoked.
	callback := localCallbackServer.waitForCallback(10 * time.Second)
	t.Logf("got callback request: %s", library.MaskTokens(callback.URL.String()))
	require.Equal(t, stateParam.String(), callback.URL.Query().Get("state"))
	require.ElementsMatch(t, []string{"openid", "pinniped:request-audience", "offline_access"}, strings.Split(callback.URL.Query().Get("scope"), " "))
	authcode := callback.URL.Query().Get("code")
	require.NotEmpty(t, authcode)

	// Call the token endpoint to get tokens.
	tokenResponse, err := downstreamOAuth2Config.Exchange(oidcHTTPClientContext, authcode, pkceParam.Verifier())
	require.NoError(t, err)

	expectedIDTokenClaims := []string{"iss", "exp", "sub", "aud", "auth_time", "iat", "jti", "nonce", "rat", "username", "groups"}
	verifyTokenResponse(t,
		tokenResponse, discovery, downstreamOAuth2Config, nonceParam,
		expectedIDTokenClaims, wantDownstreamIDTokenSubjectToMatch, wantDownstreamIDTokenUsernameToMatch)

	// token exchange on the original token
	doTokenExchange(t, &downstreamOAuth2Config, tokenResponse, httpClient, discovery)

	// Use the refresh token to get new tokens
	refreshSource := downstreamOAuth2Config.TokenSource(oidcHTTPClientContext, &oauth2.Token{RefreshToken: tokenResponse.RefreshToken})
	refreshedTokenResponse, err := refreshSource.Token()
	require.NoError(t, err)

	expectedIDTokenClaims = append(expectedIDTokenClaims, "at_hash")
	verifyTokenResponse(t,
		refreshedTokenResponse, discovery, downstreamOAuth2Config, "",
		expectedIDTokenClaims, wantDownstreamIDTokenSubjectToMatch, wantDownstreamIDTokenUsernameToMatch)

	require.NotEqual(t, tokenResponse.AccessToken, refreshedTokenResponse.AccessToken)
	require.NotEqual(t, tokenResponse.RefreshToken, refreshedTokenResponse.RefreshToken)
	require.NotEqual(t, tokenResponse.Extra("id_token"), refreshedTokenResponse.Extra("id_token"))

	// token exchange on the refreshed token
	doTokenExchange(t, &downstreamOAuth2Config, refreshedTokenResponse, httpClient, discovery)
}

func verifyTokenResponse(
	t *testing.T,
	tokenResponse *oauth2.Token,
	discovery *coreosoidc.Provider,
	downstreamOAuth2Config oauth2.Config,
	nonceParam nonce.Nonce,
	expectedIDTokenClaims []string,
	wantDownstreamIDTokenSubjectToMatch, wantDownstreamIDTokenUsernameToMatch string,
) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Verify the ID Token.
	rawIDToken, ok := tokenResponse.Extra("id_token").(string)
	require.True(t, ok, "expected to get an ID token but did not")
	var verifier = discovery.Verifier(&coreosoidc.Config{ClientID: downstreamOAuth2Config.ClientID})
	idToken, err := verifier.Verify(ctx, rawIDToken)
	require.NoError(t, err)

	// Check the sub claim of the ID token.
	require.Regexp(t, wantDownstreamIDTokenSubjectToMatch, idToken.Subject)

	// Check the nonce claim of the ID token.
	require.NoError(t, nonceParam.Validate(idToken))

	// Check the exp claim of the ID token.
	expectedIDTokenLifetime := oidc.DefaultOIDCTimeoutsConfiguration().IDTokenLifespan
	testutil.RequireTimeInDelta(t, time.Now().UTC().Add(expectedIDTokenLifetime), idToken.Expiry, time.Second*30)

	// Check the full list of claim names of the ID token.
	idTokenClaims := map[string]interface{}{}
	err = idToken.Claims(&idTokenClaims)
	require.NoError(t, err)
	idTokenClaimNames := []string{}
	for k := range idTokenClaims {
		idTokenClaimNames = append(idTokenClaimNames, k)
	}
	require.ElementsMatch(t, expectedIDTokenClaims, idTokenClaimNames)

	// Check username claim of the ID token.
	require.Regexp(t, wantDownstreamIDTokenUsernameToMatch, idTokenClaims["username"].(string))

	// Some light verification of the other tokens that were returned.
	require.NotEmpty(t, tokenResponse.AccessToken)
	require.Equal(t, "bearer", tokenResponse.TokenType)
	require.NotZero(t, tokenResponse.Expiry)
	expectedAccessTokenLifetime := oidc.DefaultOIDCTimeoutsConfiguration().AccessTokenLifespan
	testutil.RequireTimeInDelta(t, time.Now().UTC().Add(expectedAccessTokenLifetime), tokenResponse.Expiry, time.Second*30)

	require.NotEmpty(t, tokenResponse.RefreshToken)
}

func requestAuthorizationUsingOIDCIdentityProvider(t *testing.T, downstreamAuthorizeURL, downstreamCallbackURL string, httpClient *http.Client) {
	t.Helper()
	env := library.IntegrationEnv(t)

	ctx, cancelFunc := context.WithTimeout(context.Background(), time.Minute)
	defer cancelFunc()

	// Make the authorize request once "manually" so we can check its response security headers.
	authorizeRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, downstreamAuthorizeURL, nil)
	require.NoError(t, err)
	authorizeResp, err := httpClient.Do(authorizeRequest)
	require.NoError(t, err)
	require.NoError(t, authorizeResp.Body.Close())
	expectSecurityHeaders(t, authorizeResp, false)

	// Open the web browser and navigate to the downstream authorize URL.
	page := browsertest.Open(t)
	t.Logf("opening browser to downstream authorize URL %s", library.MaskTokens(downstreamAuthorizeURL))
	require.NoError(t, page.Navigate(downstreamAuthorizeURL))

	// Expect to be redirected to the upstream provider and log in.
	browsertest.LoginToUpstream(t, page, env.SupervisorUpstreamOIDC)

	// Wait for the login to happen and us be redirected back to a localhost callback.
	t.Logf("waiting for redirect to callback")
	callbackURLPattern := regexp.MustCompile(`\A` + regexp.QuoteMeta(downstreamCallbackURL) + `\?.+\z`)
	browsertest.WaitForURL(t, page, callbackURLPattern)
}

func requestAuthorizationUsingLDAPIdentityProvider(t *testing.T, downstreamAuthorizeURL, upstreamUsername, upstreamPassword string, httpClient *http.Client) {
	t.Helper()

	ctx, cancelFunc := context.WithTimeout(context.Background(), time.Minute)
	defer cancelFunc()

	authRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, downstreamAuthorizeURL, nil)
	require.NoError(t, err)

	// Set the custom username/password headers for the LDAP authorize request.
	authRequest.Header.Set("X-Pinniped-Upstream-Username", upstreamUsername)
	authRequest.Header.Set("X-Pinniped-Upstream-Password", upstreamPassword)

	authResponse, err := httpClient.Do(authRequest)
	require.NoError(t, err)
	responseBody, err := ioutil.ReadAll(authResponse.Body)
	defer authResponse.Body.Close()
	require.NoError(t, err)
	expectSecurityHeaders(t, authResponse, true)

	// A successful authorize request results in a redirect to our localhost callback listener with an authcode param.
	require.Equalf(t, http.StatusFound, authResponse.StatusCode, "response body was: %s", string(responseBody))
	redirectLocation := authResponse.Header.Get("Location")
	require.Contains(t, redirectLocation, "127.0.0.1")
	require.Contains(t, redirectLocation, "/callback")
	require.Contains(t, redirectLocation, "code=")

	// Follow the redirect.
	callbackRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, redirectLocation, nil)
	require.NoError(t, err)

	// Our localhost callback listener should have returned 200 OK.
	callbackResponse, err := httpClient.Do(callbackRequest)
	require.NoError(t, err)
	defer callbackResponse.Body.Close()
	require.Equal(t, http.StatusOK, callbackResponse.StatusCode)
}

func startLocalCallbackServer(t *testing.T) *localCallbackServer {
	// Handle the callback by sending the *http.Request object back through a channel.
	callbacks := make(chan *http.Request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callbacks <- r
	}))
	server.URL += "/callback"
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(callbacks) })
	return &localCallbackServer{Server: server, t: t, callbacks: callbacks}
}

type localCallbackServer struct {
	*httptest.Server
	t         *testing.T
	callbacks <-chan *http.Request
}

func (s *localCallbackServer) waitForCallback(timeout time.Duration) *http.Request {
	select {
	case callback := <-s.callbacks:
		return callback
	case <-time.After(timeout):
		require.Fail(s.t, "timed out waiting for callback request")
		return nil
	}
}

func doTokenExchange(t *testing.T, config *oauth2.Config, tokenResponse *oauth2.Token, httpClient *http.Client, provider *coreosoidc.Provider) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Form the HTTP POST request with the parameters specified by RFC8693.
	reqBody := strings.NewReader(url.Values{
		"grant_type":           []string{"urn:ietf:params:oauth:grant-type:token-exchange"},
		"audience":             []string{"cluster-1234"},
		"client_id":            []string{config.ClientID},
		"subject_token":        []string{tokenResponse.AccessToken},
		"subject_token_type":   []string{"urn:ietf:params:oauth:token-type:access_token"},
		"requested_token_type": []string{"urn:ietf:params:oauth:token-type:jwt"},
	}.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.Endpoint.TokenURL, reqBody)
	require.NoError(t, err)
	req.Header.Set("content-type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, resp.StatusCode, http.StatusOK)
	defer func() { _ = resp.Body.Close() }()
	var respBody struct {
		AccessToken     string `json:"access_token"`
		IssuedTokenType string `json:"issued_token_type"`
		TokenType       string `json:"token_type"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&respBody))

	var clusterVerifier = provider.Verifier(&coreosoidc.Config{ClientID: "cluster-1234"})
	exchangedToken, err := clusterVerifier.Verify(ctx, respBody.AccessToken)
	require.NoError(t, err)

	var claims map[string]interface{}
	require.NoError(t, exchangedToken.Claims(&claims))
	indentedClaims, err := json.MarshalIndent(claims, "   ", "  ")
	require.NoError(t, err)
	t.Logf("exchanged token claims:\n%s", string(indentedClaims))
}

func expectSecurityHeaders(t *testing.T, response *http.Response, expectFositeToOverrideSome bool) {
	h := response.Header
	assert.Equal(t, "default-src 'none'; frame-ancestors 'none'", h.Get("Content-Security-Policy"))
	assert.Equal(t, "DENY", h.Get("X-Frame-Options"))
	assert.Equal(t, "1; mode=block", h.Get("X-XSS-Protection"))
	assert.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
	assert.Equal(t, "no-referrer", h.Get("Referrer-Policy"))
	assert.Equal(t, "off", h.Get("X-DNS-Prefetch-Control"))
	if expectFositeToOverrideSome {
		assert.Equal(t, "no-store", h.Get("Cache-Control"))
	} else {
		assert.Equal(t, "no-cache,no-store,max-age=0,must-revalidate", h.Get("Cache-Control"))
	}
	assert.Equal(t, "no-cache", h.Get("Pragma"))
	assert.Equal(t, "0", h.Get("Expires"))
}
