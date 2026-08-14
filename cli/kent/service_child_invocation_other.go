//go:build !darwin

package main

import "context"

type serviceChildContainment struct{}

func prepareServiceChildInvocation(_ context.Context, _ string) (serviceChildContainment, error) {
	return serviceChildContainment{}, nil
}

func (serviceChildContainment) Context(ctx context.Context) context.Context {
	return ctx
}

func (serviceChildContainment) Close() {}
