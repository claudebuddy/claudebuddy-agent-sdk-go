package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func recordingHTTPClient(t *testing.T) (*http.Client, *[]MessagesRequest) {
	t.Helper()
	requests := []MessagesRequest{}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var captured MessagesRequest
		if err := json.NewDecoder(req.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		requests = append(requests, captured)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"role":"assistant","model":"test","stop_reason":"end_turn"}`)),
			Request:    req,
		}, nil
	})}
	return client, &requests
}

func TestClientResolvesMaxTokensFromRequestModel(t *testing.T) {
	httpClient, requests := recordingHTTPClient(t)
	client := NewClient(ClientConfig{
		APIKey:     "test-key",
		BaseURL:    "https://local.invalid",
		Model:      "sonnet-4-6",
		HTTPClient: httpClient,
	})

	tests := []struct {
		model         string
		wantMaxTokens int
	}{
		{model: "sonnet-4-6", wantMaxTokens: 16384},
		{model: "haiku-4-5", wantMaxTokens: 8192},
	}
	for _, tt := range tests {
		if _, err := client.CreateMessage(context.Background(), MessagesRequest{Model: tt.model}); err != nil {
			t.Fatalf("CreateMessage(%q): %v", tt.model, err)
		}
		got := (*requests)[len(*requests)-1]
		if got.Model != tt.model || got.MaxTokens != tt.wantMaxTokens {
			t.Fatalf("request = {Model:%q MaxTokens:%d}, want {Model:%q MaxTokens:%d}", got.Model, got.MaxTokens, tt.model, tt.wantMaxTokens)
		}
	}
}

func TestClientPreservesExplicitMaxTokensOverride(t *testing.T) {
	t.Run("client config", func(t *testing.T) {
		httpClient, requests := recordingHTTPClient(t)
		client := NewClient(ClientConfig{
			APIKey:     "test-key",
			BaseURL:    "https://local.invalid",
			Model:      "sonnet-4-6",
			MaxTokens:  24000,
			HTTPClient: httpClient,
		})
		client.SetModel("haiku-4-5")

		if _, err := client.CreateMessage(context.Background(), MessagesRequest{}); err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
		got := (*requests)[0]
		if got.Model != "haiku-4-5" || got.MaxTokens != 24000 {
			t.Fatalf("request = {Model:%q MaxTokens:%d}, want {Model:%q MaxTokens:%d}", got.Model, got.MaxTokens, "haiku-4-5", 24000)
		}
	})

	t.Run("request", func(t *testing.T) {
		httpClient, requests := recordingHTTPClient(t)
		client := NewClient(ClientConfig{
			APIKey:     "test-key",
			BaseURL:    "https://local.invalid",
			Model:      "sonnet-4-6",
			MaxTokens:  24000,
			HTTPClient: httpClient,
		})

		if _, err := client.CreateMessage(context.Background(), MessagesRequest{Model: "haiku-4-5", MaxTokens: 1234}); err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
		if got := (*requests)[0].MaxTokens; got != 1234 {
			t.Fatalf("MaxTokens = %d, want explicit request override 1234", got)
		}
	})
}
