package rpc

import (
	"context"
	"errors"
	"io"
	"log"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/controller"
)

// nonBlockingUserSyncer is an optional capability a Backend can implement
// when SyncUser would otherwise block this stream's read loop for longer
// than users typically arrive apart on it. Without it, a backend whose
// SyncUser blocks until some internal batching/debounce window flushes (see
// singbox.SingBox.QueueUser's doc comment for why sing-box needs exactly
// this) would stall stream.Recv() for that same duration - defeating the
// batching by starving it of the very messages it's meant to coalesce,
// since each message would only be read once the previous one's entire
// wait is already over.
//
// Checked via a type assertion rather than added to the Backend interface
// itself so every other backend (xray, wireguard, openvpn, mtproto) keeps
// its existing, already-safe, strictly-sequential-per-message SyncUser
// behavior on this RPC with zero code or behavior change.
type nonBlockingUserSyncer interface {
	QueueUser(ctx context.Context, user *common.User) error
}

func (s *Service) SyncUser(stream grpc.ClientStreamingServer[common.User, common.Empty]) error {
	backend, err := s.backend()
	if err != nil {
		return err
	}

	queuer, canQueue := backend.(nonBlockingUserSyncer)

	for {
		user, err := stream.Recv()
		if err != nil {
			return stream.SendAndClose(&common.Empty{})
		}

		if user.GetEmail() == "" {
			return errors.New("email is required")
		}

		log.Printf("Got user: %v", user.GetEmail())

		if canQueue {
			if err = queuer.QueueUser(stream.Context(), user); err != nil {
				log.Printf("Error queuing user: %v", err)
				return status.Errorf(codes.Internal, "failed to update user: %v", err)
			}
			continue
		}

		if err = backend.SyncUser(stream.Context(), user); err != nil {
			log.Printf("Error syncing user: %v", err)
			return status.Errorf(codes.Internal, "failed to update user: %v", err)
		}
	}
}

func (s *Service) SyncUsers(ctx context.Context, users *common.Users) (*common.Empty, error) {
	backend, err := s.backend()
	if err != nil {
		return nil, err
	}

	if err := backend.SyncUsers(ctx, users.GetUsers()); err != nil {
		return nil, err
	}

	return nil, nil
}

func (s *Service) SyncUsersChunked(stream grpc.ClientStreamingServer[common.UsersChunk, common.Empty]) error {
	chunks := make(map[uint64][]*common.User)
	var (
		lastIndex uint64
		sawLast   bool
	)

	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return status.Errorf(codes.Internal, "failed to receive chunk: %v", err)
		}

		chunks[chunk.GetIndex()] = append(chunks[chunk.GetIndex()], chunk.GetUsers()...)

		if chunk.GetLast() {
			sawLast = true
			lastIndex = chunk.GetIndex()
			break
		}
	}

	users, err := controller.BuildUsersFromChunks(chunks, lastIndex, sawLast)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	backend, err := s.backend()
	if err != nil {
		return err
	}

	if err := controller.ApplyChunkedUserUpdate(stream.Context(), backend, users); err != nil {
		return status.Errorf(codes.Internal, "failed to update users: %v", err)
	}

	return stream.SendAndClose(&common.Empty{})
}
