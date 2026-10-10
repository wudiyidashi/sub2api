package service

import (
	"context"
	"sync"
)

func (s *OpenAIGatewayService) DrainAccountDriverConnections(ctx context.Context, id int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if store := s.getOpenAIWSStateStore(); store != nil {
		if err := store.InvalidateAccount(ctx, id); err != nil {
			return err
		}
	}
	if pool := s.getOpenAIWSConnPool(); pool != nil {
		pool.ClearAccount(id)
	}
	if s.pluginManager != nil && s.pluginManager.accountDriverManager != nil {
		return s.pluginManager.accountDriverManager.DrainAccountWebSockets(id)
	}
	return nil
}

// The maintenance lease represents a business turn, never an indefinitely idle
// websocket. A persisted claim blocks the next turn while the current completes.
func wrapAccountDriverMaintenanceWSHooks(ctx context.Context, account *Account, hooks *OpenAIWSIngressHooks) (context.Context, *OpenAIWSIngressHooks, func(), error) {
	base, cancelSession := context.WithCancelCause(ctx)
	var mu sync.Mutex
	releases := map[int]func(){}
	acquire := func(turn int) error {
		mu.Lock()
		defer mu.Unlock()
		if releases[turn] != nil {
			return nil
		}
		operationCtx, release, err := AcquireAccountDriverBoundOperation(base, account, "ws_turn")
		if err != nil {
			return err
		}
		stop := make(chan struct{})
		var releaseOnce sync.Once
		releases[turn] = func() {
			releaseOnce.Do(func() {
				close(stop)
				release()
			})
		}
		if abort := AccountDriverOperationAbortContext(operationCtx); abort != nil {
			go func() {
				select {
				case <-stop:
					return
				case <-abort.Done():
					cancelSession(context.Cause(abort))
				}
			}()
		}
		return nil
	}
	if err := acquire(1); err != nil {
		cancelSession(err)
		return ctx, nil, func() {}, err
	}
	wrapped := &OpenAIWSIngressHooks{}
	if hooks != nil {
		*wrapped = *hooks
	}
	wrapped.BeforeTurn = func(turn int) error {
		if err := acquire(turn); err != nil {
			return err
		}
		if hooks != nil && hooks.BeforeTurn != nil {
			return hooks.BeforeTurn(turn)
		}
		return nil
	}
	wrapped.AfterTurn = func(turn int, result *OpenAIForwardResult, err error) {
		if hooks != nil && hooks.AfterTurn != nil {
			hooks.AfterTurn(turn, result, err)
		}
		mu.Lock()
		release := releases[turn]
		delete(releases, turn)
		mu.Unlock()
		if release != nil {
			release()
		}
	}
	cleanup := func() {
		cancelSession(context.Canceled)
		mu.Lock()
		pending := make([]func(), 0, len(releases))
		for turn, release := range releases {
			pending = append(pending, release)
			delete(releases, turn)
		}
		mu.Unlock()
		for _, release := range pending {
			release()
		}
	}
	return base, wrapped, cleanup, nil
}
