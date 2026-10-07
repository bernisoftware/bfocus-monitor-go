package bfmonitor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"runtime"
	"strconv"
	"sync"
	"time"
)

// Sinal de vida (monitor/BRIEF.md §7b): o painel mostra o agente VIVO mesmo sem erro.
// Um no Init (em segundo plano: o Init continua sem rede) e depois a cada 5 min, até o Close.

type heartbeatBody struct {
	Instance    string            `json:"instance"`
	Release     string            `json:"release,omitempty"`
	Environment string            `json:"environment,omitempty"`
	Host        string            `json:"host,omitempty"`
	Runtime     map[string]string `json:"runtime"`
	SDK         SDK               `json:"sdk"`
}

var (
	instanceOnce sync.Once
	instanceID   string
	hostName     string
)

// processInstance: hash curto de hostname + pid (estável enquanto o processo vive).
func processInstance() (string, string) {
	instanceOnce.Do(func() {
		hostName, _ = os.Hostname()
		sum := sha256.Sum256([]byte(hostName + ":" + strconv.Itoa(os.Getpid())))
		instanceID = hex.EncodeToString(sum[:])[:16]
	})
	return instanceID, hostName
}

func (c *client) heartbeatBody() []byte {
	id, host := processInstance()
	b, err := json.Marshal(heartbeatBody{
		Instance: id, Release: c.opts.Release, Environment: c.opts.Environment, Host: truncate(host, 100),
		Runtime: map[string]string{"name": "go", "version": runtime.Version()},
		SDK:     SDK{Name: SDKName, Version: Version},
	})
	if err != nil {
		return nil
	}
	return b
}

func (c *client) heartbeatLoop() {
	defer swallow()
	t := time.NewTicker(heartbeatInterval)
	defer t.Stop()
	for {
		c.sendHeartbeat()
		select {
		case <-c.stop:
			return
		case <-t.C:
		}
	}
}

// sendHeartbeat: 401/403 desliga o envio (como nos eventos); rede/5xx: o próximo tenta.
func (c *client) sendHeartbeat() {
	defer swallow()
	if c.disabled.Load() || c.closed.Load() {
		return
	}
	body := c.heartbeatBody()
	if body == nil {
		return
	}
	status, err := c.sender.heartbeat(body)
	if err == nil && (status == 401 || status == 403) {
		c.disabled.Store(true)
		c.dropQueue()
	}
}
