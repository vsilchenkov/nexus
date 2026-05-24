package service

import "context"

type Example interface {
	Ping(ctx context.Context) (string, error)
}

type Services struct {
	Example
}

func New(example Example) *Services {
	return &Services{
		Example: example,
	}
}
