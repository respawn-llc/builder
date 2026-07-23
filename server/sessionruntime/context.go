package sessionruntime

import (
	"context"
	"sync"
)

func MergeContexts(contexts ...context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	stop := func() { once.Do(cancel) }
	for _, source := range contexts {
		if source == nil {
			continue
		}
		if err := source.Err(); err != nil {
			stop()
			continue
		}
		done := source.Done()
		if done == nil {
			continue
		}
		go func() {
			select {
			case <-done:
				stop()
			case <-ctx.Done():
			}
		}()
	}
	return ctx, stop
}
