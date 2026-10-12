package mailtest

import (
	"context"
	"sync"
)

type Message struct {
	To            []string
	Subject, Body string
}

type Recorder struct {
	mu       sync.Mutex
	Messages []Message
	Fail     error
}

func (r *Recorder) Send(_ context.Context, to []string, subject, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Fail != nil {
		return r.Fail
	}
	r.Messages = append(r.Messages, Message{To: to, Subject: subject, Body: body})
	return nil
}

func (r *Recorder) To(addr string) []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Message
	for _, m := range r.Messages {
		for _, t := range m.To {
			if t == addr {
				out = append(out, m)
			}
		}
	}
	return out
}
