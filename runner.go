package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// RunController serializes agent runs behind the web UI: at most one task
// executes at a time, and Stop cancels the active context.
type RunController struct {
	mu      sync.Mutex
	agent   *Agent
	tracer  *Tracer
	cancel  context.CancelFunc
	running bool
	lastErr string
}

// NewRunController wires an agent and its tracer for interactive control.
func NewRunController(agent *Agent, tracer *Tracer) *RunController {
	return &RunController{agent: agent, tracer: tracer}
}

// Running reports whether a task is in flight.
func (c *RunController) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// LastError returns the last run error message (empty on success / idle).
func (c *RunController) LastError() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

// Start begins task in a background goroutine. Returns an error if a run is
// already active or task is empty.
func (c *RunController) Start(task string) error {
	task = strings.TrimSpace(task)
	if task == "" {
		return fmt.Errorf("empty task")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		return fmt.Errorf("a task is already running")
	}
	if c.tracer != nil {
		c.tracer.Reset()
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.running = true
	c.lastErr = ""
	go c.execute(ctx, task)
	return nil
}

// Stop cancels the active run. Returns false if nothing was running.
func (c *RunController) Stop() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running || c.cancel == nil {
		return false
	}
	c.cancel()
	return true
}

func (c *RunController) execute(ctx context.Context, task string) {
	res, err := c.agent.Run(ctx, task)
	c.mu.Lock()
	c.running = false
	c.cancel = nil
	if err != nil {
		c.lastErr = err.Error()
		if c.tracer != nil {
			// Run already records error events for most failures; ensure done.
			c.tracer.SetDone()
		}
	} else {
		c.lastErr = ""
		_ = res
		if c.tracer != nil {
			c.tracer.SetDone()
		}
	}
	c.mu.Unlock()
}
