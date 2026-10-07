package bfmonitor

import (
	"bytes"
	"io"
	"net/http"
	"time"
)

// sender manda um corpo {"events":[...]} e devolve o status HTTP (err = falha de rede).
type sender interface {
	send(body []byte) (int, error)
	heartbeat(body []byte) (int, error)
}

type httpSender struct {
	c  *client
	hc *http.Client
}

func newHTTPSender(c *client) sender {
	return &httpSender{c: c, hc: &http.Client{Timeout: 10 * time.Second}}
}

func (s *httpSender) send(body []byte) (int, error) { return s.post(s.c.endpoint, body) }

func (s *httpSender) heartbeat(body []byte) (int, error) { return s.post(s.c.hbEndpoint, body) }

func (s *httpSender) post(url string, body []byte) (int, error) {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-bFocus-Monitor-Key", s.c.opts.Key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-bFocus-Client", s.c.userAgent)
	req.Header.Set("User-Agent", s.c.userAgent)
	resp, err := s.hc.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}
