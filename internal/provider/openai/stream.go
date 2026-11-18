package openai

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"velo/internal/protocol"
	"velo/internal/provider"
)

const maxSSELineBytes = 1 << 20

type sseStream struct {
	ctx        context.Context
	body       io.ReadCloser
	reader     *bufio.Reader
	stop       chan struct{}
	closeOnce  sync.Once
	mu         sync.Mutex
	closed     bool
	total      int64
	data       []string
	queue      []protocol.StreamEvent
	finish     protocol.FinishReason
	finishSet  bool
	sawCalls   bool
	finished   bool
	doneQueued bool
	done       bool
	err        error
}

func newStream(ctx context.Context, body io.ReadCloser) provider.Stream {
	s := &sseStream{
		ctx:    ctx,
		body:   body,
		reader: bufio.NewReaderSize(body, 32*1024),
		stop:   make(chan struct{}),
	}
	go func() {
		select {
		case <-ctx.Done():
			s.close()
		case <-s.stop:
		}
	}()
	return s
}

func (s *sseStream) Recv() (protocol.StreamEvent, error) {
	if s.isClosed() {
		return protocol.StreamEvent{}, io.EOF
	}
	if err := s.ctx.Err(); err != nil {
		return protocol.StreamEvent{}, protocol.ErrorFromContext(providerName, err)
	}
	for {
		if len(s.queue) > 0 {
			event := s.queue[0]
			s.queue = s.queue[1:]
			if event.Type == protocol.StreamDone {
				s.done = true
				s.close()
			}
			return event, nil
		}
		if s.done {
			return protocol.StreamEvent{}, io.EOF
		}
		if s.err != nil {
			return protocol.StreamEvent{}, s.err
		}
		if s.finished {
			s.done = true
			s.close()
			return protocol.StreamEvent{}, io.EOF
		}
		err := s.pump()
		if err == nil {
			continue
		}
		if s.isClosed() {
			s.done = true
			return protocol.StreamEvent{}, io.EOF
		}
		if errors.Is(err, io.EOF) {
			s.finished = true
			s.enqueueDone()
			continue
		}
		s.err = err
		s.finished = true
		return protocol.StreamEvent{}, err
	}
}

func (s *sseStream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.close()
	return nil
}

func (s *sseStream) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *sseStream) close() {
	s.closeOnce.Do(func() {
		close(s.stop)
		if s.body != nil {
			_ = s.body.Close()
		}
	})
}

func (s *sseStream) pump() error {
	for {
		line, err := s.readLine()
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if line == "" {
			if errors.Is(err, io.EOF) {
				if len(s.data) > 0 {
					if derr := s.dispatch(); derr != nil {
						return derr
					}
				}
				return io.EOF
			}
			if len(s.data) > 0 {
				if derr := s.dispatch(); derr != nil {
					return derr
				}
				return nil
			}
			if s.finished {
				return nil
			}
			continue
		}
		s.consumeLine(line)
		if s.finished {
			return nil
		}
		if errors.Is(err, io.EOF) {
			if len(s.data) > 0 {
				if derr := s.dispatch(); derr != nil {
					return derr
				}
			}
			return io.EOF
		}
	}
}

func (s *sseStream) readLine() (string, error) {
	var buf []byte
	for {
		chunk, err := s.reader.ReadSlice('\n')
		s.total += int64(len(chunk))
		if s.total > maxBodyBytes {
			return "", protocol.NewProviderError(protocol.ErrProtocol, providerName, "stream response exceeded 8 MiB limit")
		}
		if len(buf)+len(chunk) > maxSSELineBytes {
			return "", protocol.NewProviderError(protocol.ErrProtocol, providerName, "stream line exceeded 1 MiB limit")
		}
		buf = append(buf, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if ctxErr := s.ctx.Err(); ctxErr != nil {
				return "", protocol.ErrorFromContext(providerName, ctxErr)
			}
			if s.isClosed() {
				return "", io.EOF
			}
			if errors.Is(err, io.EOF) {
				if len(buf) == 0 {
					return "", io.EOF
				}
				return trimSSELine(string(buf)), nil
			}
			return "", protocol.ErrorFromContext(providerName, err)
		}
		return trimSSELine(string(buf)), nil
	}
}

func trimSSELine(line string) string {
	line = strings.TrimSuffix(line, "\n")
	return strings.TrimSuffix(line, "\r")
}

func (s *sseStream) consumeLine(line string) {
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	if line == "" || strings.HasPrefix(line, ":") {
		return
	}
	field, value, found := strings.Cut(line, ":")
	if !found || field != "data" {
		return
	}
	s.data = append(s.data, strings.TrimPrefix(value, " "))
}

func (s *sseStream) dispatch() error {
	payload := strings.TrimSpace(strings.Join(s.data, "\n"))
	s.data = s.data[:0]
	if payload == "" {
		return nil
	}
	if payload == "[DONE]" {
		s.finished = true
		s.enqueueDone()
		return nil
	}

	var chunk chatResponse
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return protocol.NewProviderError(protocol.ErrProtocol, providerName, fmt.Sprintf("invalid stream json: %v", err))
	}
	if chunk.Error != nil {
		s.finished = true
		s.queue = append(s.queue, protocol.StreamEvent{
			Type:  protocol.StreamError,
			Error: errorFromChatError(chunk.Error),
		})
		return nil
	}

	for _, choice := range chunk.Choices {
		text, err := contentText(choice.Delta.Content)
		if err != nil {
			return protocol.NewProviderError(protocol.ErrProtocol, providerName, fmt.Sprintf("invalid stream content: %v", err))
		}
		if text != "" {
			s.queue = append(s.queue, protocol.StreamEvent{
				Type: protocol.StreamTextDelta,
				Text: text,
			})
		}
		for _, call := range choice.Delta.ToolCalls {
			if call.ID == "" && call.Function.Name == "" && call.Function.Arguments == "" {
				continue
			}
			s.sawCalls = true
			s.queue = append(s.queue, protocol.StreamEvent{
				Type: protocol.StreamToolCallDelta,
				ToolCall: &protocol.ToolCallDelta{
					Index:          call.Index,
					ID:             call.ID,
					Name:           call.Function.Name,
					ArgumentsDelta: call.Function.Arguments,
				},
			})
		}
		if strings.TrimSpace(choice.FinishReason) != "" {
			s.finish = mapFinishReason(choice.FinishReason)
			s.finishSet = true
		}
	}

	if chunk.Usage != nil {
		s.queue = append(s.queue, protocol.StreamEvent{
			Type:  protocol.StreamUsage,
			Usage: convertUsage(chunk.Usage),
		})
	}
	return nil
}

func (s *sseStream) enqueueDone() {
	if s.doneQueued {
		return
	}
	s.doneQueued = true
	finish := s.finish
	if !s.finishSet {
		if s.sawCalls {
			finish = protocol.FinishToolCalls
		} else {
			finish = protocol.FinishStop
		}
	}
	if finish == "" || !finish.Valid() {
		finish = protocol.FinishStop
	}
	s.queue = append(s.queue, protocol.StreamEvent{
		Type:         protocol.StreamDone,
		FinishReason: finish,
	})
}
