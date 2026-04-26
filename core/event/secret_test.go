package event

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMaskSecrets_AnthropicAPIKey(t *testing.T) {
	in := "Authorization: x-api-key sk-ant-api03-AbC123XYZ_DEF456GHIjklmno789"
	out := MaskSecrets(in)
	require.NotContains(t, out, "AbC123XYZ_DEF456GHIjklmno789")
	require.Contains(t, out, "<secret_hidden>")
}

func TestMaskSecrets_AnthropicOAuthToken(t *testing.T) {
	in := "token=sk-ant-oat01-abcdefghij1234567890klmnopqrstu"
	out := MaskSecrets(in)
	require.NotContains(t, out, "abcdefghij1234567890klmnopqrstu")
}

func TestMaskSecrets_OpenAIKey(t *testing.T) {
	in := "OPENAI_API_KEY=sk-proj-abcdefghijklmnopqrstuvwxyz123456"
	out := MaskSecrets(in)
	require.NotContains(t, out, "abcdefghijklmnopqrstuvwxyz123456")
	require.Contains(t, out, "<secret_hidden>")
}

func TestMaskSecrets_GitHubToken(t *testing.T) {
	in := "GITHUB_TOKEN=ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789"
	out := MaskSecrets(in)
	require.NotContains(t, out, "ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789")
}

func TestMaskSecrets_PasswordKeepsContext(t *testing.T) {
	in := "password=hunter2is-strong-pwd"
	out := MaskSecrets(in)
	require.Contains(t, out, "password=") // context preserved
	require.NotContains(t, out, "hunter2is-strong-pwd")
}

func TestMaskSecrets_BearerToken(t *testing.T) {
	in := "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.signature123abc"
	out := MaskSecrets(in)
	require.NotContains(t, out, "eyJhbGciOiJIUzI1NiJ9.payload.signature123abc")
	require.Contains(t, out, "Bearer")
}

func TestMaskSecrets_NoFalsePositives(t *testing.T) {
	cases := []string{
		"the file path is /usr/local/bin/something",
		"this is a normal sentence with no secrets",
		"shorts: sk-abc, password=x, bearer y",  // too short, should NOT match
	}
	for _, c := range cases {
		require.Equal(t, c, MaskSecrets(c), "false positive on: %q", c)
	}
}

func TestMaskSecrets_PreservesNonSecretText(t *testing.T) {
	in := "Before sk-ant-api01-AbCdEfGhIjKlMnOpQrStUvWxYz1234567890 after"
	out := MaskSecrets(in)
	require.True(t, strings.HasPrefix(out, "Before "))
	require.True(t, strings.HasSuffix(out, " after"))
	require.Contains(t, out, "<secret_hidden>")
}
