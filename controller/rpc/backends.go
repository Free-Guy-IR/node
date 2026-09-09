package rpc

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/controller"
)

func (s *Service) AddBackend(ctx context.Context, request *common.Backend) (*common.Empty, error) {
	s.LockControl()
	defer s.UnlockControl()

	if err := s.AttachBackend(ctx, request); err != nil {
		if errors.Is(err, controller.ErrBackendTypeNotShareable) {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	return &common.Empty{}, nil
}

func (s *Service) RemoveBackend(_ context.Context, request *common.RemoveBackendRequest) (*common.Empty, error) {
	s.LockControl()
	defer s.UnlockControl()

	if err := s.DetachBackend(request.GetType()); err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return &common.Empty{}, nil
}

func (s *Service) ListBackends(_ context.Context, _ *common.Empty) (*common.BackendList, error) {
	return &common.BackendList{Types: s.BackendTypes()}, nil
}
