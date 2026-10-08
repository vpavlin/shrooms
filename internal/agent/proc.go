// Package agent runs coding-agent sessions — Claude Code, pi, and other
// harnesses (harness.go) — and serves them to the owner's other devices over
// the mesh (docs/agents.md).
//
// A session is a name, a directory and a harness — what `cl` keyed its tmux
// sessions on — plus the conversation id that lets it be resumed. While in use
// it has one process of its harness; idle, it has none, and the next message
// resumes it.
package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
)

// proc is one running session process — `claude -p`, `pi --mode rpc`, or
// another harness's.
//
// What it writes goes through its harness's codec; the stream-json messages
// that come out are handed to out in order. out is closed when the process has
// gone, and err then says why.
type proc struct {
	cmd   *exec.Cmd
	codec Codec

	mu    sync.Mutex // serialises writes: lines must not interleave
	stdin io.WriteCloser

	out  chan json.RawMessage
	done chan struct{}
	err  error
	// read is closed when the session has finished with the process: its
	// last event, "stopped", is written. Stopping waits for this, not done —
	// a session removed once done is closed had its log recreated by that
	// last write (2026-10-04).
	read chan struct{}
	// cage is where it runs; nil on the machine itself.
	cage *cageRun
}

// cage, when not nil, runs the process in the session's cage (cage.go),
// which is stopped once the process has ended.
func startProc(ctx context.Context, log *slog.Logger, h Harness, bin, dir string, o StartOptions, cage *cageRun) (*proc, error) {
	var cmd *exec.Cmd
	if cage != nil {
		name, args := cage.command(h.Args(o))
		cmd = exec.CommandContext(ctx, name, args...)
	} else {
		cmd = exec.CommandContext(ctx, bin, h.Args(o)...)
		cmd.Dir = dir
		if o.Session != "" {
			cmd.Env = append(os.Environ(), "SHROOMS_AGENT_SESSION="+o.Session)
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", bin, err)
	}
	p := &proc{cmd: cmd, codec: h.Codec(), stdin: stdin, out: make(chan json.RawMessage, 64),
		done: make(chan struct{}), read: make(chan struct{}), cage: cage}

	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			log.Debug(h.Name()+" stderr", "line", sc.Text())
		}
	}()
	go func() {
		defer close(p.done)
		sc := bufio.NewScanner(stdout)
		// A tool result can be a whole file; the default 64 KiB line limit
		// would end the session on the first large read.
		sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
		for sc.Scan() {
			line := bytes.TrimSuffix(sc.Bytes(), []byte("\r"))
			if !json.Valid(line) {
				log.Debug(h.Name()+" wrote a line that is not JSON", "line", string(line))
				continue
			}
			for _, m := range p.codec.Decode(append(json.RawMessage(nil), line...)) {
				p.out <- m
			}
		}
		close(p.out)
		werr := cmd.Wait()
		// Before done: a restart waits for it, and must not have its new
		// process's cage stopped under it.
		if cage != nil {
			cage.stop()
		}
		switch {
		case sc.Err() != nil:
			p.err = sc.Err()
		case werr != nil:
			p.err = werr
		}
	}()
	if err := p.writeAll(p.codec.Start()); err != nil {
		p.close()
		return nil, err
	}
	return p, nil
}

// writeAll sends each of msgs, in order.
func (p *proc) writeAll(msgs []any) error {
	for _, m := range msgs {
		if err := p.write(m); err != nil {
			return err
		}
	}
	return nil
}

// write sends one message to the process.
func (p *proc) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stdin == nil {
		return errors.New("the session's process has stopped taking input")
	}
	_, err = p.stdin.Write(append(b, '\n'))
	return err
}

// close ends the input, which is how a stream-json process is asked to finish:
// it completes the turn in hand and exits.
func (p *proc) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stdin != nil {
		p.stdin.Close()
		p.stdin = nil
	}
}
