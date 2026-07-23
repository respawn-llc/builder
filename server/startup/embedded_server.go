package startup

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"core/server/auth"
	"core/server/core"
	"core/shared/config"
)

type EmbeddedServer struct {
	*core.Core
	deps *startupGatewayDependencies
	cfg  config.App

	rpcMu sync.Mutex
	rpc   *runningRPC

	fatalCancel context.CancelFunc
	fatalWG     sync.WaitGroup
	fatalErr    error
	failures    chan error

	closeOnce sync.Once
	closeErr  error
}

func (s *EmbeddedServer) Config() config.App {
	if s == nil {
		return config.App{}
	}
	if s.Core != nil {
		return s.Core.Config()
	}
	if s.deps != nil {
		return s.deps.snapshotConfig()
	}
	return s.cfg
}

func (s *EmbeddedServer) AuthManager() *auth.Manager {
	if s == nil {
		return nil
	}
	if s.Core != nil {
		return s.Core.AuthManager()
	}
	if s.deps != nil {
		return s.deps.AuthManager()
	}
	return nil
}

func (s *EmbeddedServer) OAuthOptions() auth.OpenAIOAuthOptions {
	if s == nil {
		return auth.OpenAIOAuthOptions{}
	}
	if s.Core != nil {
		return s.Core.OAuthOptions()
	}
	if s.deps != nil {
		return s.deps.authSupport.OAuthOptions
	}
	return auth.OpenAIOAuthOptions{}
}

// ServeBackground binds the loopback control endpoints (configured TCP plus the
// derived same-machine Unix socket) and serves the embedded Core in the
// background so external clients — notably `kent run` subagents launched from an
// interactive session — can attach to the in-process server. The listeners live
// until Close, which tears them down before closing the Core; the subagent
// servers therefore stop when the owning session exits, which is intended. It is
// an error to call it more than once.
func (s *EmbeddedServer) ServeBackground() error {
	if s == nil {
		return errors.New("server is required")
	}
	s.rpcMu.Lock()
	defer s.rpcMu.Unlock()
	if s.rpc != nil {
		return errors.New("embedded server is already serving")
	}
	var (
		rpc *runningRPC
		err error
	)
	if s.Core != nil {
		rpc, err = startCoreRPC(s.Core)
	} else if s.deps != nil {
		rpc, err = startGatewayRPC(s.deps, s.cfg)
	} else {
		err = errors.New("startup dependencies are required")
	}
	if err != nil {
		return err
	}
	s.rpc = rpc
	fatalCtx, fatalCancel := context.WithCancel(context.Background())
	s.fatalCancel = fatalCancel
	if failures := workflowExecutionFailures(s.Core, s.deps); failures != nil {
		s.fatalWG.Add(1)
		go s.superviseWorkflowExecutionFailures(fatalCtx, rpc, failures)
	}
	return nil
}

func workflowExecutionFailures(appCore *core.Core, deps *startupGatewayDependencies) <-chan error {
	if appCore != nil {
		return appCore.WorkflowExecutionFailures()
	}
	if deps != nil {
		return deps.workflowExecutionFailures()
	}
	return nil
}

func (s *EmbeddedServer) Failures() <-chan error {
	if s == nil {
		return nil
	}
	s.rpcMu.Lock()
	defer s.rpcMu.Unlock()
	if s.failures == nil {
		s.failures = make(chan error, 1)
	}
	return s.failures
}

func (s *EmbeddedServer) superviseWorkflowExecutionFailures(ctx context.Context, rpc *runningRPC, failures <-chan error) {
	defer s.fatalWG.Done()
	select {
	case <-ctx.Done():
		return
	case fatalErr := <-failures:
		reported := fmt.Errorf("embedded server stopped after fatal workflow execution failure: %w", fatalErr)
		s.rpcMu.Lock()
		s.fatalErr = reported
		if s.failures == nil {
			s.failures = make(chan error, 1)
		}
		s.failures <- reported
		s.rpcMu.Unlock()
		rpc.shutdown()
		s.closeUnderlying()
	}
}

func (s *EmbeddedServer) closeUnderlying() error {
	s.closeOnce.Do(func() {
		if s.Core != nil {
			s.closeErr = s.Core.Close()
		} else if s.deps != nil {
			s.closeErr = s.deps.Close()
		}
	})
	return s.closeErr
}

// Close stops the background control endpoints (if ServeBackground was called)
// and then closes the underlying Core.
func (s *EmbeddedServer) Close() error {
	if s == nil {
		return nil
	}
	s.rpcMu.Lock()
	rpc := s.rpc
	s.rpc = nil
	fatalCancel := s.fatalCancel
	s.fatalCancel = nil
	s.rpcMu.Unlock()
	if fatalCancel != nil {
		fatalCancel()
	}
	if rpc != nil {
		rpc.shutdown()
		rpc.wait()
	}
	s.fatalWG.Wait()
	closeErr := s.closeUnderlying()
	s.rpcMu.Lock()
	fatalErr := s.fatalErr
	s.rpcMu.Unlock()
	return errors.Join(closeErr, fatalErr)
}
