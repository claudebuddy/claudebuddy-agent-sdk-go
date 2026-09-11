package main

import (
	"context"
	"log"
	"os"
	"sync"
	"time"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/agent"
)

func main() {
	a := agent.New(agent.Options{
		APIKey:            os.Getenv("CLAUDEBUDDY_API_KEY"),
		Model:             os.Getenv("CLAUDEBUDDY_MODEL"),
		BaseURL:           os.Getenv("CLAUDEBUDDY_BASE_URL"),
		MaxConcurrentRuns: 32,
	})
	defer a.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var wg sync.WaitGroup
	for _, id := range []string{"customer-a", "customer-b"} {
		session, err := a.NewSession(agent.SessionOptions{ID: id})
		if err != nil {
			log.Fatal(err)
		}
		wg.Add(1)
		go func(s *agent.Session) {
			defer wg.Done()
			result, err := s.Prompt(ctx, "Summarize this session in one sentence")
			if err != nil {
				if result != nil {
					log.Printf("%s (%s): %v", s.SessionID(), result.Subtype, err)
				} else {
					log.Printf("%s: %v", s.SessionID(), err)
				}
				return
			}
			log.Printf("%s: %s", s.SessionID(), result.Text)
		}(session)
	}
	wg.Wait()
}
