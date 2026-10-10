package service

import (
	"context"
	"io"
	"sync"
)

func acquireOpenAIAccountDriverOperation(ctx context.Context, account *Account) (context.Context, func(), error) {
	if account == nil || account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth {
		return ctx, func() {}, nil
	}
	return AcquireAccountDriverBoundOperation(ctx, account, "inference")
}

type accountDriverAdmissionBody struct {
	io.ReadCloser
	release   func()
	once      sync.Once
	closeOnce sync.Once
	closeErr  error
	done      chan struct{}
}

func newAccountDriverAdmissionBody(body io.ReadCloser, release func(), ctx context.Context) *accountDriverAdmissionBody {
	b := &accountDriverAdmissionBody{ReadCloser: body, release: release, done: make(chan struct{})}
	if abort := AccountDriverOperationAbortContext(ctx); abort != nil && abort.Done() != nil {
		go func() {
			select {
			case <-b.done:
			case <-abort.Done():
				_ = b.Close()
			}
		}()
	}
	return b
}

func (b *accountDriverAdmissionBody) finish() {
	b.once.Do(func() {
		close(b.done)
		b.release()
	})
}

func (b *accountDriverAdmissionBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.finish()
	}
	return n, err
}

func (b *accountDriverAdmissionBody) Close() error {
	b.closeOnce.Do(func() {
		b.closeErr = b.ReadCloser.Close()
		b.finish()
	})
	return b.closeErr
}
